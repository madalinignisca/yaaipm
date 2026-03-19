package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/gorilla/websocket"
	"google.golang.org/genai"

	"github.com/madalin/forgedesk/internal/ai"
	"github.com/madalin/forgedesk/internal/auth"
	"github.com/madalin/forgedesk/internal/config"
	"github.com/madalin/forgedesk/internal/middleware"
	"github.com/madalin/forgedesk/internal/models"
	"github.com/madalin/forgedesk/internal/render"
	"github.com/madalin/forgedesk/internal/ws"
)

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool {
		origin := r.Header.Get("Origin")
		if origin == "" {
			return true // same-origin requests may omit Origin
		}
		u, err := url.Parse(origin)
		if err != nil {
			return false
		}
		return u.Host == r.Host
	},
}

type AssistantHandler struct {
	db     *models.DB
	engine *render.Engine
	gemini *ai.GeminiClient
	hub    *ws.Hub
	cfg    *config.Config
}

func NewAssistantHandler(db *models.DB, engine *render.Engine, gemini *ai.GeminiClient, hub *ws.Hub, cfg *config.Config) *AssistantHandler {
	return &AssistantHandler{db: db, engine: engine, gemini: gemini, hub: hub, cfg: cfg}
}

// ── WebSocket Message Types ──────────────────────────────────

type wsMessage struct {
	Type string          `json:"type"`
	Data json.RawMessage `json:"data,omitempty"`
}

type sendMessageData struct {
	Content string `json:"content"`
}

type userMessageData struct {
	ID        string `json:"id"`
	UserID    string `json:"user_id"`
	UserName  string `json:"user_name"`
	Content   string `json:"content"`
	CreatedAt string `json:"created_at"`
}

type historyMessage struct {
	ID        string  `json:"id"`
	Role      string  `json:"role"`
	Content   string  `json:"content"`
	UserID    *string `json:"user_id,omitempty"`
	UserName  string  `json:"user_name,omitempty"`
	CreatedAt string  `json:"created_at"`
}

// ── WebSocket Handler ────────────────────────────────────────

// HandleWebSocket upgrades the connection and manages the shared project chat.
func (h *AssistantHandler) HandleWebSocket(w http.ResponseWriter, r *http.Request) {
	user := middleware.GetUser(r)
	if user == nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	projectID := chi.URLParam(r, "projectID")
	if projectID == "" {
		http.Error(w, "Project ID required", http.StatusBadRequest)
		return
	}

	// Verify user has access to the project
	if err := h.checkProjectAccess(r.Context(), user, projectID); err != nil {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}

	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("ws upgrade error: %v", err)
		return
	}

	client := ws.NewClient(h.hub, conn, projectID, user.ID, user.Name, user, func(c *ws.Client, data []byte) { //nolint:contextcheck // websocket callback
		h.handleClientMessage(c, data)
	})

	h.hub.Register(client)

	// Send conversation info and history
	h.sendInitialState(client, projectID) //nolint:contextcheck // websocket goroutine

	go client.WritePump()
	go client.ReadPump()
}

// sendInitialState sends conversation info and message history to a newly connected client.
func (h *AssistantHandler) sendInitialState(client *ws.Client, projectID string) {
	ctx := context.Background()

	// Get or create the shared project conversation
	conv, err := h.db.GetOrCreateProjectConversation(ctx, projectID, client.UserID)
	if err != nil {
		log.Printf("ws: get/create conversation error: %v", err)
		h.sendError(client, "Failed to load conversation")
		return
	}

	// Send conversation ID
	convInfo, _ := json.Marshal(wsMessage{ //nolint:errchkjson // marshal cannot fail
		Type: "conv_info",
		Data: mustJSON(map[string]string{"conversation_id": conv.ID}),
	})
	client.Send(convInfo)

	// Send message history
	msgs, err := h.db.ListAIMessages(ctx, conv.ID)
	if err != nil {
		log.Printf("ws: list messages error: %v", err)
		h.sendError(client, "Failed to load history")
		return
	}

	var history []historyMessage
	for _, m := range msgs {
		history = append(history, historyMessage{
			ID:        m.ID,
			Role:      m.Role,
			Content:   m.Content,
			UserID:    m.UserID,
			UserName:  m.UserName,
			CreatedAt: m.CreatedAt.Format("2006-01-02T15:04:05Z"),
		})
	}
	if history == nil {
		history = []historyMessage{}
	}

	historyMsg, _ := json.Marshal(wsMessage{ //nolint:errchkjson // marshal cannot fail
		Type: "history",
		Data: mustJSON(history),
	})
	client.Send(historyMsg)
}

// handleClientMessage dispatches incoming WebSocket messages.
func (h *AssistantHandler) handleClientMessage(client *ws.Client, data []byte) {
	var msg wsMessage
	if err := json.Unmarshal(data, &msg); err != nil {
		log.Printf("ws: invalid message from %s: %v", client.UserID, err)
		return
	}

	if msg.Type == "send_message" {
		var payload sendMessageData
		if err := json.Unmarshal(msg.Data, &payload); err != nil {
			return
		}
		h.handleSendMessage(client, payload.Content)
	}
}

