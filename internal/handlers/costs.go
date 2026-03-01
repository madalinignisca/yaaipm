package handlers

import (
	"log"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/madalin/forgedesk/internal/auth"
	"github.com/madalin/forgedesk/internal/middleware"
	"github.com/madalin/forgedesk/internal/models"
	"github.com/madalin/forgedesk/internal/render"
)

type CostHandler struct {
	db     *models.DB
	engine *render.Engine
}

func NewCostHandler(db *models.DB, engine *render.Engine) *CostHandler {
	return &CostHandler{db: db, engine: engine}
}

// currentMonth returns the current YYYY-MM string.
func currentMonth() string {
	return time.Now().Format("2006-01")
}

// parseMonth validates and returns the month from query param, defaulting to current.
func parseMonth(r *http.Request) string {
	m := r.URL.Query().Get("month")
	if m == "" {
		return currentMonth()
	}
	// Validate format
	if _, err := time.Parse("2006-01", m); err != nil {
		return currentMonth()
	}
	return m
}

// adjacentMonths returns prev and next YYYY-MM strings.
func adjacentMonths(month string) (string, string) {
	t, err := time.Parse("2006-01", month)
	if err != nil {
		t = time.Now()
	}
	prev := t.AddDate(0, -1, 0).Format("2006-01")
	next := t.AddDate(0, 1, 0).Format("2006-01")
	return prev, next
}

// ProjectCosts renders the costs tab for a project.
func (h *CostHandler) ProjectCosts(w http.ResponseWriter, r *http.Request) {
	user := middleware.GetUser(r)
	orgSlug := r.PathValue("orgSlug")
	projSlug := r.PathValue("projSlug")

	org, err := h.db.GetOrgBySlug(r.Context(), orgSlug)
	if err != nil {
		h.engine.RenderError(w, http.StatusNotFound, "Organization not found")
		return
	}

	if !auth.IsStaffOrAbove(user.Role) {
		if _, err := h.db.GetOrgMembership(r.Context(), user.ID, org.ID); err != nil {
			h.engine.RenderError(w, http.StatusForbidden, "Access denied")
			return
		}
	}

	proj, err := h.db.GetProject(r.Context(), org.ID, projSlug)
	if err != nil {
		h.engine.RenderError(w, http.StatusNotFound, "Project not found")
		return
	}

	month := parseMonth(r)
	prevMonth, nextMonth := adjacentMonths(month)

	costs, _ := h.db.ListProjectCosts(r.Context(), proj.ID, month)
	aiUsage, _ := h.db.ListAIUsageByProjectMonth(r.Context(), proj.ID, month)

	var infraTotal int64
	for _, c := range costs {
		infraTotal += c.AmountCents
	}
	var aiTotal int64
	for _, u := range aiUsage {
		aiTotal += u.TotalCents
	}

	margin := org.AIMarginPercent
	aiTotalWithMargin := aiTotal + aiTotal*int64(margin)/100

	projects, _ := h.db.ListProjects(r.Context(), org.ID)
	orgs := h.loadOrgs(r, user)

	canEdit := auth.IsStaffOrAbove(user.Role)

	h.engine.Render(w, "project_costs.html", render.PageData{
		Title: proj.Name + " — Costs", User: user, Org: org, Orgs: orgs, CurrentPath: r.URL.Path,
		ProjectID: proj.ID,
		Data: map[string]any{
			"Project":           proj,
			"Projects":          projects,
			"Tab":               "costs",
			"Month":             month,
			"PrevMonth":         prevMonth,
			"NextMonth":         nextMonth,
			"Costs":             costs,
			"AIUsage":           aiUsage,
			"InfraTotal":        infraTotal,
			"AITotal":           aiTotal,
			"AITotalWithMargin": aiTotalWithMargin,
			"GrandTotal":        infraTotal + aiTotalWithMargin,
			"AIMargin":          margin,
			"CanEdit":           canEdit,
			"IsStaff":           canEdit,
		},
	})
}

// OrgCosts renders the org-level costs overview.
func (h *CostHandler) OrgCosts(w http.ResponseWriter, r *http.Request) {
	user := middleware.GetUser(r)
	orgSlug := r.PathValue("orgSlug")

	org, err := h.db.GetOrgBySlug(r.Context(), orgSlug)
	if err != nil {
		h.engine.RenderError(w, http.StatusNotFound, "Organization not found")
		return
	}

	if !auth.IsStaffOrAbove(user.Role) {
		if _, err := h.db.GetOrgMembership(r.Context(), user.ID, org.ID); err != nil {
			h.engine.RenderError(w, http.StatusForbidden, "Access denied")
			return
		}
	}

	month := parseMonth(r)
	prevMonth, nextMonth := adjacentMonths(month)

	// Get all projects in this org
	projects, _ := h.db.ListProjects(r.Context(), org.ID)

	// Per-project costs
	type projectCostData struct {
		Project    models.Project
		Costs      []models.ProjectCost
		InfraTotal int64
	}
	var projectCosts []projectCostData
	var orgInfraTotal int64
	for _, p := range projects {
		costs, _ := h.db.ListProjectCosts(r.Context(), p.ID, month)
		var total int64
		for _, c := range costs {
			total += c.AmountCents
		}
		if len(costs) > 0 {
			projectCosts = append(projectCosts, projectCostData{Project: p, Costs: costs, InfraTotal: total})
		}
		orgInfraTotal += total
	}

	aiUsage, _ := h.db.ListAIUsageByOrgMonth(r.Context(), org.ID, month)
	var aiTotal int64
	for _, u := range aiUsage {
		aiTotal += u.TotalCents
	}

	margin := org.AIMarginPercent
	aiTotalWithMargin := aiTotal + aiTotal*int64(margin)/100

	orgs := h.loadOrgs(r, user)

	canEdit := auth.IsStaffOrAbove(user.Role)

	h.engine.Render(w, "org_costs.html", render.PageData{
		Title: org.Name + " — Costs", User: user, Org: org, Orgs: orgs, CurrentPath: r.URL.Path,
		Data: map[string]any{
			"Month":              month,
			"PrevMonth":          prevMonth,
			"NextMonth":          nextMonth,
			"ProjectCosts":       projectCosts,
			"AIUsage":            aiUsage,
			"OrgInfraTotal":      orgInfraTotal,
			"AITotal":            aiTotal,
			"AITotalWithMargin":  aiTotalWithMargin,
			"GrandTotal":         orgInfraTotal + aiTotalWithMargin,
			"AIMargin":           margin,
			"CanEdit":            canEdit,
		},
	})
}

