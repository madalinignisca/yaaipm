package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"google.golang.org/genai"

	"github.com/madalin/forgedesk/internal/ai"
	"github.com/madalin/forgedesk/internal/middleware"
	"github.com/madalin/forgedesk/internal/models"
	"github.com/madalin/forgedesk/internal/render"
)

type AssistantHandler struct {
	db     *models.DB
	engine *render.Engine
	gemini *ai.GeminiClient
}

func NewAssistantHandler(db *models.DB, engine *render.Engine, gemini *ai.GeminiClient) *AssistantHandler {
	return &AssistantHandler{db: db, engine: engine, gemini: gemini}
}

// CreateConversation creates a new conversation or resumes the latest one.
func (h *AssistantHandler) CreateConversation(w http.ResponseWriter, r *http.Request) {
	user := middleware.GetUser(r)
	if user == nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	projectID := r.FormValue("project_id")
	var projPtr *string
	if projectID != "" {
		projPtr = &projectID
	}

	// Try to resume a recent conversation
	conv, err := h.db.GetLatestAIConversation(r.Context(), user.ID, projPtr)
	if err == nil && conv != nil {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(conv)
		return
	}

	// Create new
	conv, err = h.db.CreateAIConversation(r.Context(), user.ID, projPtr)
	if err != nil {
		log.Printf("creating conversation: %v", err)
		http.Error(w, "Failed to create conversation", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(conv)
}

// ListMessages returns the message history for a conversation.
func (h *AssistantHandler) ListMessages(w http.ResponseWriter, r *http.Request) {
	user := middleware.GetUser(r)
	if user == nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	convID := chi.URLParam(r, "convID")
	conv, err := h.db.GetAIConversation(r.Context(), convID)
	if err != nil {
		http.Error(w, "Conversation not found", http.StatusNotFound)
		return
	}
	if conv.UserID != user.ID {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}

	msgs, err := h.db.ListAIMessages(r.Context(), convID)
	if err != nil {
		log.Printf("listing messages: %v", err)
		http.Error(w, "Failed to list messages", http.StatusInternalServerError)
		return
	}

	if msgs == nil {
		msgs = []models.AIMessage{}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(msgs)
}

// SendMessage handles streaming AI responses via SSE.
func (h *AssistantHandler) SendMessage(w http.ResponseWriter, r *http.Request) {
	user := middleware.GetUser(r)
	if user == nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	if h.gemini == nil {
		http.Error(w, "AI assistant not configured", http.StatusServiceUnavailable)
		return
	}

	convID := chi.URLParam(r, "convID")
	conv, err := h.db.GetAIConversation(r.Context(), convID)
	if err != nil {
		http.Error(w, "Conversation not found", http.StatusNotFound)
		return
	}
	if conv.UserID != user.ID {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}

	content := r.FormValue("content")
	if strings.TrimSpace(content) == "" {
		http.Error(w, "Message content required", http.StatusBadRequest)
		return
	}

	// Save user message
	_, err = h.db.CreateAIMessage(r.Context(), convID, "user", content)
	if err != nil {
		log.Printf("saving user message: %v", err)
		http.Error(w, "Failed to save message", http.StatusInternalServerError)
		return
	}

	// Touch conversation to keep it resumable
	h.db.TouchAIConversation(r.Context(), convID)

	// Build system prompt
	systemPrompt := h.buildSystemPrompt(r.Context(), user, conv)

	// Load conversation history
	msgs, err := h.db.ListAIMessages(r.Context(), convID)
	if err != nil {
		log.Printf("loading history: %v", err)
		http.Error(w, "Failed to load history", http.StatusInternalServerError)
		return
	}

	// Convert to genai content
	var history []*genai.Content
	for _, m := range msgs {
		role := "user"
		if m.Role == "assistant" {
			role = "model"
		}
		history = append(history, &genai.Content{
			Role:  role,
			Parts: []*genai.Part{{Text: m.Content}},
		})
	}

	// Set SSE headers
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "Streaming not supported", http.StatusInternalServerError)
		return
	}

	// Build tool executor with user context
	executor := &toolExecutor{
		db:     h.db,
		userID: user.ID,
		user:   user,
		convID: convID,
	}

	// Stream response
	var fullResponse strings.Builder
	usage, err := h.gemini.StreamChat(r.Context(), ai.ChatOpts{
		SystemPrompt: systemPrompt,
		History:      history,
		Executor:     executor,
	}, func(text string) {
		fullResponse.WriteString(text)
		data, _ := json.Marshal(map[string]string{"text": text})
		fmt.Fprintf(w, "data: %s\n\n", data)
		flusher.Flush()
	})

	if err != nil {
		log.Printf("streaming chat: %v", err)
		errData, _ := json.Marshal(map[string]string{"error": "Failed to generate response"})
		fmt.Fprintf(w, "data: %s\n\n", errData)
		flusher.Flush()
		return
	}

	// Save assistant response
	if fullResponse.Len() > 0 {
		h.db.CreateAIMessage(r.Context(), convID, "assistant", fullResponse.String())
	}

	// Record AI usage for cost tracking
	if usage != nil && (usage.InputTokens > 0 || usage.OutputTokens > 0) {
		h.recordAIUsage(r.Context(), user, conv, usage)
	}

	// Auto-generate title from first exchange
	if conv.Title == "New conversation" && fullResponse.Len() > 0 {
		title := generateTitle(content)
		h.db.UpdateAIConversationTitle(r.Context(), convID, title)
	}

	// Send done event
	donePayload := map[string]any{"done": true}
	if executor.briefUpdated {
		donePayload["reload"] = true
	}
	doneData, _ := json.Marshal(donePayload)
	fmt.Fprintf(w, "data: %s\n\n", doneData)
	flusher.Flush()
}

// DeleteConversation deletes a conversation.
func (h *AssistantHandler) DeleteConversation(w http.ResponseWriter, r *http.Request) {
	user := middleware.GetUser(r)
	if user == nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	convID := chi.URLParam(r, "convID")
	conv, err := h.db.GetAIConversation(r.Context(), convID)
	if err != nil {
		http.Error(w, "Conversation not found", http.StatusNotFound)
		return
	}
	if conv.UserID != user.ID {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}

	if err := h.db.DeleteAIConversation(r.Context(), convID); err != nil {
		http.Error(w, "Failed to delete", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// buildSystemPrompt creates a context-aware system prompt.
func (h *AssistantHandler) buildSystemPrompt(ctx context.Context, user *models.User, conv *models.AIConversation) string {
	var sb strings.Builder
	sb.WriteString("You are ForgeDesk Assistant, an AI helper for project management. ")
	sb.WriteString("You help users manage their projects, tickets, and tasks. ")
	sb.WriteString("Be concise, helpful, and professional. Use markdown formatting in your responses.\n\n")

	sb.WriteString(fmt.Sprintf("Current user: %s (role: %s)\n", user.Name, user.Role))

	if conv.ProjectID != nil {
		proj, err := h.db.GetProjectByID(ctx, *conv.ProjectID)
		if err == nil {
			org, orgErr := h.db.GetOrgByID(ctx, proj.OrgID)
			if orgErr == nil {
				sb.WriteString(fmt.Sprintf("\nCurrent organization: %s\n", org.Name))
			}
			sb.WriteString(fmt.Sprintf("Current project: %s (ID: %s)\n", proj.Name, proj.ID))
			sb.WriteString("You are scoped to this project. Use this project's ID for all tool calls.\n")
			if proj.BriefMarkdown != "" {
				brief := proj.BriefMarkdown
				if len(brief) > 2000 {
					brief = brief[:2000] + "...(truncated)"
				}
				sb.WriteString(fmt.Sprintf("\nCurrent project brief:\n%s\n", brief))
			} else {
				sb.WriteString("\nThis project has no brief yet. You can create one with update_project_brief.\n")
			}
		}
	}

	sb.WriteString("\nTicket hierarchy:\n")
	sb.WriteString("- Epics are top-level features (no parent). The Features tab shows epics.\n")
	sb.WriteString("- Tasks are work items that belong UNDER an epic (parent_id = epic ID).\n")
	sb.WriteString("- Subtasks belong under a task (parent_id = task ID).\n")
	sb.WriteString("- Bugs are top-level (no parent). The Bugs tab shows bugs.\n")
	sb.WriteString("- When asked to 'add tasks to an epic', create tickets with type='task' and parent_id set to the epic's ID.\n")
	sb.WriteString("- NEVER create an epic when the user asks for a task — use type='task' with a parent_id instead.\n\n")

	sb.WriteString("When using tools:\n")
	sb.WriteString("- Use search_tickets to find existing tickets before creating duplicates\n")
	sb.WriteString("- When adding tasks to an epic, first search for the epic to get its ID, then create tasks with that parent_id\n")
	sb.WriteString("- Use the current project ID when creating tickets or searching\n")
	sb.WriteString("- Use update_project_brief to write or update the project brief when the user asks you to\n")
	sb.WriteString("- When updating the brief, write well-structured markdown with headings, lists, and sections\n")
	sb.WriteString("- Confirm destructive actions with the user before executing\n")
	sb.WriteString("- Report tool results clearly to the user\n")

	return sb.String()
}

// recordAIUsage writes an ai_usage_entries row with raw token counts.
// Cost is computed at query time by joining with ai_model_pricing.
func (h *AssistantHandler) recordAIUsage(ctx context.Context, user *models.User, conv *models.AIConversation, usage *ai.UsageData) {
	// Determine org ID from the conversation's project, or fall back to user's first org
	var orgID string
	if conv.ProjectID != nil {
		proj, err := h.db.GetProjectByID(ctx, *conv.ProjectID)
		if err == nil {
			orgID = proj.OrgID
		}
	}
	if orgID == "" {
		orgs, err := h.db.ListUserOrgs(ctx, user.ID)
		if err == nil && len(orgs) > 0 {
			orgID = orgs[0].ID
		}
	}
	if orgID == "" {
		log.Printf("ai usage: could not determine org for user %s", user.ID)
		return
	}

	if err := h.db.CreateAIUsageEntry(ctx, orgID, conv.ProjectID, user.ID, usage.Model, "Chat message",
		int(usage.InputTokens), int(usage.OutputTokens), 0); err != nil {
		log.Printf("recording ai usage: %v", err)
	}
}

// generateTitle creates a short title from the first user message.
func generateTitle(firstMessage string) string {
	title := firstMessage
	if len(title) > 60 {
		title = title[:57] + "..."
	}
	// Remove newlines
	title = strings.ReplaceAll(title, "\n", " ")
	title = strings.TrimSpace(title)
	if title == "" {
		title = "New conversation"
	}
	return title
}

// ── Tool Executor ────────────────────────────────────────────

type toolExecutor struct {
	db           *models.DB
	userID       string
	user         *models.User
	convID       string
	briefUpdated bool // set when update_project_brief is called successfully
}

func (e *toolExecutor) Execute(ctx context.Context, name string, args map[string]any) (map[string]any, error) {
	switch name {
	case "search_tickets":
		return e.searchTickets(ctx, args)
	case "get_project_brief":
		return e.getProjectBrief(ctx, args)
	case "create_ticket":
		return e.createTicket(ctx, args)
	case "update_ticket_status":
		return e.updateTicketStatus(ctx, args)
	case "post_comment":
		return e.postComment(ctx, args)
	case "update_project_brief":
		return e.updateProjectBrief(ctx, args)
	default:
		return nil, fmt.Errorf("unknown tool: %s", name)
	}
}

func (e *toolExecutor) searchTickets(ctx context.Context, args map[string]any) (map[string]any, error) {
	projectID, _ := args["project_id"].(string)
	query, _ := args["query"].(string)
	if query == "" {
		return map[string]any{"error": "query is required"}, nil
	}

	// If no project_id in args, try to get it from the conversation
	if projectID == "" {
		conv, err := e.db.GetAIConversation(ctx, e.convID)
		if err == nil && conv.ProjectID != nil {
			projectID = *conv.ProjectID
		}
	}
	if projectID == "" {
		return map[string]any{"error": "no project context available"}, nil
	}

	// Check user access to the project
	if err := e.checkProjectAccess(ctx, projectID); err != nil {
		return map[string]any{"error": err.Error()}, nil
	}

	var ticketType, status *string
	if t, ok := args["type"].(string); ok && t != "" {
		ticketType = &t
	}
	if s, ok := args["status"].(string); ok && s != "" {
		status = &s
	}

	tickets, err := e.db.SearchTickets(ctx, projectID, query, ticketType, status)
	if err != nil {
		return nil, fmt.Errorf("searching tickets: %w", err)
	}

	var results []map[string]any
	for _, t := range tickets {
		results = append(results, map[string]any{
			"id":       t.ID,
			"title":    t.Title,
			"type":     t.Type,
			"status":   t.Status,
			"priority": t.Priority,
		})
	}

	return map[string]any{"tickets": results, "count": len(results)}, nil
}

func (e *toolExecutor) getProjectBrief(ctx context.Context, args map[string]any) (map[string]any, error) {
	projectID, _ := args["project_id"].(string)
	if projectID == "" {
		conv, err := e.db.GetAIConversation(ctx, e.convID)
		if err == nil && conv.ProjectID != nil {
			projectID = *conv.ProjectID
		}
	}
	if projectID == "" {
		return map[string]any{"error": "project_id is required"}, nil
	}

	if err := e.checkProjectAccess(ctx, projectID); err != nil {
		return map[string]any{"error": err.Error()}, nil
	}

	proj, err := e.db.GetProjectByID(ctx, projectID)
	if err != nil {
		return map[string]any{"error": "project not found"}, nil
	}

	return map[string]any{
		"project_name": proj.Name,
		"brief":        proj.BriefMarkdown,
	}, nil
}

func (e *toolExecutor) createTicket(ctx context.Context, args map[string]any) (map[string]any, error) {
	projectID, _ := args["project_id"].(string)
	if projectID == "" {
		conv, err := e.db.GetAIConversation(ctx, e.convID)
		if err == nil && conv.ProjectID != nil {
			projectID = *conv.ProjectID
		}
	}
	if projectID == "" {
		return map[string]any{"error": "project_id is required"}, nil
	}

	if err := e.checkProjectAccess(ctx, projectID); err != nil {
		return map[string]any{"error": err.Error()}, nil
	}

	title, _ := args["title"].(string)
	ticketType, _ := args["type"].(string)
	priority, _ := args["priority"].(string)
	desc, _ := args["description"].(string)
	parentID, _ := args["parent_id"].(string)

	if title == "" || ticketType == "" || priority == "" {
		return map[string]any{"error": "title, type, and priority are required"}, nil
	}

	// Validate parent_id for tasks/subtasks
	if (ticketType == "task" || ticketType == "subtask") && parentID == "" {
		return map[string]any{"error": ticketType + "s require a parent_id (use search_tickets to find the parent epic/task first)"}, nil
	}

	ticket := &models.Ticket{
		ProjectID:           projectID,
		Type:                ticketType,
		Title:               title,
		DescriptionMarkdown: desc,
		Status:              "backlog",
		Priority:            priority,
		CreatedBy:           e.userID,
	}
	if parentID != "" {
		ticket.ParentID = &parentID
	}

	if err := e.db.CreateTicket(ctx, ticket); err != nil {
		return nil, fmt.Errorf("creating ticket: %w", err)
	}

	return map[string]any{
		"id":       ticket.ID,
		"title":    ticket.Title,
		"type":     ticket.Type,
		"status":   ticket.Status,
		"priority": ticket.Priority,
		"message":  fmt.Sprintf("Ticket '%s' created successfully", ticket.Title),
	}, nil
}

func (e *toolExecutor) updateTicketStatus(ctx context.Context, args map[string]any) (map[string]any, error) {
	ticketID, _ := args["ticket_id"].(string)
	newStatus, _ := args["new_status"].(string)

	if ticketID == "" || newStatus == "" {
		return map[string]any{"error": "ticket_id and new_status are required"}, nil
	}

	ticket, err := e.db.GetTicket(ctx, ticketID)
	if err != nil {
		return map[string]any{"error": "ticket not found"}, nil
	}

	if err := e.checkProjectAccess(ctx, ticket.ProjectID); err != nil {
		return map[string]any{"error": err.Error()}, nil
	}

	oldStatus := ticket.Status
	if err := e.db.UpdateTicketStatus(ctx, ticketID, newStatus); err != nil {
		return nil, fmt.Errorf("updating status: %w", err)
	}

	return map[string]any{
		"ticket_id":  ticketID,
		"old_status": oldStatus,
		"new_status": newStatus,
		"message":    fmt.Sprintf("Status updated from '%s' to '%s'", oldStatus, newStatus),
	}, nil
}

func (e *toolExecutor) postComment(ctx context.Context, args map[string]any) (map[string]any, error) {
	ticketID, _ := args["ticket_id"].(string)
	body, _ := args["body"].(string)

	if ticketID == "" || body == "" {
		return map[string]any{"error": "ticket_id and body are required"}, nil
	}

	ticket, err := e.db.GetTicket(ctx, ticketID)
	if err != nil {
		return map[string]any{"error": "ticket not found"}, nil
	}

	if err := e.checkProjectAccess(ctx, ticket.ProjectID); err != nil {
		return map[string]any{"error": err.Error()}, nil
	}

	comment, err := e.db.CreateComment(ctx, ticketID, &e.userID, nil, body)
	if err != nil {
		return nil, fmt.Errorf("creating comment: %w", err)
	}

	return map[string]any{
		"comment_id": comment.ID,
		"ticket_id":  ticketID,
		"message":    "Comment posted successfully",
	}, nil
}

func (e *toolExecutor) updateProjectBrief(ctx context.Context, args map[string]any) (map[string]any, error) {
	projectID, _ := args["project_id"].(string)
	if projectID == "" {
		conv, err := e.db.GetAIConversation(ctx, e.convID)
		if err == nil && conv.ProjectID != nil {
			projectID = *conv.ProjectID
		}
	}
	if projectID == "" {
		return map[string]any{"error": "project_id is required — no project context available"}, nil
	}

	if err := e.checkProjectAccess(ctx, projectID); err != nil {
		return map[string]any{"error": err.Error()}, nil
	}

	briefMarkdown, _ := args["brief_markdown"].(string)
	if strings.TrimSpace(briefMarkdown) == "" {
		return map[string]any{"error": "brief_markdown is required"}, nil
	}

	if err := e.db.UpdateProjectBrief(ctx, projectID, briefMarkdown); err != nil {
		return nil, fmt.Errorf("updating project brief: %w", err)
	}

	e.briefUpdated = true

	return map[string]any{
		"project_id": projectID,
		"message":    "Project brief updated successfully",
	}, nil
}

// checkProjectAccess verifies the user has access to the project via org membership.
func (e *toolExecutor) checkProjectAccess(ctx context.Context, projectID string) error {
	// Staff and superadmin can access all projects
	if e.user.Role == "superadmin" || e.user.Role == "staff" {
		return nil
	}

	proj, err := e.db.GetProjectByID(ctx, projectID)
	if err != nil {
		return fmt.Errorf("project not found")
	}

	_, err = e.db.GetOrgMembership(ctx, e.userID, proj.OrgID)
	if err != nil {
		return fmt.Errorf("you don't have access to this project")
	}

	return nil
}
