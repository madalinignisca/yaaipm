package handlers

import (
	"log"
	"net/http"
	"strings"

	"github.com/madalin/forgedesk/internal/auth"
	"github.com/madalin/forgedesk/internal/middleware"
	"github.com/madalin/forgedesk/internal/models"
	"github.com/madalin/forgedesk/internal/render"
)

type ProjectHandler struct {
	db     *models.DB
	engine *render.Engine
}

func NewProjectHandler(db *models.DB, engine *render.Engine) *ProjectHandler {
	return &ProjectHandler{db: db, engine: engine}
}

type projectPageData struct {
	Project  *models.Project
	Projects []models.Project
	Tab      string
	Tickets  []models.Ticket
	IsStaff  bool
}

func (h *ProjectHandler) getOrgAndProject(r *http.Request, user *models.User) (*models.Organization, *models.Project, error) {
	orgSlug := r.PathValue("orgSlug")
	projSlug := r.PathValue("projSlug")

	org, err := h.db.GetOrgBySlug(r.Context(), orgSlug)
	if err != nil {
		return nil, nil, err
	}

	if !auth.IsStaffOrAbove(user.Role) {
		if _, err := h.db.GetOrgMembership(r.Context(), user.ID, org.ID); err != nil {
			return nil, nil, err
		}
	}

	proj, err := h.db.GetProject(r.Context(), org.ID, projSlug)
	if err != nil {
		return nil, nil, err
	}

	return org, proj, nil
}

func (h *ProjectHandler) ProjectBrief(w http.ResponseWriter, r *http.Request) {
	user := middleware.GetUser(r)
	org, proj, err := h.getOrgAndProject(r, user)
	if err != nil {
		h.engine.RenderError(w, http.StatusNotFound, "Not found")
		return
	}

	projects, _ := h.db.ListProjects(r.Context(), org.ID)
	orgs := h.loadOrgs(r, user)

	h.engine.Render(w, "project_brief.html", render.PageData{
		Title: proj.Name + " — Brief", User: user, Org: org, Orgs: orgs, CurrentPath: r.URL.Path,
		ProjectID: proj.ID,
		Data:      projectPageData{Project: proj, Projects: projects, Tab: "brief", IsStaff: auth.IsStaffOrAbove(user.Role)},
	})
}

func (h *ProjectHandler) UpdateBrief(w http.ResponseWriter, r *http.Request) {
	user := middleware.GetUser(r)
	_, proj, err := h.getOrgAndProject(r, user)
	if err != nil {
		h.engine.RenderError(w, http.StatusNotFound, "Not found")
		return
	}

	if !auth.IsStaffOrAbove(user.Role) {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}

	brief := r.FormValue("brief")
	if err := h.db.UpdateProjectBrief(r.Context(), proj.ID, brief); err != nil {
		log.Printf("updating brief: %v", err)
		http.Error(w, "Failed to update", http.StatusInternalServerError)
		return
	}

	w.Header().Set("HX-Redirect", r.URL.Path)
	w.WriteHeader(http.StatusOK)
}

func (h *ProjectHandler) ProjectFeatures(w http.ResponseWriter, r *http.Request) {
	user := middleware.GetUser(r)
	org, proj, err := h.getOrgAndProject(r, user)
	if err != nil {
		h.engine.RenderError(w, http.StatusNotFound, "Not found")
		return
	}

	epics, _ := h.db.ListEpics(r.Context(), proj.ID)
	projects, _ := h.db.ListProjects(r.Context(), org.ID)
	orgs := h.loadOrgs(r, user)

	h.engine.Render(w, "project_features.html", render.PageData{
		Title: proj.Name + " — Features", User: user, Org: org, Orgs: orgs, CurrentPath: r.URL.Path,
		ProjectID: proj.ID,
		Data:      projectPageData{Project: proj, Projects: projects, Tab: "features", Tickets: epics, IsStaff: auth.IsStaffOrAbove(user.Role)},
	})
}

