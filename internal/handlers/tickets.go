package handlers

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/madalin/forgedesk/internal/ai"
	"github.com/madalin/forgedesk/internal/auth"
	"github.com/madalin/forgedesk/internal/config"
	"github.com/madalin/forgedesk/internal/middleware"
	"github.com/madalin/forgedesk/internal/models"
	"github.com/madalin/forgedesk/internal/render"
)

const typeBug = "bug"

type TicketHandler struct {
	db       *models.DB
	engine   *render.Engine
	titleGen titleGenerator
	cfg      *config.Config
}

// titleGenerator generates a ticket title from a description.
type titleGenerator interface {
	GenerateTitle(ctx context.Context, ticketType, description string) (string, *ai.UsageData, error)
}

func NewTicketHandler(db *models.DB, engine *render.Engine, tg titleGenerator, cfg *config.Config) *TicketHandler {
	return &TicketHandler{db: db, engine: engine, titleGen: tg, cfg: cfg}
}

// Valid ticket field values for input validation
var (
	validTicketTypes      = map[string]bool{"epic": true, "feature": true, "task": true, "subtask": true, "bug": true}
	validTicketPriorities = map[string]bool{"low": true, "medium": true, "high": true, "critical": true}
	validTicketStatuses   = map[string]bool{
		"backlog": true, "ready": true, "planning": true, "plan_review": true,
		"implementing": true, "testing": true, "review": true, "done": true, "canceled": true,
	}
)

type ticketDetailData struct {
	Ticket           *models.Ticket
	CommentReactions map[string][]models.ReactionGroup
	Children         []models.Ticket
	Comments         []models.Comment
	Attachments      []models.TicketAttachment
	TicketReactions  []models.ReactionGroup
	IsStaff          bool
}

func (h *TicketHandler) TicketDetail(w http.ResponseWriter, r *http.Request) {
	user := middleware.GetUser(r)
	ticketID := r.PathValue("ticketID")

	var ticket *models.Ticket
	var err error

	// For client users, enforce org-scoped access
	if auth.IsStaffOrAbove(user.Role) {
		ticket, err = h.db.GetTicket(r.Context(), ticketID)
	} else {
		org := middleware.GetOrg(r)
		if org == nil {
			h.engine.RenderError(w, http.StatusNotFound, "Ticket not found")
			return
		}
		ticket, err = h.db.GetTicketScoped(r.Context(), ticketID, org.ID)
	}
	if err != nil {
		h.engine.RenderError(w, http.StatusNotFound, "Ticket not found")
		return
	}

	children, _ := h.db.ListTicketsByParent(r.Context(), ticket.ID)
	comments, _ := h.db.ListComments(r.Context(), ticket.ID)
	attachments, _ := h.db.ListAttachmentsByTicket(r.Context(), ticket.ID)

	// Load reactions for ticket
	ticketReactions, _ := h.db.ListReactionGroups(r.Context(), "ticket", ticket.ID, user.ID)

	// Load reactions for all comments in one batch query
	var commentIDs []string
	for _, c := range comments {
		commentIDs = append(commentIDs, c.ID)
	}
	commentReactions := make(map[string][]models.ReactionGroup)
	if len(commentIDs) > 0 {
		commentReactions, _ = h.db.ListReactionGroupsBatch(r.Context(), "comment", commentIDs, user.ID)
		if commentReactions == nil {
			commentReactions = make(map[string][]models.ReactionGroup)
		}
	}

	proj, _ := h.db.GetProjectByID(r.Context(), ticket.ProjectID)

	org := middleware.GetOrg(r)
	if org == nil && proj != nil {
		org, _ = h.db.GetOrgByID(r.Context(), proj.OrgID)
	}

	_ = h.engine.Render(w, r, "ticket_detail.html", render.PageData{
		Title: ticket.Title, User: user, Org: org, Orgs: middleware.GetOrgs(r), CurrentPath: r.URL.Path,
		Projects: middleware.GetProjects(r), ActiveProject: proj,
		ProjectID: ticket.ProjectID,
		Data: ticketDetailData{
			Ticket:           ticket,
			Children:         children,
			Comments:         comments,
			Attachments:      attachments,
			IsStaff:          auth.IsStaffOrAbove(user.Role),
			TicketReactions:  ticketReactions,
			CommentReactions: commentReactions,
		},
	})
}

