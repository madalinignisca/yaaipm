package handlers

import (
	"errors"
	"log"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"github.com/madalin/forgedesk/internal/auth"
	"github.com/madalin/forgedesk/internal/middleware"
	"github.com/madalin/forgedesk/internal/models"
	"github.com/madalin/forgedesk/internal/render"
)

type ProjectHandler struct {
	db     *models.DB
	engine *render.Engine
	// scorerProviders are the debate scorer providers that actually have
	// an API key configured, in stable display order (issue #63). The
	// settings dropdown offers exactly these, and UpdateScorerProvider
	// refuses anything outside the list: storing a provider with no
	// registered scorer would leave the project silently unscored,
	// visible only as a WARNING in the server log.
	scorerProviders []string
}

func NewProjectHandler(db *models.DB, engine *render.Engine, scorerProviders []string) *ProjectHandler {
	return &ProjectHandler{db: db, engine: engine, scorerProviders: scorerProviders}
}

type projectPageData struct {
	Project   *models.Project
	Tab       string
	Projects  []models.Project
	Tickets   []models.Ticket
	Revisions []models.BriefRevision
	AllOrgs   []models.Organization
	// ScorerProviders are the selectable debate scorer providers for the
	// settings dropdown (issue #63); empty when no AI keys are configured.
	ScorerProviders []string
	IsStaff         bool
	// ScorerProviderUnavailable is true when the project's stored provider
	// is not in ScorerProviders — its API key was removed after an admin
	// selected it. The dropdown surfaces that rather than silently
	// appearing to say something else.
	ScorerProviderUnavailable bool
}