func (h *ProjectHandler) ProjectBugs(w http.ResponseWriter, r *http.Request) {
	user := middleware.GetUser(r)
	org, proj, err := h.getOrgAndProject(r, user)
	if err != nil {
		h.engine.RenderError(w, http.StatusNotFound, "Not found")
		return
	}

	bugs, _ := h.db.ListBugs(r.Context(), proj.ID)
	projects, _ := h.db.ListProjects(r.Context(), org.ID)
	orgs := h.loadOrgs(r, user)

	h.engine.Render(w, "project_bugs.html", render.PageData{
		Title: proj.Name + " — Bugs", User: user, Org: org, Orgs: orgs, CurrentPath: r.URL.Path,
		ProjectID: proj.ID,
		Data:      projectPageData{Project: proj, Projects: projects, Tab: "bugs", Tickets: bugs, IsStaff: auth.IsStaffOrAbove(user.Role)},
	})
}

func (h *ProjectHandler) ProjectGantt(w http.ResponseWriter, r *http.Request) {
	user := middleware.GetUser(r)
	org, proj, err := h.getOrgAndProject(r, user)
	if err != nil {
		h.engine.RenderError(w, http.StatusNotFound, "Not found")
		return
	}

	tickets, _ := h.db.ListGanttTickets(r.Context(), proj.ID)
	projects, _ := h.db.ListProjects(r.Context(), org.ID)
	orgs := h.loadOrgs(r, user)

	h.engine.Render(w, "project_gantt.html", render.PageData{
		Title: proj.Name + " — Timeline", User: user, Org: org, Orgs: orgs, CurrentPath: r.URL.Path,
		ProjectID: proj.ID,
		Data:      projectPageData{Project: proj, Projects: projects, Tab: "gantt", Tickets: tickets, IsStaff: auth.IsStaffOrAbove(user.Role)},
	})
}

func (h *ProjectHandler) CreateProject(w http.ResponseWriter, r *http.Request) {
	user := middleware.GetUser(r)
	orgSlug := r.PathValue("orgSlug")
	name := strings.TrimSpace(r.FormValue("name"))

	if name == "" {
		http.Error(w, "Name is required", http.StatusBadRequest)
		return
	}

	org, err := h.db.GetOrgBySlug(r.Context(), orgSlug)
	if err != nil {
		h.engine.RenderError(w, http.StatusNotFound, "Organization not found")
		return
	}

	if !auth.IsStaffOrAbove(user.Role) {
		mem, err := h.db.GetOrgMembership(r.Context(), user.ID, org.ID)
		if err != nil || !auth.CanManageOrg(mem.Role) {
			http.Error(w, "Forbidden", http.StatusForbidden)
			return
		}
	}

	slug := slugify(name)
	_, err = h.db.CreateProject(r.Context(), org.ID, name, slug)
	if err != nil {
		log.Printf("creating project: %v", err)
		http.Error(w, "Failed to create project", http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, "/orgs/"+orgSlug+"/projects/"+slug+"/brief", http.StatusSeeOther)
}

func (h *ProjectHandler) ProjectArchived(w http.ResponseWriter, r *http.Request) {
	user := middleware.GetUser(r)
	if !auth.IsStaffOrAbove(user.Role) {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}

	org, proj, err := h.getOrgAndProject(r, user)
	if err != nil {
		h.engine.RenderError(w, http.StatusNotFound, "Not found")
		return
	}

	archived, _ := h.db.ListArchivedTickets(r.Context(), proj.ID)
	projects, _ := h.db.ListProjects(r.Context(), org.ID)
	orgs := h.loadOrgs(r, user)

	h.engine.Render(w, "project_archived.html", render.PageData{
		Title: proj.Name + " — Archived", User: user, Org: org, Orgs: orgs, CurrentPath: r.URL.Path,
		ProjectID: proj.ID,
		Data:      projectPageData{Project: proj, Projects: projects, Tab: "archived", Tickets: archived, IsStaff: true},
	})
}

func (h *ProjectHandler) loadOrgs(r *http.Request, user *models.User) []models.Organization {
	var orgs []models.Organization
	if auth.IsStaffOrAbove(user.Role) {
		orgs, _ = h.db.ListAllOrgs(r.Context())
	} else {
		orgs, _ = h.db.ListUserOrgs(r.Context(), user.ID)
	}
	return orgs
}