func (h *TicketHandler) CreateTicket(w http.ResponseWriter, r *http.Request) {
	user := middleware.GetUser(r)
	projectID := r.FormValue("project_id")
	if projectID == "" {
		http.Error(w, "project_id required", http.StatusBadRequest)
		return
	}

	// Cross-tenant guard: client users may only create tickets in projects
	// under an org they belong to. Staff have global access. DB failures
	// surface as 500 so incidents are not masked as 404. (#25)
	if err := authorizeProjectAccess(r.Context(), h.db, user, projectID); err != nil {
		respondAuthzError(w, err, "Project not found")
		return
	}

	title := strings.TrimSpace(r.FormValue("title"))
	description := r.FormValue("description")

	// Auto-generate title from description if not provided
	if title == "" && description != "" && h.titleGen != nil {
		generated, usage, genErr := h.titleGen.GenerateTitle(r.Context(), r.FormValue("type"), description)
		if genErr != nil {
			log.Printf("generating title: %v", genErr)
		} else {
			title = generated
		}
		if usage != nil && (usage.InputTokens > 0 || usage.OutputTokens > 0) {
			h.recordAIUsage(r.Context(), user, projectID, usage, "Auto-generated title")
		}
	}
	if title == "" {
		http.Error(w, "Title or description is required", http.StatusBadRequest)
		return
	}

	parentID := r.FormValue("parent_id")
	var parentPtr *string
	if parentID != "" {
		// Enforce same-project parentage at the handler level for a
		// friendlier error than the FK violation we'd otherwise surface.
		parent, parentErr := h.db.GetTicket(r.Context(), parentID)
		if parentErr != nil {
			http.Error(w, "Parent ticket not found", http.StatusBadRequest)
			return
		}
		if parent.ProjectID != projectID {
			http.Error(w, "Parent ticket must belong to the same project", http.StatusBadRequest)
			return
		}
		parentPtr = &parentID
	}

	ticketType := r.FormValue("type")
	if ticketType == "" {
		ticketType = "task"
	}
	if !validTicketTypes[ticketType] {
		http.Error(w, "Invalid ticket type", http.StatusBadRequest)
		return
	}

	var dateStart, dateEnd *time.Time
	if ds := r.FormValue("date_start"); ds != "" {
		if t, parseErr := time.Parse("2006-01-02", ds); parseErr == nil {
			dateStart = &t
		}
	}
	if de := r.FormValue("date_end"); de != "" {
		if t, parseErr := time.Parse("2006-01-02", de); parseErr == nil {
			dateEnd = &t
		}
	}

	ticket := &models.Ticket{
		ProjectID:           projectID,
		ParentID:            parentPtr,
		Type:                ticketType,
		Title:               title,
		DescriptionMarkdown: description,
		Status:              "backlog",
		Priority:            r.FormValue("priority"),
		DateStart:           dateStart,
		DateEnd:             dateEnd,
		CreatedBy:           user.ID,
	}

	if ticket.Priority == "" {
		ticket.Priority = "medium"
	}
	if !validTicketPriorities[ticket.Priority] {
		http.Error(w, "Invalid priority", http.StatusBadRequest)
		return
	}

	if err := h.db.CreateTicket(r.Context(), ticket); err != nil {
		log.Printf("creating ticket: %v", err)
		http.Error(w, "Failed to create ticket", http.StatusInternalServerError)
		return
	}

	// Auto-expand parent dates to encompass this child
	if ticket.ParentID != nil && (ticket.DateStart != nil || ticket.DateEnd != nil) {
		if err := h.db.ExpandParentDates(r.Context(), ticket.DateStart, ticket.DateEnd, ticket.ParentID); err != nil {
			log.Printf("expanding parent dates: %v", err)
		}
	}

	// Redirect back (only to same host to prevent open redirect)
	if referer := r.Header.Get("Referer"); referer != "" {
		if u, parseErr := url.Parse(referer); parseErr == nil && (u.Host == "" || u.Host == r.Host) {
			http.Redirect(w, r, referer, http.StatusSeeOther)
			return
		}
	}
	w.Header().Set("Hx-Refresh", "true")
	w.WriteHeader(http.StatusCreated)
}