// AddCostItem creates a new cost line item for a project.
func (h *CostHandler) AddCostItem(w http.ResponseWriter, r *http.Request) {
	user := middleware.GetUser(r)
	if !auth.IsStaffOrAbove(user.Role) {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}

	orgSlug := r.PathValue("orgSlug")
	projSlug := r.PathValue("projSlug")

	org, err := h.db.GetOrgBySlug(r.Context(), orgSlug)
	if err != nil {
		http.Error(w, "Organization not found", http.StatusNotFound)
		return
	}

	proj, err := h.db.GetProject(r.Context(), org.ID, projSlug)
	if err != nil {
		http.Error(w, "Project not found", http.StatusNotFound)
		return
	}

	category := r.FormValue("category")
	name := strings.TrimSpace(r.FormValue("name"))
	amountStr := r.FormValue("amount")
	month := r.FormValue("month")
	if month == "" {
		month = currentMonth()
	}

	if category == "" || amountStr == "" {
		http.Error(w, "Category and amount are required", http.StatusBadRequest)
		return
	}

	amountCents, err := parseToCents(amountStr)
	if err != nil {
		http.Error(w, "Invalid amount", http.StatusBadRequest)
		return
	}

	_, err = h.db.CreateProjectCost(r.Context(), proj.ID, month, category, name, amountCents)
	if err != nil {
		log.Printf("creating project cost: %v", err)
		http.Error(w, "Failed to add cost item", http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, "/orgs/"+orgSlug+"/projects/"+projSlug+"/costs?month="+month, http.StatusSeeOther)
}

// UpdateCostItem updates a cost line item amount.
func (h *CostHandler) UpdateCostItem(w http.ResponseWriter, r *http.Request) {
	user := middleware.GetUser(r)
	if !auth.IsStaffOrAbove(user.Role) {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}

	costID := chi.URLParam(r, "costID")
	amountStr := r.FormValue("amount")

	amountCents, err := parseToCents(amountStr)
	if err != nil {
		http.Error(w, "Invalid amount", http.StatusBadRequest)
		return
	}

	if err := h.db.UpdateProjectCost(r.Context(), costID, amountCents); err != nil {
		log.Printf("updating project cost: %v", err)
		http.Error(w, "Failed to update", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
}

// DeleteCostItem deletes a cost line item.
func (h *CostHandler) DeleteCostItem(w http.ResponseWriter, r *http.Request) {
	user := middleware.GetUser(r)
	if !auth.IsStaffOrAbove(user.Role) {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}

	costID := chi.URLParam(r, "costID")

	// Look up the cost so we can redirect back to the right page.
	cost, err := h.db.GetProjectCost(r.Context(), costID)
	if err != nil {
		http.Error(w, "Cost item not found", http.StatusNotFound)
		return
	}

	proj, err := h.db.GetProjectByID(r.Context(), cost.ProjectID)
	if err != nil {
		http.Error(w, "Project not found", http.StatusNotFound)
		return
	}

	org, err := h.db.GetOrgByID(r.Context(), proj.OrgID)
	if err != nil {
		http.Error(w, "Organization not found", http.StatusNotFound)
		return
	}

	if err := h.db.DeleteProjectCost(r.Context(), costID); err != nil {
		log.Printf("deleting project cost: %v", err)
		http.Error(w, "Failed to delete", http.StatusInternalServerError)
		return
	}

	redirect := "/orgs/" + org.Slug + "/projects/" + proj.Slug + "/costs?month=" + cost.Month
	w.Header().Set("HX-Redirect", redirect)
	w.WriteHeader(http.StatusOK)
}

func (h *CostHandler) loadOrgs(r *http.Request, user *models.User) []models.Organization {
	var orgs []models.Organization
	if auth.IsStaffOrAbove(user.Role) {
		orgs, _ = h.db.ListAllOrgs(r.Context())
	} else {
		orgs, _ = h.db.ListUserOrgs(r.Context(), user.ID)
	}
	return orgs
}

// parseToCents converts a currency string like "12.50" or "€12.50" to cents (1250).
func parseToCents(s string) (int64, error) {
	s = strings.TrimSpace(s)
	// Strip common currency symbols
	for _, sym := range []string{"$", "€", "£", "¥"} {
		s = strings.TrimPrefix(s, sym)
	}
	s = strings.TrimSpace(s)
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, err
	}
	return int64(math.Round(f * 100)), nil
}
