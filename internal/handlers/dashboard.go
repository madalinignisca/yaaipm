package handlers

import (
	"net/http"

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

	orgs := middleware.GetOrgs(r)
	pendingInvites, _ := h.db.ListPendingInvitationsForUser(r.Context(), user.Email)

	// Auto-redirect if org selected and no pending invitations
	if selectedOrg := middleware.GetOrg(r); selectedOrg != nil && len(pendingInvites) == 0 {
		http.Redirect(w, r, "/orgs/"+selectedOrg.Slug, http.StatusSeeOther)
		return
	}

	// Fallback: if one org and no pending invites
	if len(orgs) == 1 && len(pendingInvites) == 0 {
		http.Redirect(w, r, "/orgs/"+orgs[0].Slug, http.StatusSeeOther)
		return
	}

	_ = h.engine.Render(w, r, "dashboard.html", render.PageData{
		Title:       "Dashboard",
		User:        user,
		Orgs:        orgs,
		Projects:    middleware.GetProjects(r),
		CurrentPath: r.URL.Path,
		Data:        map[string]any{"PendingInvites": pendingInvites},
	})
}
