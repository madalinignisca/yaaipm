package orchestrator

import (
	"context"
	"fmt"
	"strings"

	"github.com/madalin/forgedesk/internal/models"
)

// planContext holds all the assembled data needed to build a planning prompt.
type planContext struct {
	Project        *models.Project
	Ticket         models.Ticket
	ParentEpic     *models.Ticket
	SiblingTickets []models.Ticket
	Comments       []models.Comment
}

// assembleContext gathers all relevant data for a ticket's planning prompt.
func assembleContext(ctx context.Context, db *models.DB, ticket models.Ticket) (*planContext, error) {
	project, err := db.GetProjectByID(ctx, ticket.ProjectID)
	if err != nil {
		return nil, fmt.Errorf("getting project %s: %w", ticket.ProjectID, err)
	}

	pctx := &planContext{
		Project: project,
		Ticket:  ticket,
	}

	// Get parent epic if this ticket has one
	if ticket.ParentID != nil {
		parent, err := db.GetTicket(ctx, *ticket.ParentID)
		if err != nil {
			return nil, fmt.Errorf("getting parent ticket %s: %w", *ticket.ParentID, err)
		}
		pctx.ParentEpic = parent
	}

	// Get all project tickets for sibling awareness
	siblings, err := db.ListTickets(ctx, ticket.ProjectID, "")
	if err != nil {
		return nil, fmt.Errorf("listing project tickets: %w", err)
	}
	pctx.SiblingTickets = siblings

	// Get existing comments on this ticket
	comments, err := db.ListComments(ctx, ticket.ID)
	if err != nil {
		return nil, fmt.Errorf("listing ticket comments: %w", err)
	}
	pctx.Comments = comments

	return pctx, nil
}

// buildPlanPrompt constructs the system and user prompts for a planning request.
func buildPlanPrompt(pctx *planContext) (systemPrompt, userPrompt string) {
	systemPrompt = `You are a senior software architect working on a project management platform called ForgeDesk. Your task is to analyze a ticket and produce a detailed implementation plan.

Your plan must be written in markdown and include these sections:
1. **Summary** — A brief overview of what needs to be done
2. **Technical Approach** — Architecture decisions, patterns to use, key design choices
3. **Implementation Steps** — Numbered, ordered steps with file paths and descriptions
4. **Database Changes** — Any migrations or schema changes needed (if applicable)
5. **Testing Strategy** — How to verify the implementation works correctly
6. **Risks & Considerations** — Edge cases, potential issues, security concerns

Be specific about file paths, function names, and data structures. The plan should be detailed enough that another developer (or AI agent) can implement it without ambiguity.

Keep the plan focused and practical — no unnecessary boilerplate or generic advice.`

	var b strings.Builder

	// Project brief
	if pctx.Project.BriefMarkdown != "" {
		b.WriteString("## Project Brief\n\n")
		b.WriteString(pctx.Project.BriefMarkdown)
		b.WriteString("\n\n---\n\n")
	}

	// Ticket details
	b.WriteString("## Ticket to Plan\n\n")
	b.WriteString(fmt.Sprintf("**Type:** %s\n", pctx.Ticket.Type))
	b.WriteString(fmt.Sprintf("**Title:** %s\n", pctx.Ticket.Title))
	b.WriteString(fmt.Sprintf("**Priority:** %s\n", pctx.Ticket.Priority))
	b.WriteString(fmt.Sprintf("**Status:** %s\n\n", pctx.Ticket.Status))
	if pctx.Ticket.DescriptionMarkdown != "" {
		b.WriteString("**Description:**\n\n")
		b.WriteString(pctx.Ticket.DescriptionMarkdown)
		b.WriteString("\n\n")
	}

	// Parent epic context
	if pctx.ParentEpic != nil {
		b.WriteString("---\n\n## Parent Epic\n\n")
		b.WriteString(fmt.Sprintf("**Title:** %s\n\n", pctx.ParentEpic.Title))
		if pctx.ParentEpic.DescriptionMarkdown != "" {
			b.WriteString(pctx.ParentEpic.DescriptionMarkdown)
			b.WriteString("\n\n")
		}
	}

	// Sibling tickets (titles + statuses for awareness)
	if len(pctx.SiblingTickets) > 0 {
		b.WriteString("---\n\n## Other Tickets in Project\n\n")
		for _, t := range pctx.SiblingTickets {
			if t.ID == pctx.Ticket.ID {
				continue // skip the current ticket
			}
			b.WriteString(fmt.Sprintf("- [%s] **%s** (%s) — %s\n", t.Status, t.Title, t.Type, t.Priority))
		}
		b.WriteString("\n")
	}

	// Existing comments
	if len(pctx.Comments) > 0 {
		b.WriteString("---\n\n## Existing Discussion\n\n")
		for _, c := range pctx.Comments {
			author := "System"
			if c.AgentName != nil {
				author = "Agent: " + *c.AgentName
			} else if c.UserID != nil {
				author = "User"
			}
			b.WriteString(fmt.Sprintf("**%s** (%s):\n%s\n\n", author, c.CreatedAt.Format("2006-01-02 15:04"), c.BodyMarkdown))
		}
	}

	userPrompt = b.String()
	return systemPrompt, userPrompt
}

// buildImplementPrompt constructs the prompt for an implementation request.
// The agent is instructed to write code in the worktree — not commit or push.
func buildImplementPrompt(pctx *planContext) string {
	var b strings.Builder

	b.WriteString("You are implementing a ticket for a software project. ")
	b.WriteString("Write production-quality code following the project's existing patterns and conventions.\n\n")
	b.WriteString("IMPORTANT RULES:\n")
	b.WriteString("- Do NOT commit or push. Just write the code.\n")
	b.WriteString("- Follow existing code style, naming conventions, and project structure.\n")
	b.WriteString("- Write tests if the project has a test suite.\n")
	b.WriteString("- If you encounter issues, leave TODO comments explaining what needs attention.\n\n")

	// Project brief
	if pctx.Project.BriefMarkdown != "" {
		b.WriteString("## Project Brief\n\n")
		b.WriteString(pctx.Project.BriefMarkdown)
		b.WriteString("\n\n---\n\n")
	}

	// Ticket details
	b.WriteString("## Ticket to Implement\n\n")
	b.WriteString(fmt.Sprintf("**Type:** %s\n", pctx.Ticket.Type))
	b.WriteString(fmt.Sprintf("**Title:** %s\n", pctx.Ticket.Title))
	b.WriteString(fmt.Sprintf("**Priority:** %s\n\n", pctx.Ticket.Priority))
	if pctx.Ticket.DescriptionMarkdown != "" {
		b.WriteString("**Description:**\n\n")
		b.WriteString(pctx.Ticket.DescriptionMarkdown)
		b.WriteString("\n\n")
	}

	// Parent epic context
	if pctx.ParentEpic != nil {
		b.WriteString("---\n\n## Parent Epic\n\n")
		b.WriteString(fmt.Sprintf("**Title:** %s\n\n", pctx.ParentEpic.Title))
		if pctx.ParentEpic.DescriptionMarkdown != "" {
			b.WriteString(pctx.ParentEpic.DescriptionMarkdown)
			b.WriteString("\n\n")
		}
	}

	// Find the approved plan from comments
	for _, c := range pctx.Comments {
		if strings.Contains(c.BodyMarkdown, "## Implementation Plan") {
			b.WriteString("---\n\n## Approved Implementation Plan\n\n")
			b.WriteString("Follow this plan closely:\n\n")
			b.WriteString(c.BodyMarkdown)
			b.WriteString("\n\n")
			break
		}
	}

	return b.String()
}