func (h *TicketHandler) UpdateTicket(w http.ResponseWriter, r *http.Request) {
	user := middleware.GetUser(r)
	ticketID := r.PathValue("ticketID")

	// Cross-tenant guard: clients may only mutate tickets in orgs they
	// belong to. 404 (not 403) to avoid leaking existence; real DB
	// errors surface as 500 so operational incidents are not masked. (#25)
	ticket, err := authorizeTicketAccess(r.Context(), h.db, user, ticketID)
	if err != nil {
		respondAuthzError(w, err, "Ticket not found")
		return
	}

	if title := strings.TrimSpace(r.FormValue("title")); title != "" {
		ticket.Title = title
	}
	if desc := r.FormValue("description"); r.Form.Has("description") {
		ticket.DescriptionMarkdown = desc
	}
	if pri := r.FormValue("priority"); pri != "" {
		ticket.Priority = pri
	}
	if ds := r.FormValue("date_start"); ds != "" {
		if t, parseErr := time.Parse("2006-01-02", ds); parseErr == nil {
			ticket.DateStart = &t
		}
	}
	if de := r.FormValue("date_end"); de != "" {
		if t, parseErr := time.Parse("2006-01-02", de); parseErr == nil {
			ticket.DateEnd = &t
		}
	}

	if err := h.db.UpdateTicket(r.Context(), ticket); err != nil {
		log.Printf("updating ticket: %v", err)
		http.Error(w, "Failed to update ticket", http.StatusInternalServerError)
		return
	}

	// Auto-expand parent dates if this ticket's dates changed
	if ticket.ParentID != nil && (ticket.DateStart != nil || ticket.DateEnd != nil) {
		if err := h.db.ExpandParentDates(r.Context(), ticket.DateStart, ticket.DateEnd, ticket.ParentID); err != nil {
			log.Printf("expanding parent dates: %v", err)
		}
	}

	w.Header().Set("Hx-Refresh", "true")
	w.WriteHeader(http.StatusOK)
}

func (h *TicketHandler) UpdateStatus(w http.ResponseWriter, r *http.Request) {
	user := middleware.GetUser(r)
	ticketID := r.PathValue("ticketID")
	newStatus := r.FormValue("status")

	if newStatus == "" {
		http.Error(w, "Status is required", http.StatusBadRequest)
		return
	}
	if !validTicketStatuses[newStatus] {
		http.Error(w, "Invalid status", http.StatusBadRequest)
		return
	}

	// Cross-tenant guard: clients may only transition tickets in their
	// own orgs. Uses the lite (single-join) variant since we don't
	// need the ticket body here. (#25 — not explicitly listed in the
	// issue but shares the same root cause as UpdateTicket.)
	if err := authorizeTicketOrgAccess(r.Context(), h.db, user, ticketID); err != nil {
		respondAuthzError(w, err, "Ticket not found")
		return
	}

	if err := h.db.UpdateTicketStatus(r.Context(), ticketID, newStatus); err != nil {
		log.Printf("updating status: %v", err)
		http.Error(w, "Failed to update status", http.StatusInternalServerError)
		return
	}

	details, _ := json.Marshal(map[string]string{"new_status": newStatus}) //nolint:errchkjson // simple map marshal
	_ = h.db.CreateActivity(r.Context(), ticketID, &user.ID, nil, "status_change", string(details))

	w.Header().Set("Hx-Refresh", "true")
	w.WriteHeader(http.StatusOK)
}

func (h *TicketHandler) ArchiveTicket(w http.ResponseWriter, r *http.Request) {
	user := middleware.GetUser(r)
	if !auth.IsStaffOrAbove(user.Role) {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}

	ticketID := r.PathValue("ticketID")
	ticket, err := h.db.GetTicket(r.Context(), ticketID)
	if err != nil {
		http.Error(w, "Ticket not found", http.StatusNotFound)
		return
	}

	if err := h.db.ArchiveTicket(r.Context(), ticketID); err != nil {
		log.Printf("archiving ticket: %v", err)
		http.Error(w, "Failed to archive", http.StatusInternalServerError)
		return
	}

	log.Printf("Ticket %s archived by user %s (%s)", ticketID, user.ID, user.Email)

	// Redirect back to the project page
	proj, _ := h.db.GetProjectByID(r.Context(), ticket.ProjectID)
	if proj != nil {
		org, _ := h.db.GetOrgByID(r.Context(), proj.OrgID)
		if org != nil {
			tab := "features"
			if ticket.Type == typeBug {
				tab = "bugs"
			}
			w.Header().Set("Hx-Redirect", "/orgs/"+org.Slug+"/projects/"+proj.Slug+"/"+tab)
			w.WriteHeader(http.StatusOK)
			return
		}
	}
	w.Header().Set("Hx-Refresh", "true")
	w.WriteHeader(http.StatusOK)
}

