package handlers

import (
	"log"
	"net/http"
	"strings"

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

	orgs := middleware.GetOrgs(r)

	platform, _ := h.db.GetPlatformSettings(r.Context())

	h.engine.Render(w, "admin.html", render.PageData{
		Title:       "Admin Panel",
		User:        user,
		Orgs:        orgs,
		Projects:    middleware.GetProjects(r),
		CurrentPath: r.URL.Path,
		Data: map[string]any{
			"Users":    users,
			"Orgs":     orgs,
			"Platform": platform,
		},
	})
}

func (h *AdminHandler) UpdatePlatformBusiness(w http.ResponseWriter, r *http.Request) {
	if err := h.db.UpdatePlatformSettings(r.Context(),
		strings.TrimSpace(r.FormValue("business_name")),
		strings.TrimSpace(r.FormValue("vat_number")),
		strings.TrimSpace(r.FormValue("registration_number")),
		strings.TrimSpace(r.FormValue("address_street")),
		strings.TrimSpace(r.FormValue("address_extra")),
		strings.TrimSpace(r.FormValue("postal_code")),
		strings.TrimSpace(r.FormValue("city")),
		strings.TrimSpace(r.FormValue("country")),
		strings.TrimSpace(r.FormValue("contact_phones")),
		strings.TrimSpace(r.FormValue("contact_emails")),
	); err != nil {
		log.Printf("updating platform business details: %v", err)
		http.Error(w, "Failed to update platform business details", http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, "/admin", http.StatusSeeOther)
}