// handleSendMessage saves the user message, broadcasts it, and triggers AI response.
func (h *AssistantHandler) handleSendMessage(client *ws.Client, content string) {
	content = strings.TrimSpace(content)
	if content == "" {
		return
	}

	ctx := context.Background()

	// Get the project conversation
	conv, err := h.db.GetOrCreateProjectConversation(ctx, client.ProjectID, client.UserID)
	if err != nil {
		log.Printf("ws: conversation error: %v", err)
		h.sendError(client, "Failed to access conversation")
		return
	}

	// Save user message with attribution
	msg, err := h.db.CreateAIMessageWithUser(ctx, conv.ID, roleUser, content, &client.UserID, client.UserName)
	if err != nil {
		log.Printf("ws: save message error: %v", err)
		h.sendError(client, "Failed to save message")
		return
	}

	// Touch conversation
	_ = h.db.TouchAIConversation(ctx, conv.ID)

	// Broadcast user message to all clients in the room
	userMsg, _ := json.Marshal(wsMessage{ //nolint:errchkjson // marshal cannot fail
		Type: "user_message",
		Data: mustJSON(userMessageData{
			ID:        msg.ID,
			UserID:    client.UserID,
			UserName:  client.UserName,
			Content:   content,
			CreatedAt: msg.CreatedAt.Format("2006-01-02T15:04:05Z"),
		}),
	})
	h.hub.BroadcastAll(client.ProjectID, userMsg)

	// Trigger AI response in background
	if h.gemini != nil {
		go h.streamAIResponse(client, conv)
	}
}

// streamAIResponse generates and streams an AI response to all clients in the project room.
func (h *AssistantHandler) streamAIResponse(client *ws.Client, conv *models.AIConversation) {
	// Acquire AI lock — blocks if another AI call is in progress for this project
	release := h.hub.AcquireAILock(client.ProjectID)
	defer release()

	ctx := context.Background()

	// Broadcast typing indicator
	typing, _ := json.Marshal(wsMessage{Type: "ai_typing"}) //nolint:errchkjson // marshal cannot fail
	h.hub.BroadcastAll(client.ProjectID, typing)

	// Build system prompt with multi-user context
	systemPrompt := h.buildSystemPrompt(ctx, client.User, conv)

	// Load conversation history
	msgs, err := h.db.ListAIMessages(ctx, conv.ID)
	if err != nil {
		log.Printf("ws: load history error: %v", err)
		h.broadcastAIError(client.ProjectID, "Failed to load conversation history")
		return
	}

	// Convert to genai content, prefixing user messages with sender name
	var history []*genai.Content
	for _, m := range msgs {
		role := roleUser
		if m.Role == "assistant" {
			role = "model"
		}
		messageContent := m.Content
		if m.Role == roleUser && m.UserName != "" {
			messageContent = fmt.Sprintf("[%s]: %s", m.UserName, m.Content)
		}
		history = append(history, &genai.Content{
			Role:  role,
			Parts: []*genai.Part{{Text: messageContent}},
		})
	}

	// Build tool executor with sender's user context
	executor := &toolExecutor{
		db:     h.db,
		userID: client.UserID,
		user:   client.User,
		convID: conv.ID,
	}

	// Stream response
	var fullResponse strings.Builder
	usage, err := h.gemini.StreamChat(ctx, ai.ChatOpts{
		SystemPrompt: systemPrompt,
		History:      history,
		Executor:     executor,
	}, func(text string) {
		fullResponse.WriteString(text)
		chunk, _ := json.Marshal(wsMessage{
			Type: "ai_chunk",
			Data: mustJSON(map[string]string{"text": text}),
		})
		h.hub.BroadcastAll(client.ProjectID, chunk)
	})

	if err != nil {
		log.Printf("ws: ai stream error: %v", err)
		h.broadcastAIError(client.ProjectID, "Failed to generate response")
		return
	}

	// Save assistant response
	if fullResponse.Len() > 0 {
		_, _ = h.db.CreateAIMessage(ctx, conv.ID, "assistant", fullResponse.String())
	}

	// Record AI usage
	if usage != nil && (usage.InputTokens > 0 || usage.OutputTokens > 0) {
		h.recordAIUsage(ctx, client.User, conv, usage)
	}

	// Auto-generate title
	if conv.Title == "New conversation" && fullResponse.Len() > 0 {
		// Get the first user message for title generation
		if len(msgs) > 0 {
			for _, m := range msgs {
				if m.Role == roleUser {
					title := generateTitle(m.Content)
					_ = h.db.UpdateAIConversationTitle(ctx, conv.ID, title)
					break
				}
			}
		}
	}

	// Broadcast done
	done, _ := json.Marshal(wsMessage{ //nolint:errchkjson // marshal cannot fail
		Type: "ai_done",
		Data: mustJSON(map[string]bool{"reload": executor.briefUpdated}),
	})
	h.hub.BroadcastAll(client.ProjectID, done)
}

// ── Helper Methods ───────────────────────────────────────────

