package handlers

import (
	"net/http"

	"github.com/madalin/forgedesk/internal/auth"
	"github.com/madalin/forgedesk/internal/middleware"
	"github.com/madalin/forgedesk/internal/models"
	"github.com/madalin/forgedesk/internal/render"
)

type DashboardHandler struct {
	db     *models.DB
	engine *render.Engine
}

func NewDashboardHandler(db *models.DB, engine *render.Engine) *DashboardHandler {
	return &DashboardHandler{db: db, engine: engine}
}

func (h *DashboardHandler) Dashboard(w http.ResponseWriter, r *http.Request) {
	user := middleware.GetUser(r)
	if user == nil {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}

	var orgs []models.Organization
	var err error

	if auth.IsStaffOrAbove(user.Role) {
		orgs, err = h.db.ListAllOrgs(r.Context())
	} else {
		orgs, err = h.db.ListUserOrgs(r.Context(), user.ID)
	}
	if err != nil {
		h.engine.RenderError(w, http.StatusInternalServerError, "Failed to load organizations")
		return
	}

	pendingInvites, _ := h.db.ListPendingInvitationsForUser(r.Context(), user.Email)

	// Only auto-redirect if one org AND no pending invitations
	if len(orgs) == 1 && len(pendingInvites) == 0 {
		http.Redirect(w, r, "/orgs/"+orgs[0].Slug, http.StatusSeeOther)
		return
	}

	h.engine.Render(w, "dashboard.html", render.PageData{
		Title:       "Dashboard",
		User:        user,
		Orgs:        orgs,
		CurrentPath: r.URL.Path,
		Data:        map[string]any{"PendingInvites": pendingInvites},
	})
}
