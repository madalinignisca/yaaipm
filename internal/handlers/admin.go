package handlers

import (
	"net/http"

	"github.com/madalin/forgedesk/internal/middleware"
	"github.com/madalin/forgedesk/internal/models"
	"github.com/madalin/forgedesk/internal/render"
)

type AdminHandler struct {
	db     *models.DB
	engine *render.Engine
}

func NewAdminHandler(db *models.DB, engine *render.Engine) *AdminHandler {
	return &AdminHandler{db: db, engine: engine}
}

func (h *AdminHandler) AdminPage(w http.ResponseWriter, r *http.Request) {
	user := middleware.GetUser(r)

	users, err := h.db.ListUsers(r.Context())
	if err != nil {
		h.engine.RenderError(w, http.StatusInternalServerError, "Failed to load users")
		return
	}

	orgs, err := h.db.ListAllOrgs(r.Context())
	if err != nil {
		h.engine.RenderError(w, http.StatusInternalServerError, "Failed to load organizations")
		return
	}

	pricing, _ := h.db.ListModelPricing(r.Context())

	h.engine.Render(w, "admin.html", render.PageData{
		Title:       "Admin Panel",
		User:        user,
		Orgs:        orgs,
		CurrentPath: r.URL.Path,
		Data: map[string]any{
			"Users":   users,
			"Orgs":    orgs,
			"Pricing": pricing,
		},
	})
}
