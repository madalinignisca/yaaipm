package orchestrator

import (
	"context"
	"fmt"
	"log"
	"sync"

	"github.com/madalin/forgedesk/internal/ai"
	"github.com/madalin/forgedesk/internal/config"
	"github.com/madalin/forgedesk/internal/models"
)

// Dispatcher processes agent-ready tickets by dispatching them to AI agents.
type Dispatcher struct {
	db        *models.DB
	claude    *ai.AnthropicClient
	cfg       *config.Config
	workspace *WorkspaceManager
	mu        sync.Mutex
}

// NewDispatcher creates a new Dispatcher.
func NewDispatcher(db *models.DB, claude *ai.AnthropicClient, cfg *config.Config) *Dispatcher {
	return &Dispatcher{
		db:        db,
		claude:    claude,
		cfg:       cfg,
		workspace: NewWorkspaceManager(cfg.WorkspacesDir),
	}
}

// ProcessTickets finds all agent-ready tickets and dispatches them.
// Uses TryLock to prevent concurrent runs from overlapping.
func (d *Dispatcher) ProcessTickets(ctx context.Context) {
	if !d.mu.TryLock() {
		log.Println("ProcessTickets already running, skipping")
		return
	}
	defer d.mu.Unlock()

	tickets, err := d.db.ListAgentReady(ctx)
	if err != nil {
		log.Printf("fetching agent-ready tickets: %v", err)
		return
	}

	if len(tickets) == 0 {
		return
	}

	log.Printf("Found %d tickets ready for agent processing", len(tickets))

	for _, t := range tickets {
		agentMode := "plan"
		if t.AgentMode != nil {
			agentMode = *t.AgentMode
		}

		agentName := agentClaude
		if t.AgentName != nil {
			agentName = *t.AgentName
		}

		log.Printf("Processing ticket %s: mode=%s agent=%s title=%q", t.ID, agentMode, agentName, t.Title)

		switch agentMode {
		case "plan":
			d.handlePlan(ctx, t)
		case "implement":
			d.handleImplement(ctx, t)
		}
	}
}

// handlePlan assembles context, calls Claude to generate a plan, and posts it as a comment.
func (d *Dispatcher) handlePlan(ctx context.Context, ticket models.Ticket) {
	agentName := agentClaude
	if ticket.AgentName != nil {
		agentName = *ticket.AgentName
	}

	// Claim the ticket immediately to prevent re-pickup on next poll
	if err := d.db.UpdateTicketStatus(ctx, ticket.ID, "planning"); err != nil {
		log.Printf("setting ticket %s to planning: %v", ticket.ID, err)
		return
	}

	// Assemble context
	pctx, err := assembleContext(ctx, d.db, ticket)
	if err != nil {
		d.postError(ctx, ticket, agentName, fmt.Errorf("assembling context: %w", err))
		return
	}

	systemPrompt, userPrompt := buildPlanPrompt(pctx)

	// Call Claude
	text, usage, err := d.claude.GenerateResponse(ctx, d.cfg.AnthropicModel, systemPrompt, userPrompt, 16000)
	if err != nil {
		d.postError(ctx, ticket, agentName, fmt.Errorf("calling anthropic API: %w", err))
		return
	}

	// Post the plan as a comment
	body := fmt.Sprintf("## Implementation Plan\n\n%s", text)
	if _, err := d.db.CreateComment(ctx, ticket.ID, nil, &agentName, body); err != nil {
		log.Printf("posting plan comment on ticket %s: %v", ticket.ID, err)
		// Continue — still try to record usage and transition status
	}

	// Record AI usage
	if usage != nil {
		d.recordUsage(ctx, ticket, pctx.Project, usage, "Agent Planning")
	}

	// Transition to plan_review and clear agent_mode
	if err := d.db.UpdateTicketStatus(ctx, ticket.ID, "plan_review"); err != nil {
		log.Printf("setting ticket %s to plan_review: %v", ticket.ID, err)
	}
	if err := d.db.UpdateTicketAgentMode(ctx, ticket.ID, nil, ticket.AgentName); err != nil {
		log.Printf("clearing agent_mode on ticket %s: %v", ticket.ID, err)
	}

	log.Printf("Plan completed for ticket %s (%d input tokens, %d output tokens)", ticket.ID, usage.InputTokens, usage.OutputTokens)
}

