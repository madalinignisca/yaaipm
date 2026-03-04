package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/gorilla/websocket"
	"google.golang.org/genai"

	"github.com/madalin/forgedesk/internal/ai"
	"github.com/madalin/forgedesk/internal/config"
	"github.com/madalin/forgedesk/internal/middleware"
	"github.com/madalin/forgedesk/internal/models"
	"github.com/madalin/forgedesk/internal/render"
	"github.com/madalin/forgedesk/internal/ws"
)

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true },
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

	client := ws.NewClient(h.hub, conn, projectID, user.ID, user.Name, user, func(c *ws.Client, data []byte) {
		h.handleClientMessage(c, data)
	})

	h.hub.Register(client)

	// Send conversation info and history
	h.sendInitialState(client, projectID)

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
	convInfo, _ := json.Marshal(wsMessage{
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

	historyMsg, _ := json.Marshal(wsMessage{
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

	switch msg.Type {
	case "send_message":
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
	msg, err := h.db.CreateAIMessageWithUser(ctx, conv.ID, "user", content, &client.UserID, client.UserName)
	if err != nil {
		log.Printf("ws: save message error: %v", err)
		h.sendError(client, "Failed to save message")
		return
	}

	// Touch conversation
	h.db.TouchAIConversation(ctx, conv.ID)

	// Broadcast user message to all clients in the room
	userMsg, _ := json.Marshal(wsMessage{
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
	typing, _ := json.Marshal(wsMessage{Type: "ai_typing"})
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
		role := "user"
		if m.Role == "assistant" {
			role = "model"
		}
		messageContent := m.Content
		if m.Role == "user" && m.UserName != "" {
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
		h.db.CreateAIMessage(ctx, conv.ID, "assistant", fullResponse.String())
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
				if m.Role == "user" {
					title := generateTitle(m.Content)
					h.db.UpdateAIConversationTitle(ctx, conv.ID, title)
					break
				}
			}
		}
	}

	// Broadcast done
	done, _ := json.Marshal(wsMessage{
		Type: "ai_done",
		Data: mustJSON(map[string]bool{"reload": executor.briefUpdated}),
	})
	h.hub.BroadcastAll(client.ProjectID, done)
}

// ── Helper Methods ───────────────────────────────────────────

func (h *AssistantHandler) sendError(client *ws.Client, msg string) {
	data, _ := json.Marshal(wsMessage{
		Type: "ai_error",
		Data: mustJSON(map[string]string{"error": msg}),
	})
	client.Send(data)
}

func (h *AssistantHandler) broadcastAIError(projectID, msg string) {
	data, _ := json.Marshal(wsMessage{
		Type: "ai_error",
		Data: mustJSON(map[string]string{"error": msg}),
	})
	h.hub.BroadcastAll(projectID, data)
}

func mustJSON(v any) json.RawMessage {
	data, _ := json.Marshal(v)
	return data
}

// checkProjectAccess verifies the user has access to a project.
func (h *AssistantHandler) checkProjectAccess(ctx context.Context, user *models.User, projectID string) error {
	if user.Role == "superadmin" || user.Role == "staff" {
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
func (h *AssistantHandler) buildSystemPrompt(ctx context.Context, user *models.User, conv *models.AIConversation) string {
	var sb strings.Builder
	sb.WriteString("You are ForgeDesk Assistant, an AI helper for project management. ")
	sb.WriteString("You help users manage their projects, tickets, and tasks. ")
	sb.WriteString("Be concise, helpful, and professional. Use markdown formatting in your responses.\n\n")

	sb.WriteString("This is a shared project chat. Multiple team members may be participating. ")
	sb.WriteString("User messages are prefixed with [Name] to identify who is speaking. ")
	sb.WriteString("Address users by name when appropriate.\n\n")

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
	sb.WriteString("- Features are top-level items (no parent). The Features tab shows features.\n")
	sb.WriteString("- Tasks are work items that belong UNDER a feature (parent_id = feature ID).\n")
	sb.WriteString("- Subtasks belong under a task (parent_id = task ID).\n")
	sb.WriteString("- Bugs are top-level (no parent). The Bugs tab shows bugs.\n")
	sb.WriteString("- When asked to 'add tasks to a feature', create tickets with type='task' and parent_id set to the feature's ID.\n")
	sb.WriteString("- NEVER create a feature when the user asks for a task — use type='task' with a parent_id instead.\n\n")

	sb.WriteString("When using tools:\n")
	sb.WriteString("- Use search_tickets to find existing tickets before creating duplicates\n")
	sb.WriteString("- When adding tasks to a feature, first search for the feature to get its ID, then create tasks with that parent_id\n")
	sb.WriteString("- Use the current project ID when creating tickets or searching\n")
	sb.WriteString("- Use update_project_brief to write or update the project brief when the user asks you to\n")
	sb.WriteString("- When updating the brief, write well-structured markdown with headings, lists, and sections\n")
	sb.WriteString("- Confirm destructive actions with the user before executing\n")
	sb.WriteString("- Report tool results clearly to the user\n")

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

func (e *toolExecutor) checkProjectAccess(ctx context.Context, projectID string) error {
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
