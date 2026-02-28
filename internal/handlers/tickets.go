package handlers

import (
	"encoding/json"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/madalin/forgedesk/internal/auth"
	"github.com/madalin/forgedesk/internal/middleware"
	"github.com/madalin/forgedesk/internal/models"
	"github.com/madalin/forgedesk/internal/render"
)

type TicketHandler struct {
	db     *models.DB
	engine *render.Engine
}

func NewTicketHandler(db *models.DB, engine *render.Engine) *TicketHandler {
	return &TicketHandler{db: db, engine: engine}
}

type ticketDetailData struct {
	Ticket   *models.Ticket
	Children []models.Ticket
	Comments []models.Comment
	IsStaff  bool
}

func (h *TicketHandler) TicketDetail(w http.ResponseWriter, r *http.Request) {
	user := middleware.GetUser(r)
	ticketID := r.PathValue("ticketID")

	ticket, err := h.db.GetTicket(r.Context(), ticketID)
	if err != nil {
		h.engine.RenderError(w, http.StatusNotFound, "Ticket not found")
		return
	}

	children, _ := h.db.ListTicketsByParent(r.Context(), ticket.ID)
	comments, _ := h.db.ListComments(r.Context(), ticket.ID)

	proj, _ := h.db.GetProjectByID(r.Context(), ticket.ProjectID)

	var org *models.Organization
	var orgs []models.Organization
	if proj != nil {
		org, _ = h.db.GetOrgByID(r.Context(), proj.OrgID)
		if auth.IsStaffOrAbove(user.Role) {
			orgs, _ = h.db.ListAllOrgs(r.Context())
		} else {
			orgs, _ = h.db.ListUserOrgs(r.Context(), user.ID)
		}
	}

	h.engine.Render(w, "ticket_detail.html", render.PageData{
		Title: ticket.Title, User: user, Org: org, Orgs: orgs, CurrentPath: r.URL.Path,
		ProjectID: ticket.ProjectID,
		Data: ticketDetailData{
			Ticket:   ticket,
			Children: children,
			Comments: comments,
			IsStaff:  auth.IsStaffOrAbove(user.Role),
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

	title := strings.TrimSpace(r.FormValue("title"))
	if title == "" {
		http.Error(w, "Title is required", http.StatusBadRequest)
		return
	}

	parentID := r.FormValue("parent_id")
	var parentPtr *string
	if parentID != "" {
		parentPtr = &parentID
	}

	ticketType := r.FormValue("type")
	if ticketType == "" {
		ticketType = "task"
	}

	var dateStart, dateEnd *time.Time
	if ds := r.FormValue("date_start"); ds != "" {
		if t, err := time.Parse("2006-01-02", ds); err == nil {
			dateStart = &t
		}
	}
	if de := r.FormValue("date_end"); de != "" {
		if t, err := time.Parse("2006-01-02", de); err == nil {
			dateEnd = &t
		}
	}

	ticket := &models.Ticket{
		ProjectID:           projectID,
		ParentID:            parentPtr,
		Type:                ticketType,
		Title:               title,
		DescriptionMarkdown: r.FormValue("description"),
		Status:              "backlog",
		Priority:            r.FormValue("priority"),
		DateStart:           dateStart,
		DateEnd:             dateEnd,
		CreatedBy:           user.ID,
	}

	if ticket.Priority == "" {
		ticket.Priority = "medium"
	}

	if err := h.db.CreateTicket(r.Context(), ticket); err != nil {
		log.Printf("creating ticket: %v", err)
		http.Error(w, "Failed to create ticket", http.StatusInternalServerError)
		return
	}

	// Redirect back
	referer := r.Header.Get("Referer")
	if referer != "" {
		http.Redirect(w, r, referer, http.StatusSeeOther)
		return
	}
	w.Header().Set("HX-Refresh", "true")
	w.WriteHeader(http.StatusCreated)
}

func (h *TicketHandler) UpdateStatus(w http.ResponseWriter, r *http.Request) {
	user := middleware.GetUser(r)
	ticketID := r.PathValue("ticketID")
	newStatus := r.FormValue("status")

	if newStatus == "" {
		http.Error(w, "Status is required", http.StatusBadRequest)
		return
	}

	if err := h.db.UpdateTicketStatus(r.Context(), ticketID, newStatus); err != nil {
		log.Printf("updating status: %v", err)
		http.Error(w, "Failed to update status", http.StatusInternalServerError)
		return
	}

	details, _ := json.Marshal(map[string]string{"new_status": newStatus})
	h.db.CreateActivity(r.Context(), ticketID, &user.ID, nil, "status_change", string(details))

	w.Header().Set("HX-Refresh", "true")
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

	w.Header().Set("HX-Refresh", "true")
	w.WriteHeader(http.StatusOK)
}