func (h *AssistantHandler) sendError(client *ws.Client, msg string) {
	data, _ := json.Marshal(wsMessage{ //nolint:errchkjson // marshal cannot fail
		Type: "ai_error",
		Data: mustJSON(map[string]string{"error": msg}),
	})
	client.Send(data)
}

func (h *AssistantHandler) broadcastAIError(projectID, msg string) {
	data, _ := json.Marshal(wsMessage{ //nolint:errchkjson // marshal cannot fail
		Type: "ai_error",
		Data: mustJSON(map[string]string{"error": msg}),
	})
	h.hub.BroadcastAll(projectID, data)
}

func mustJSON(v any) json.RawMessage {
	data, _ := json.Marshal(v) //nolint:errchkjson // marshal cannot fail
	return data
}

// checkProjectAccess verifies the user has access to a project.
func (h *AssistantHandler) checkProjectAccess(ctx context.Context, user *models.User, projectID string) error {
	if user.Role == roleSuperadmin || user.Role == roleStaff {
		return nil
	}
	proj, err := h.db.GetProjectByID(ctx, projectID)
	if err != nil {
		return fmt.Errorf("project not found")
	}
	_, err = h.db.GetOrgMembership(ctx, user.ID, proj.OrgID)
	if err != nil {
		return fmt.Errorf("no access to project")
	}
	return nil
}

