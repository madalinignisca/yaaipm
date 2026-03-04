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
	Project   *models.Project
	Projects  []models.Project
	Tab       string
	Tickets   []models.Ticket
	IsStaff   bool
	Revisions []models.BriefRevision
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

	projects := middleware.GetProjects(r)
	revisions, _ := h.db.ListBriefRevisions(r.Context(), proj.ID)

	h.engine.Render(w, "project_brief.html", render.PageData{
		Title: proj.Name + " — Brief", User: user, Org: org, Orgs: middleware.GetOrgs(r), CurrentPath: r.URL.Path,
		Projects: projects, ActiveProject: proj, ActiveTab: "brief",
		ProjectID: proj.ID,
		Data:      projectPageData{Project: proj, Projects: projects, Tab: "brief", IsStaff: auth.IsStaffOrAbove(user.Role), Revisions: revisions},
	})
}

func (h *ProjectHandler) UpdateBrief(w http.ResponseWriter, r *http.Request) {
	user := middleware.GetUser(r)
	_, proj, err := h.getOrgAndProject(r, user)
	if err != nil {
		h.engine.RenderError(w, http.StatusNotFound, "Not found")
		return
	}

	brief := r.FormValue("brief")

	// Save revision before overwriting
	if err := h.db.CreateBriefRevision(r.Context(), proj.ID, user.ID, "edit", proj.BriefMarkdown); err != nil {
		log.Printf("saving brief revision: %v", err)
	}

	if err := h.db.UpdateProjectBrief(r.Context(), proj.ID, brief); err != nil {
		log.Printf("updating brief: %v", err)
		http.Error(w, "Failed to update", http.StatusInternalServerError)
		return
	}

	w.Header().Set("HX-Redirect", r.URL.Path)
	w.WriteHeader(http.StatusOK)
}

func (h *ProjectHandler) MarkBriefReviewed(w http.ResponseWriter, r *http.Request) {
	user := middleware.GetUser(r)
	_, proj, err := h.getOrgAndProject(r, user)
	if err != nil {
		h.engine.RenderError(w, http.StatusNotFound, "Not found")
		return
	}

	if err := h.db.CreateBriefRevision(r.Context(), proj.ID, user.ID, "reviewed", ""); err != nil {
		log.Printf("saving brief review: %v", err)
		http.Error(w, "Failed to save", http.StatusInternalServerError)
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

	features, _ := h.db.ListFeatures(r.Context(), proj.ID)
	projects := middleware.GetProjects(r)

	h.engine.Render(w, "project_features.html", render.PageData{
		Title: proj.Name + " — Features", User: user, Org: org, Orgs: middleware.GetOrgs(r), CurrentPath: r.URL.Path,
		Projects: projects, ActiveProject: proj, ActiveTab: "features",
		ProjectID: proj.ID,
		Data:      projectPageData{Project: proj, Projects: projects, Tab: "features", Tickets: features, IsStaff: auth.IsStaffOrAbove(user.Role)},
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
	projects := middleware.GetProjects(r)

	h.engine.Render(w, "project_bugs.html", render.PageData{
		Title: proj.Name + " — Bugs", User: user, Org: org, Orgs: middleware.GetOrgs(r), CurrentPath: r.URL.Path,
		Projects: projects, ActiveProject: proj, ActiveTab: "bugs",
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
	projects := middleware.GetProjects(r)

	h.engine.Render(w, "project_gantt.html", render.PageData{
		Title: proj.Name + " — Timeline", User: user, Org: org, Orgs: middleware.GetOrgs(r), CurrentPath: r.URL.Path,
		Projects: projects, ActiveProject: proj, ActiveTab: "gantt",
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
	projects := middleware.GetProjects(r)

	h.engine.Render(w, "project_archived.html", render.PageData{
		Title: proj.Name + " — Archived", User: user, Org: org, Orgs: middleware.GetOrgs(r), CurrentPath: r.URL.Path,
		Projects: projects, ActiveProject: proj, ActiveTab: "archived",
		ProjectID: proj.ID,
		Data:      projectPageData{Project: proj, Projects: projects, Tab: "archived", Tickets: archived, IsStaff: true},
	})
}

func (h *ProjectHandler) ProjectSettings(w http.ResponseWriter, r *http.Request) {
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

	projects := middleware.GetProjects(r)

	h.engine.Render(w, "project_settings.html", render.PageData{
		Title: proj.Name + " — Settings", User: user, Org: org, Orgs: middleware.GetOrgs(r), CurrentPath: r.URL.Path,
		Projects: projects, ActiveProject: proj, ActiveTab: "settings",
		ProjectID: proj.ID,
		Data:      projectPageData{Project: proj, Projects: projects, Tab: "settings", IsStaff: true},
	})
}

func (h *ProjectHandler) UpdateRepoURL(w http.ResponseWriter, r *http.Request) {
	user := middleware.GetUser(r)
	if !auth.IsStaffOrAbove(user.Role) {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}

	_, proj, err := h.getOrgAndProject(r, user)
	if err != nil {
		h.engine.RenderError(w, http.StatusNotFound, "Not found")
		return
	}

	repoURL := strings.TrimSpace(r.FormValue("repo_url"))
	if err := h.db.UpdateProjectRepoURL(r.Context(), proj.ID, repoURL); err != nil {
		log.Printf("updating repo url: %v", err)
		http.Error(w, "Failed to update", http.StatusInternalServerError)
		return
	}

	w.Header().Set("HX-Redirect", r.URL.Path[:strings.LastIndex(r.URL.Path, "/")])
	w.WriteHeader(http.StatusOK)
}