func (h *ProjectHandler) getOrgAndProject(r *http.Request, user *models.User) (*models.Organization, *models.Project, error) {
	orgSlug := r.PathValue("orgSlug")
	projSlug := r.PathValue("projSlug")

	org, err := h.db.GetOrgBySlug(r.Context(), orgSlug)
	if err != nil {
		return nil, nil, err
	}

	// Returns errCrossTenant for a genuine non-member and the underlying
	// error otherwise, so callers CAN tell them apart. Today every caller
	// still renders 404 for both, which is safe — but the distinction now
	// exists in the returned error instead of being lost here, and an
	// infrastructure fault is logged where it happens rather than
	// vanishing into a "Not found" page (#128).
	if authErr := authorizeOrgAccess(r.Context(), h.db, user, org.ID); authErr != nil {
		if !errors.Is(authErr, errCrossTenant) {
			log.Printf("project authz for user %s org %s: %v", user.ID, org.ID, authErr)
		}
		return nil, nil, authErr
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

	_ = h.engine.Render(w, r, "project_brief.html", render.PageData{
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

	w.Header().Set("Hx-Redirect", r.URL.Path)
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

	w.Header().Set("Hx-Redirect", r.URL.Path)
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

	_ = h.engine.Render(w, r, "project_features.html", render.PageData{
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

	_ = h.engine.Render(w, r, "project_bugs.html", render.PageData{
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

	_ = h.engine.Render(w, r, "project_gantt.html", render.PageData{
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

	if canManage, authErr := authorizeOrgManage(r.Context(), h.db, user, org.ID); authErr != nil {
		log.Printf("create project authz for user %s org %s: %v", user.ID, org.ID, authErr)
		http.Error(w, "Internal error", http.StatusInternalServerError)
		return
	} else if !canManage {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
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

	_ = h.engine.Render(w, r, "project_archived.html", render.PageData{
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
	allOrgs, _ := h.db.ListAllOrgs(r.Context())

	_ = h.engine.Render(w, r, "project_settings.html", render.PageData{
		Title: proj.Name + " — Settings", User: user, Org: org, Orgs: middleware.GetOrgs(r), CurrentPath: r.URL.Path,
		Projects: projects, ActiveProject: proj, ActiveTab: "settings",
		ProjectID: proj.ID,
		Data: projectPageData{
			Project: proj, Projects: projects, Tab: "settings", IsStaff: true, AllOrgs: allOrgs,
			ScorerProviders:           h.scorerProviders,
			ScorerProviderUnavailable: !slices.Contains(h.scorerProviders, proj.ScorerProvider),
		},
	})
}

// UpdateScorerProvider sets which AI provider scores this project's
// feature debates (issue #63). Staff-only, like every other setting on
// this page.
func (h *ProjectHandler) UpdateScorerProvider(w http.ResponseWriter, r *http.Request) {
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

	provider := strings.TrimSpace(r.FormValue("scorer_provider"))

	// An absent field is "nothing was selected", not "invalid selection",
	// and must not be reported as an error. It happens for real: when the
	// stored provider has lost its API key the dropdown renders it as a
	// disabled option, and HTML form submission SKIPS disabled options
	// even when they are selected — so a staff user who opens settings
	// and clicks Save without touching the dropdown sends no field at
	// all. Treat that as a no-op and return them to the page.
	if provider == "" {
		w.Header().Set("Hx-Redirect", r.URL.Path[:strings.LastIndex(r.URL.Path, "/")])
		w.WriteHeader(http.StatusOK)
		return
	}

	// One membership check covers both remaining failure modes: a name
	// that is not a provider at all, and a real provider whose API key
	// this deployment lacks. Either way the value must not reach the
	// database — the CHECK constraint would catch the former but not the
	// latter, and storing the latter leaves the project silently
	// unscored.
	if !slices.Contains(h.scorerProviders, provider) {
		http.Error(w, "Unknown or unconfigured scorer provider", http.StatusBadRequest)
		return
	}

	if err := h.db.UpdateProjectScorerProvider(r.Context(), proj.ID, provider); err != nil {
		log.Printf("updating scorer provider: %v", err)
		http.Error(w, "Failed to update", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Hx-Redirect", r.URL.Path[:strings.LastIndex(r.URL.Path, "/")])
	w.WriteHeader(http.StatusOK)
}

func (h *ProjectHandler) TransferProject(w http.ResponseWriter, r *http.Request) {
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

	targetOrgID := r.FormValue("target_org_id")
	if targetOrgID == "" || targetOrgID == org.ID {
		http.Error(w, "Invalid target organization", http.StatusBadRequest)
		return
	}

	targetOrg, err := h.db.GetOrgByID(r.Context(), targetOrgID)
	if err != nil {
		http.Error(w, "Target organization not found", http.StatusBadRequest)
		return
	}

	if err := h.db.TransferProject(r.Context(), proj.ID, targetOrgID); err != nil {
		if strings.Contains(err.Error(), "duplicate key") || strings.Contains(err.Error(), "unique constraint") {
			// Slug collision in target org
			projects := middleware.GetProjects(r)
			allOrgs, _ := h.db.ListAllOrgs(r.Context())
			_ = h.engine.Render(w, r, "project_settings.html", render.PageData{
				Title: proj.Name + " — Settings", User: user, Org: org, Orgs: middleware.GetOrgs(r), CurrentPath: r.URL.Path,
				Projects: projects, ActiveProject: proj, ActiveTab: "settings",
				ProjectID: proj.ID,
				Flash:     "Cannot transfer: " + targetOrg.Name + " already has a project with slug \"" + proj.Slug + "\".",
				FlashType: "error",
				Data:      projectPageData{Project: proj, Projects: projects, Tab: "settings", IsStaff: true, AllOrgs: allOrgs},
			})
			return
		}
		log.Printf("transferring project: %v", err)
		http.Error(w, "Failed to transfer project", http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, "/orgs/"+targetOrg.Slug+"/projects/"+proj.Slug+"/settings", http.StatusSeeOther)
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

	// Validate repo URL scheme if non-empty
	if repoURL != "" {
		validSchemes := map[string]bool{"http": true, "https": true, "ssh": true, "git": true}
		if u, err := url.Parse(repoURL); err != nil || (u.Scheme != "" && !validSchemes[u.Scheme]) {
			http.Error(w, "Invalid repository URL (only http, https, ssh, git allowed)", http.StatusBadRequest)
			return
		}
	}

	if err := h.db.UpdateProjectRepoURL(r.Context(), proj.ID, repoURL); err != nil {
		log.Printf("updating repo url: %v", err)
		http.Error(w, "Failed to update", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Hx-Redirect", r.URL.Path[:strings.LastIndex(r.URL.Path, "/")])
	w.WriteHeader(http.StatusOK)
}