func (h *TicketHandler) RestoreTicket(w http.ResponseWriter, r *http.Request) {
	user := middleware.GetUser(r)
	if !auth.IsStaffOrAbove(user.Role) {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}

	ticketID := r.PathValue("ticketID")
	if err := h.db.RestoreTicket(r.Context(), ticketID); err != nil {
		log.Printf("restoring ticket: %v", err)
		http.Error(w, "Failed to restore", http.StatusInternalServerError)
		return
	}

	log.Printf("Ticket %s restored by user %s (%s)", ticketID, user.ID, user.Email)
	w.Header().Set("Hx-Refresh", "true")
	w.WriteHeader(http.StatusOK)
}

func (h *TicketHandler) DeleteTicket(w http.ResponseWriter, r *http.Request) {
	user := middleware.GetUser(r)
	if !auth.IsStaffOrAbove(user.Role) {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}

	ticketID := r.PathValue("ticketID")
	ticket, err := h.db.GetTicket(r.Context(), ticketID)
	if err != nil {
		http.Error(w, "Ticket not found", http.StatusNotFound)
		return
	}

	log.Printf("Ticket %s (%s) permanently deleted by user %s (%s)", ticketID, ticket.Title, user.ID, user.Email)

	if err := h.db.DeleteTicketTx(r.Context(), ticketID); err != nil {
		log.Printf("deleting ticket: %v", err)
		http.Error(w, "Failed to delete", http.StatusInternalServerError)
		return
	}

	// Redirect back to the project page
	proj, _ := h.db.GetProjectByID(r.Context(), ticket.ProjectID)
	if proj != nil {
		org, _ := h.db.GetOrgByID(r.Context(), proj.OrgID)
		if org != nil {
			tab := "features"
			if ticket.Type == typeBug {
				tab = "bugs"
			}
			w.Header().Set("Hx-Redirect", "/orgs/"+org.Slug+"/projects/"+proj.Slug+"/"+tab)
			w.WriteHeader(http.StatusOK)
			return
		}
	}
	w.Header().Set("Hx-Refresh", "true")
	w.WriteHeader(http.StatusOK)
}

func (h *TicketHandler) UpdateAgentMode(w http.ResponseWriter, r *http.Request) {
	user := middleware.GetUser(r)
	if !auth.IsStaffOrAbove(user.Role) {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}

	ticketID := r.PathValue("ticketID")
	mode := r.FormValue("agent_mode")
	agent := r.FormValue("agent_name")

	var modePtr, agentPtr *string
	if mode != "" {
		modePtr = &mode
	}
	if agent != "" {
		agentPtr = &agent
	}

	if err := h.db.UpdateTicketAgentMode(r.Context(), ticketID, modePtr, agentPtr); err != nil {
		log.Printf("updating agent mode: %v", err)
		http.Error(w, "Failed to update", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Hx-Refresh", "true")
	w.WriteHeader(http.StatusOK)
}

// recordAIUsage records an AI usage entry for a project-scoped operation.
func (h *TicketHandler) recordAIUsage(ctx context.Context, user *models.User, projectID string, usage *ai.UsageData, label string) {
	proj, err := h.db.GetProjectByID(ctx, projectID)
	if err != nil {
		log.Printf("ai usage: project %s not found: %v", projectID, err)
		return
	}
	costCents := h.cfg.CalculateAICost(usage.Model, usage.InputTokens, usage.OutputTokens, usage.HasImageOutput)
	if err := h.db.CreateAIUsageEntry(ctx, proj.OrgID, &projectID, &user.ID, usage.Model, label,
		int(usage.InputTokens), int(usage.OutputTokens), costCents); err != nil {
		log.Printf("recording ai usage: %v", err)
	}
}