// handleImplement creates a worktree, runs the agent CLI, commits, and pushes.
func (d *Dispatcher) handleImplement(ctx context.Context, ticket models.Ticket) {
	agentName := agentClaude
	if ticket.AgentName != nil {
		agentName = *ticket.AgentName
	}

	// 1. Claim ticket → implementing
	if err := d.db.UpdateTicketStatus(ctx, ticket.ID, "implementing"); err != nil {
		log.Printf("setting ticket %s to implementing: %v", ticket.ID, err)
		return
	}

	// 2. Assemble context
	pctx, err := assembleContext(ctx, d.db, ticket)
	if err != nil {
		d.postError(ctx, ticket, agentName, fmt.Errorf("assembling context: %w", err))
		return
	}

	// 3. Get org for slug (needed for workspace paths)
	org, err := d.db.GetOrgByID(ctx, pctx.Project.OrgID)
	if err != nil {
		d.postError(ctx, ticket, agentName, fmt.Errorf("getting org: %w", err))
		return
	}

	// 4. Validate repo URL
	if pctx.Project.RepoURL == "" {
		d.postError(ctx, ticket, agentName, fmt.Errorf("project %q has no repository URL configured — set it in project settings", pctx.Project.Name))
		return
	}

	// 5. Ensure repo is cloned / up to date
	if _, repoErr := d.workspace.EnsureRepo(ctx, org.Slug, pctx.Project.Slug, pctx.Project.RepoURL); repoErr != nil {
		d.postError(ctx, ticket, agentName, fmt.Errorf("ensuring repo: %w", repoErr))
		return
	}

	// 6. Create worktree
	wtDir, branchName, err := d.workspace.CreateWorktree(ctx, org.Slug, pctx.Project.Slug, ticket.ID, ticket.Title)
	if err != nil {
		d.postError(ctx, ticket, agentName, fmt.Errorf("creating worktree: %w", err))
		return
	}
	log.Printf("Worktree created at %s on branch %s", wtDir, branchName)

	// 7. Post starting comment
	startBody := fmt.Sprintf("[Orchestrator] Starting implementation with **%s** on branch `%s`.", agentName, branchName)
	_, _ = d.db.CreateComment(ctx, ticket.ID, nil, &agentName, startBody)

	// 8. Build prompt and run agent
	prompt := buildImplementPrompt(pctx)
	output, agentErr := runAgentCLI(ctx, agentName, wtDir, prompt)

	// 9. Post agent output as comment (even on error — partial output is useful)
	if output != "" {
		outputBody := fmt.Sprintf("## Agent Output\n\n```\n%s\n```", truncate(output, 10000))
		if _, commentErr := d.db.CreateComment(ctx, ticket.ID, nil, &agentName, outputBody); commentErr != nil {
			log.Printf("posting agent output on ticket %s: %v", ticket.ID, commentErr)
		}
	}

	if agentErr != nil {
		d.postError(ctx, ticket, agentName, fmt.Errorf("agent execution: %w", agentErr))
		return
	}

	// 10. Commit and push
	committed, err := commitAndPush(ctx, wtDir, ticket, branchName)
	if err != nil {
		d.postError(ctx, ticket, agentName, fmt.Errorf("commit/push: %w", err))
		return
	}

	if committed {
		pushBody := fmt.Sprintf("[Orchestrator] Code pushed to branch `%s`. Ready for review.", branchName)
		_, _ = d.db.CreateComment(ctx, ticket.ID, nil, &agentName, pushBody)
	} else {
		noChangeBody := "[Orchestrator] Agent completed but produced no file changes."
		_, _ = d.db.CreateComment(ctx, ticket.ID, nil, &agentName, noChangeBody)
	}

	// 11. Transition to testing and clear agent_mode
	if err := d.db.UpdateTicketStatus(ctx, ticket.ID, "testing"); err != nil {
		log.Printf("setting ticket %s to testing: %v", ticket.ID, err)
	}
	if err := d.db.UpdateTicketAgentMode(ctx, ticket.ID, nil, ticket.AgentName); err != nil {
		log.Printf("clearing agent_mode on ticket %s: %v", ticket.ID, err)
	}

	log.Printf("Implementation completed for ticket %s on branch %s", ticket.ID, branchName)
}

// postError posts an error comment on the ticket and clears agent_mode.
func (d *Dispatcher) postError(ctx context.Context, ticket models.Ticket, agentName string, err error) {
	log.Printf("Error processing ticket %s: %v", ticket.ID, err)

	body := fmt.Sprintf("[Orchestrator] Error during processing:\n\n```\n%s\n```\n\nA staff member can retry by setting the agent mode again.", err)
	if _, cerr := d.db.CreateComment(ctx, ticket.ID, nil, &agentName, body); cerr != nil {
		log.Printf("posting error comment on ticket %s: %v", ticket.ID, cerr)
	}

	// Clear agent_mode to prevent infinite retry
	if clearErr := d.db.UpdateTicketAgentMode(ctx, ticket.ID, nil, ticket.AgentName); clearErr != nil {
		log.Printf("clearing agent_mode on ticket %s: %v", ticket.ID, clearErr)
	}
}

// recordUsage calculates and records AI cost in the usage tracking table.
func (d *Dispatcher) recordUsage(ctx context.Context, ticket models.Ticket, project *models.Project, usage *ai.UsageData, label string) {
	costCents := d.cfg.CalculateAICost(usage.Model, usage.InputTokens, usage.OutputTokens, usage.HasImageOutput)
	projectID := project.ID
	if err := d.db.CreateAIUsageEntry(ctx, project.OrgID, &projectID, nil, usage.Model, label,
		int(usage.InputTokens), int(usage.OutputTokens), costCents); err != nil {
		log.Printf("recording ai usage for ticket %s: %v", ticket.ID, err)
	}
}