// DeleteConversation deletes a conversation (staff/superadmin only).
func (h *AssistantHandler) DeleteConversation(w http.ResponseWriter, r *http.Request) {
	user := middleware.GetUser(r)
	if user == nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	if user.Role != "superadmin" && user.Role != "staff" {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}

	convID := chi.URLParam(r, "convID")
	if err := h.db.DeleteAIConversation(r.Context(), convID); err != nil {
		http.Error(w, "Failed to delete", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// buildSystemPrompt creates a context-aware system prompt.
//
//nolint:nestif // system prompt builder requires sequential checks
func (h *AssistantHandler) buildSystemPrompt(ctx context.Context, user *models.User, conv *models.AIConversation) string {
	var sb strings.Builder
	sb.WriteString("You are Simona, the ForgeDesk AI assistant for project management. ")
	sb.WriteString("You help users manage their projects, tickets, and tasks. ")
	sb.WriteString("Be concise, helpful, and professional. Use markdown formatting in your responses.\n\n")

	sb.WriteString("This is a shared project chat. Multiple team members may be participating. ")
	sb.WriteString("User messages are prefixed with [Name] to identify who is speaking. ")
	sb.WriteString("Address users by name when appropriate.\n\n")

	fmt.Fprintf(&sb, "Current user: %s (role: %s)\n", sanitizeForPrompt(user.Name, 100), user.Role)

	if conv.ProjectID != nil {
		proj, err := h.db.GetProjectByID(ctx, *conv.ProjectID)
		if err == nil {
			org, orgErr := h.db.GetOrgByID(ctx, proj.OrgID)
			if orgErr == nil {
				fmt.Fprintf(&sb, "\nCurrent organization: %s\n", org.Name)
			}
			fmt.Fprintf(&sb, "Current project: %s (ID: %s)\n", sanitizeForPrompt(proj.Name, 200), proj.ID)
			sb.WriteString("You are scoped to this project. Use this project's ID for all tool calls.\n")
			if proj.BriefMarkdown != "" {
				brief := proj.BriefMarkdown
				if len(brief) > 2000 {
					brief = brief[:2000] + "...(truncated)"
				}
				fmt.Fprintf(&sb, "\nCurrent project brief:\n%s\n", brief)
			} else {
				sb.WriteString("\nThis project has no brief yet. You can create one with update_project_brief.\n")
			}
		}
	}

	sb.WriteString("\nTicket hierarchy:\n")
	sb.WriteString("- Features are top-level items (no parent). The Features tab shows features.\n")
	sb.WriteString("- Tasks are work items that belong UNDER a feature (parent_id = feature ID).\n")
	sb.WriteString("- Subtasks belong under a task (parent_id = task ID).\n")
	sb.WriteString("- Bugs are top-level (no parent). The Bugs tab shows bugs.\n")
	sb.WriteString("- When asked to 'add tasks to a feature', create tickets with type='task' and parent_id set to the feature's ID.\n")
	sb.WriteString("- NEVER create a feature when the user asks for a task — use type='task' with a parent_id instead.\n\n")

	sb.WriteString("When using tools:\n")
	sb.WriteString("- Use search_tickets to find existing tickets before creating duplicates\n")
	sb.WriteString("- Use list_tickets with parent_id to see all children of a feature or task\n")
	sb.WriteString("- Use list_tickets with type='feature' or type='bug' to list all features or bugs in the project\n")
	sb.WriteString("- Use get_ticket for full ticket detail including description, comments, and children\n")
	sb.WriteString("- When adding tasks to a feature, first search for the feature to get its ID, then create tasks with that parent_id\n")
	sb.WriteString("- Title is optional when creating tickets — if omitted, it's auto-generated from the description\n")
	sb.WriteString("- Use the current project ID when creating tickets or searching\n")
	sb.WriteString("- Use update_project_brief to write or update the project brief. Write well-structured markdown with headings, lists, and sections\n")
	sb.WriteString("- Use update_ticket to modify any ticket field: title, description, priority, status, dates\n")
	sb.WriteString("- Set date_start and date_end (YYYY-MM-DD) on tickets for timeline/Gantt planning\n")
	sb.WriteString("- Child ticket dates auto-expand the parent's date range, so set dates on children and parents will adjust automatically\n")
	sb.WriteString("- Confirm destructive actions (archive, delete) with the user before executing\n")
	sb.WriteString("- Report tool results clearly to the user\n")

	if auth.IsStaffOrAbove(user.Role) {
		sb.WriteString("\nStaff tools available to you:\n")
		sb.WriteString("- archive_ticket / restore_ticket: soft-delete and restore tickets\n")
		sb.WriteString("- delete_ticket: permanently delete a ticket (irreversible, always confirm first)\n")
		sb.WriteString("- update_agent_mode: assign tickets to AI agents (claude, gemini, codex, mistral) with mode (plan, implement)\n")
		sb.WriteString("- update_repo_url: set the project's repository URL\n")
		sb.WriteString("- mark_brief_reviewed: record that the brief has been reviewed\n")
	}

	return sb.String()
}

// recordAIUsage writes an ai_usage_entries row with raw token counts.
func (h *AssistantHandler) recordAIUsage(ctx context.Context, user *models.User, conv *models.AIConversation, usage *ai.UsageData) {
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

	costCents := h.cfg.CalculateAICost(usage.Model, usage.InputTokens, usage.OutputTokens, usage.HasImageOutput)
	if err := h.db.CreateAIUsageEntry(ctx, orgID, conv.ProjectID, &user.ID, usage.Model, "Chat message",
		int(usage.InputTokens), int(usage.OutputTokens), costCents); err != nil {
		log.Printf("recording ai usage: %v", err)
	}
}

// generateTitle creates a short title from the first user message.
func generateTitle(firstMessage string) string {
	title := firstMessage
	if len(title) > 60 {
		title = title[:57] + "..."
	}
	title = strings.ReplaceAll(title, "\n", " ")
	title = strings.TrimSpace(title)
	if title == "" {
		title = "New conversation"
	}
	return title
}

// ── Tool Executor ────────────────────────────────────────────

const (
	roleUser       = "user"
	roleSuperadmin = "superadmin"
)

type toolExecutor struct {
	db           *models.DB
	userID       string
	user         *models.User
	convID       string
	briefUpdated bool
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
	case "update_ticket":
		return e.updateTicket(ctx, args)
	case "post_comment":
		return e.postComment(ctx, args)
	case "update_project_brief":
		return e.updateProjectBrief(ctx, args)
	case "get_ticket":
		return e.getTicket(ctx, args)
	case "list_tickets":
		return e.listTickets(ctx, args)
	case "archive_ticket":
		return e.archiveTicket(ctx, args)
	case "restore_ticket":
		return e.restoreTicket(ctx, args)
	case "delete_ticket":
		return e.deleteTicket(ctx, args)
	case "update_agent_mode":
		return e.updateAgentMode(ctx, args)
	case "update_repo_url":
		return e.updateRepoURL(ctx, args)
	case "mark_brief_reviewed":
		return e.markBriefReviewed(ctx, args)
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

	if projectID == "" {
		conv, err := e.db.GetAIConversation(ctx, e.convID)
		if err == nil && conv.ProjectID != nil {
			projectID = *conv.ProjectID
		}
	}
	if projectID == "" {
		return map[string]any{"error": "no project context available"}, nil
	}

	if err := e.checkProjectAccess(ctx, projectID); err != nil {
		return map[string]any{"error": err.Error()}, nil //nolint:nilerr // tool result returns error in response map
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
		r := map[string]any{
			"id":         t.ID,
			"title":      t.Title,
			"type":       t.Type,
			"status":     t.Status,
			"priority":   t.Priority,
			"created_by": t.CreatedBy,
			"created_at": t.CreatedAt.Format("2006-01-02T15:04:05Z"),
			"updated_at": t.UpdatedAt.Format("2006-01-02T15:04:05Z"),
		}
		if t.DescriptionMarkdown != "" {
			desc := t.DescriptionMarkdown
			if len(desc) > 300 {
				desc = desc[:300] + "..."
			}
			r["description"] = desc
		}
		if t.DateStart != nil {
			r["date_start"] = t.DateStart.Format("2006-01-02")
		}
		if t.DateEnd != nil {
			r["date_end"] = t.DateEnd.Format("2006-01-02")
		}
		if t.ParentID != nil {
			r["parent_id"] = *t.ParentID
		}
		if t.AssignedTo != nil {
			r["assigned_to"] = *t.AssignedTo
		}
		results = append(results, r)
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
		return map[string]any{"error": err.Error()}, nil //nolint:nilerr // tool result returns error in response map
	}

	proj, err := e.db.GetProjectByID(ctx, projectID)
	if err != nil {
		return map[string]any{"error": "project not found"}, nil //nolint:nilerr // tool result returns error in response map
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
		return map[string]any{"error": err.Error()}, nil //nolint:nilerr // tool result returns error in response map
	}

	title, _ := args["title"].(string)
	ticketType, _ := args["type"].(string)
	priority, _ := args["priority"].(string)
	desc, _ := args["description"].(string)
	parentID, _ := args["parent_id"].(string)

	if ticketType == "" || priority == "" {
		return map[string]any{"error": "type and priority are required"}, nil
	}

	// Auto-generate title from description if not provided
	if title == "" && desc != "" {
		title = generateTitle(desc)
	}
	if title == "" {
		return map[string]any{"error": "title or description is required"}, nil
	}

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
	if ds, ok := args["date_start"].(string); ok && ds != "" {
		if t, err := time.Parse("2006-01-02", ds); err == nil {
			ticket.DateStart = &t
		}
	}
	if de, ok := args["date_end"].(string); ok && de != "" {
		if t, err := time.Parse("2006-01-02", de); err == nil {
			ticket.DateEnd = &t
		}
	}

	if err := e.db.CreateTicket(ctx, ticket); err != nil {
		return nil, fmt.Errorf("creating ticket: %w", err)
	}

	// Auto-expand parent dates to encompass this child
	if ticket.ParentID != nil && (ticket.DateStart != nil || ticket.DateEnd != nil) {
		if err := e.db.ExpandParentDates(ctx, ticket.DateStart, ticket.DateEnd, ticket.ParentID); err != nil {
			log.Printf("expanding parent dates: %v", err)
		}
	}

	result := map[string]any{
		"id":       ticket.ID,
		"title":    ticket.Title,
		"type":     ticket.Type,
		"status":   ticket.Status,
		"priority": ticket.Priority,
		"message":  fmt.Sprintf("Ticket '%s' created successfully", ticket.Title),
	}
	if ticket.DateStart != nil {
		result["date_start"] = ticket.DateStart.Format("2006-01-02")
	}
	if ticket.DateEnd != nil {
		result["date_end"] = ticket.DateEnd.Format("2006-01-02")
	}
	return result, nil
}

func (e *toolExecutor) updateTicketStatus(ctx context.Context, args map[string]any) (map[string]any, error) {
	ticketID, _ := args["ticket_id"].(string)
	newStatus, _ := args["new_status"].(string)

	if ticketID == "" || newStatus == "" {
		return map[string]any{"error": "ticket_id and new_status are required"}, nil
	}

	ticket, err := e.db.GetTicket(ctx, ticketID)
	if err != nil {
		return map[string]any{"error": "ticket not found"}, nil //nolint:nilerr // tool result returns error in response map
	}

	if err := e.checkProjectAccess(ctx, ticket.ProjectID); err != nil {
		return map[string]any{"error": err.Error()}, nil //nolint:nilerr // tool result returns error in response map
	}

	oldStatus := ticket.Status
	if err := e.db.UpdateTicketStatus(ctx, ticketID, newStatus); err != nil {
		return nil, fmt.Errorf("updating status: %w", err)
	}

	details, _ := json.Marshal(map[string]string{"new_status": newStatus}) //nolint:errchkjson // simple map marshal cannot fail
	_ = e.db.CreateActivity(ctx, ticketID, &e.userID, nil, "status_change", string(details))

	return map[string]any{
		"ticket_id":  ticketID,
		"old_status": oldStatus,
		"new_status": newStatus,
		"message":    fmt.Sprintf("Status updated from '%s' to '%s'", oldStatus, newStatus),
	}, nil
}

func (e *toolExecutor) updateTicket(ctx context.Context, args map[string]any) (map[string]any, error) {
	ticketID, _ := args["ticket_id"].(string)
	if ticketID == "" {
		return map[string]any{"error": "ticket_id is required"}, nil
	}

	ticket, err := e.db.GetTicket(ctx, ticketID)
	if err != nil {
		return map[string]any{"error": "ticket not found"}, nil //nolint:nilerr // tool result returns error in response map
	}

	if err := e.checkProjectAccess(ctx, ticket.ProjectID); err != nil {
		return map[string]any{"error": err.Error()}, nil //nolint:nilerr // tool result returns error in response map
	}

	var changes []string
	if title, ok := args["title"].(string); ok && title != "" {
		ticket.Title = title
		changes = append(changes, "title")
	}
	if desc, ok := args["description"].(string); ok {
		ticket.DescriptionMarkdown = desc
		changes = append(changes, "description")
	}
	if pri, ok := args["priority"].(string); ok && pri != "" {
		ticket.Priority = pri
		changes = append(changes, "priority")
	}
	if status, ok := args["status"].(string); ok && status != "" {
		ticket.Status = status
		changes = append(changes, "status")
	}
	if ds, ok := args["date_start"].(string); ok && ds != "" {
		if t, err := time.Parse("2006-01-02", ds); err == nil {
			ticket.DateStart = &t
			changes = append(changes, "date_start")
		}
	}
	if de, ok := args["date_end"].(string); ok && de != "" {
		if t, err := time.Parse("2006-01-02", de); err == nil {
			ticket.DateEnd = &t
			changes = append(changes, "date_end")
		}
	}

	if len(changes) == 0 {
		return map[string]any{"error": "no fields to update"}, nil
	}

	if err := e.db.UpdateTicket(ctx, ticket); err != nil {
		return nil, fmt.Errorf("updating ticket: %w", err)
	}

	// Log activity for status changes
	if slices.Contains(changes, "status") {
		details, _ := json.Marshal(map[string]string{"new_status": ticket.Status}) //nolint:errchkjson // simple map marshal cannot fail
		_ = e.db.CreateActivity(ctx, ticketID, &e.userID, nil, "status_change", string(details))
	}

	// Auto-expand parent dates if dates changed
	if ticket.ParentID != nil && (ticket.DateStart != nil || ticket.DateEnd != nil) {
		if err := e.db.ExpandParentDates(ctx, ticket.DateStart, ticket.DateEnd, ticket.ParentID); err != nil {
			log.Printf("expanding parent dates: %v", err)
		}
	}

	return map[string]any{
		"ticket_id": ticketID,
		"updated":   changes,
		"message":   fmt.Sprintf("Ticket updated: %s", strings.Join(changes, ", ")),
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
		return map[string]any{"error": "ticket not found"}, nil //nolint:nilerr // tool result returns error in response map
	}

	if errAccess := e.checkProjectAccess(ctx, ticket.ProjectID); errAccess != nil {
		return map[string]any{"error": errAccess.Error()}, nil //nolint:nilerr // tool result returns error in response map
	}

	comment, err := e.db.CreateComment(ctx, ticketID, &e.userID, nil, body)
	if err != nil {
		return nil, fmt.Errorf("creating comment: %w", err)
	}

	_ = e.db.CreateActivity(ctx, ticketID, &e.userID, nil, "comment", "{}")

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
		return map[string]any{"error": err.Error()}, nil //nolint:nilerr // tool result returns error in response map
	}

	briefMarkdown, _ := args["brief_markdown"].(string)
	if strings.TrimSpace(briefMarkdown) == "" {
		return map[string]any{"error": "brief_markdown is required"}, nil
	}

	// Save revision before overwriting (matches web UI behavior)
	proj, err := e.db.GetProjectByID(ctx, projectID)
	if err == nil {
		if revErr := e.db.CreateBriefRevision(ctx, projectID, e.userID, "edit", proj.BriefMarkdown); revErr != nil {
			log.Printf("saving brief revision: %v", revErr)
		}
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

func (e *toolExecutor) checkProjectAccess(ctx context.Context, projectID string) error {
	if e.user.Role == roleSuperadmin || e.user.Role == roleStaff {
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

// resolveProjectID gets the project ID from args or conversation context.
func (e *toolExecutor) resolveProjectID(ctx context.Context, args map[string]any) string {
	if pid, ok := args["project_id"].(string); ok && pid != "" {
		return pid
	}
	conv, err := e.db.GetAIConversation(ctx, e.convID)
	if err == nil && conv.ProjectID != nil {
		return *conv.ProjectID
	}
	return ""
}

// ticketToMap converts a Ticket to a response map.
func ticketToMap(t *models.Ticket) map[string]any {
	r := map[string]any{
		"id":         t.ID,
		"title":      t.Title,
		"type":       t.Type,
		"status":     t.Status,
		"priority":   t.Priority,
		"created_by": t.CreatedBy,
		"created_at": t.CreatedAt.Format("2006-01-02T15:04:05Z"),
		"updated_at": t.UpdatedAt.Format("2006-01-02T15:04:05Z"),
	}
	if t.DescriptionMarkdown != "" {
		desc := t.DescriptionMarkdown
		if len(desc) > 300 {
			desc = desc[:300] + "..."
		}
		r["description"] = desc
	}
	if t.DateStart != nil {
		r["date_start"] = t.DateStart.Format("2006-01-02")
	}
	if t.DateEnd != nil {
		r["date_end"] = t.DateEnd.Format("2006-01-02")
	}
	if t.ParentID != nil {
		r["parent_id"] = *t.ParentID
	}
	if t.AssignedTo != nil {
		r["assigned_to"] = *t.AssignedTo
	}
	return r
}

func (e *toolExecutor) getTicket(ctx context.Context, args map[string]any) (map[string]any, error) {
	ticketID, _ := args["ticket_id"].(string)
	if ticketID == "" {
		return map[string]any{"error": "ticket_id is required"}, nil
	}

	ticket, err := e.db.GetTicket(ctx, ticketID)
	if err != nil {
		return map[string]any{"error": "ticket not found"}, nil //nolint:nilerr // tool result returns error in response map
	}

	if err := e.checkProjectAccess(ctx, ticket.ProjectID); err != nil {
		return map[string]any{"error": err.Error()}, nil //nolint:nilerr // tool result returns error in response map
	}

	result := map[string]any{
		"id":         ticket.ID,
		"title":      ticket.Title,
		"type":       ticket.Type,
		"status":     ticket.Status,
		"priority":   ticket.Priority,
		"created_by": ticket.CreatedBy,
		"created_at": ticket.CreatedAt.Format("2006-01-02T15:04:05Z"),
		"updated_at": ticket.UpdatedAt.Format("2006-01-02T15:04:05Z"),
	}
	if ticket.DescriptionMarkdown != "" {
		result["description"] = ticket.DescriptionMarkdown
	}
	if ticket.DateStart != nil {
		result["date_start"] = ticket.DateStart.Format("2006-01-02")
	}
	if ticket.DateEnd != nil {
		result["date_end"] = ticket.DateEnd.Format("2006-01-02")
	}
	if ticket.ParentID != nil {
		result["parent_id"] = *ticket.ParentID
	}
	if ticket.AssignedTo != nil {
		result["assigned_to"] = *ticket.AssignedTo
	}

	// Load children
	children, _ := e.db.ListTicketsByParent(ctx, ticket.ID)
	if len(children) > 0 {
		var childList []map[string]any
		for _, c := range children {
			childList = append(childList, ticketToMap(&c))
		}
		result["children"] = childList
	}

	// Load comments
	comments, _ := e.db.ListComments(ctx, ticket.ID)
	if len(comments) > 0 {
		var commentList []map[string]any
		for _, c := range comments {
			cm := map[string]any{
				"id":         c.ID,
				"body":       c.BodyMarkdown,
				"created_at": c.CreatedAt.Format("2006-01-02T15:04:05Z"),
			}
			if c.UserID != nil {
				cm["user_id"] = *c.UserID
			}
			if c.AgentName != nil {
				cm["agent_name"] = *c.AgentName
			}
			commentList = append(commentList, cm)
		}
		result["comments"] = commentList
	}

	return result, nil
}

func (e *toolExecutor) listTickets(ctx context.Context, args map[string]any) (map[string]any, error) {
	parentID, _ := args["parent_id"].(string)

	// If parent_id is specified, list children of that ticket
	if parentID != "" {
		parent, err := e.db.GetTicket(ctx, parentID)
		if err != nil {
			return map[string]any{"error": "parent ticket not found"}, nil //nolint:nilerr // tool result returns error in response map
		}
		if errAccess := e.checkProjectAccess(ctx, parent.ProjectID); errAccess != nil {
			return map[string]any{"error": errAccess.Error()}, nil //nolint:nilerr // tool result returns error in response map
		}
		children, err := e.db.ListTicketsByParent(ctx, parentID)
		if err != nil {
			return nil, fmt.Errorf("listing children: %w", err)
		}
		var results []map[string]any
		for _, t := range children {
			results = append(results, ticketToMap(&t))
		}
		return map[string]any{
			"parent_id": parentID,
			"tickets":   results,
			"count":     len(results),
		}, nil
	}

	// Otherwise list by project + type
	projectID := e.resolveProjectID(ctx, args)
	if projectID == "" {
		return map[string]any{"error": "project_id or parent_id is required"}, nil
	}
	if err := e.checkProjectAccess(ctx, projectID); err != nil {
		return map[string]any{"error": err.Error()}, nil //nolint:nilerr // tool result returns error in response map
	}

	ticketType, _ := args["type"].(string)

	var tickets []models.Ticket
	var err error
	switch ticketType {
	case "feature":
		tickets, err = e.db.ListFeatures(ctx, projectID)
	case "bug":
		tickets, err = e.db.ListBugs(ctx, projectID)
	default:
		if ticketType == "" {
			ticketType = "feature"
		}
		tickets, err = e.db.ListTickets(ctx, projectID, ticketType)
	}
	if err != nil {
		return nil, fmt.Errorf("listing tickets: %w", err)
	}

	var results []map[string]any
	for _, t := range tickets {
		results = append(results, ticketToMap(&t))
	}
	return map[string]any{
		"type":    ticketType,
		"tickets": results,
		"count":   len(results),
	}, nil
}

func (e *toolExecutor) archiveTicket(ctx context.Context, args map[string]any) (map[string]any, error) {
	if !auth.IsStaffOrAbove(e.user.Role) {
		return map[string]any{"error": "forbidden: staff or superadmin role required"}, nil
	}

	ticketID, _ := args["ticket_id"].(string)
	if ticketID == "" {
		return map[string]any{"error": "ticket_id is required"}, nil
	}

	ticket, err := e.db.GetTicket(ctx, ticketID)
	if err != nil {
		return map[string]any{"error": "ticket not found"}, nil //nolint:nilerr // tool result returns error in response map
	}

	if err := e.db.ArchiveTicket(ctx, ticketID); err != nil {
		return nil, fmt.Errorf("archiving ticket: %w", err)
	}

	log.Printf("Ticket %s archived by user %s (%s) via AI assistant", ticketID, e.userID, e.user.Email)

	return map[string]any{
		"ticket_id": ticketID,
		"title":     ticket.Title,
		"message":   fmt.Sprintf("Ticket '%s' archived successfully", ticket.Title),
	}, nil
}

func (e *toolExecutor) restoreTicket(ctx context.Context, args map[string]any) (map[string]any, error) {
	if !auth.IsStaffOrAbove(e.user.Role) {
		return map[string]any{"error": "forbidden: staff or superadmin role required"}, nil
	}

	ticketID, _ := args["ticket_id"].(string)
	if ticketID == "" {
		return map[string]any{"error": "ticket_id is required"}, nil
	}

	if err := e.db.RestoreTicket(ctx, ticketID); err != nil {
		return nil, fmt.Errorf("restoring ticket: %w", err)
	}

	log.Printf("Ticket %s restored by user %s (%s) via AI assistant", ticketID, e.userID, e.user.Email)

	return map[string]any{
		"ticket_id": ticketID,
		"message":   "Ticket restored successfully",
	}, nil
}

func (e *toolExecutor) deleteTicket(ctx context.Context, args map[string]any) (map[string]any, error) {
	if !auth.IsStaffOrAbove(e.user.Role) {
		return map[string]any{"error": "forbidden: staff or superadmin role required"}, nil
	}

	ticketID, _ := args["ticket_id"].(string)
	if ticketID == "" {
		return map[string]any{"error": "ticket_id is required"}, nil
	}

	ticket, err := e.db.GetTicket(ctx, ticketID)
	if err != nil {
		return map[string]any{"error": "ticket not found"}, nil //nolint:nilerr // tool result returns error in response map
	}

	log.Printf("Ticket %s (%s) permanently deleted by user %s (%s) via AI assistant", ticketID, ticket.Title, e.userID, e.user.Email)

	if err := e.db.DeleteTicket(ctx, ticketID); err != nil {
		return nil, fmt.Errorf("deleting ticket: %w", err)
	}

	return map[string]any{
		"ticket_id": ticketID,
		"title":     ticket.Title,
		"message":   fmt.Sprintf("Ticket '%s' permanently deleted", ticket.Title),
	}, nil
}

func (e *toolExecutor) updateAgentMode(ctx context.Context, args map[string]any) (map[string]any, error) {
	if !auth.IsStaffOrAbove(e.user.Role) {
		return map[string]any{"error": "forbidden: staff or superadmin role required"}, nil
	}

	ticketID, _ := args["ticket_id"].(string)
	if ticketID == "" {
		return map[string]any{"error": "ticket_id is required"}, nil
	}

	mode, _ := args["agent_mode"].(string)
	agent, _ := args["agent_name"].(string)

	var modePtr, agentPtr *string
	if mode != "" && mode != "none" {
		modePtr = &mode
	}
	if agent != "" && agent != "none" {
		agentPtr = &agent
	}

	if err := e.db.UpdateTicketAgentMode(ctx, ticketID, modePtr, agentPtr); err != nil {
		return nil, fmt.Errorf("updating agent mode: %w", err)
	}

	result := map[string]any{
		"ticket_id": ticketID,
		"message":   "Agent mode updated",
	}
	if mode != "" {
		result["agent_mode"] = mode
	}
	if agent != "" {
		result["agent_name"] = agent
	}
	return result, nil
}

func (e *toolExecutor) updateRepoURL(ctx context.Context, args map[string]any) (map[string]any, error) {
	if !auth.IsStaffOrAbove(e.user.Role) {
		return map[string]any{"error": "forbidden: staff or superadmin role required"}, nil
	}

	projectID := e.resolveProjectID(ctx, args)
	if projectID == "" {
		return map[string]any{"error": "project_id is required"}, nil
	}

	repoURL, _ := args["repo_url"].(string)

	if err := e.db.UpdateProjectRepoURL(ctx, projectID, strings.TrimSpace(repoURL)); err != nil {
		return nil, fmt.Errorf("updating repo url: %w", err)
	}

	return map[string]any{
		"project_id": projectID,
		"repo_url":   repoURL,
		"message":    "Repository URL updated",
	}, nil
}

func (e *toolExecutor) markBriefReviewed(ctx context.Context, args map[string]any) (map[string]any, error) {
	if !auth.IsStaffOrAbove(e.user.Role) {
		return map[string]any{"error": "forbidden: staff or superadmin role required"}, nil
	}

	projectID := e.resolveProjectID(ctx, args)
	if projectID == "" {
		return map[string]any{"error": "project_id is required"}, nil
	}

	if err := e.checkProjectAccess(ctx, projectID); err != nil {
		return map[string]any{"error": err.Error()}, nil //nolint:nilerr // tool result returns error in response map
	}

	if err := e.db.CreateBriefRevision(ctx, projectID, e.userID, "reviewed", ""); err != nil {
		return nil, fmt.Errorf("marking brief reviewed: %w", err)
	}

	return map[string]any{
		"project_id": projectID,
		"message":    "Brief marked as reviewed",
	}, nil
}

// sanitizeForPrompt strips control characters and limits length for safe AI prompt injection.
func sanitizeForPrompt(s string, maxLen int) string {
	// Strip control characters (except common whitespace)
	var b strings.Builder
	for _, r := range s {
		if r == '\n' || r == '\t' || r >= 32 {
			b.WriteRune(r)
		}
	}
	result := b.String()
	if len(result) > maxLen {
		result = result[:maxLen]
	}
	return result
}
