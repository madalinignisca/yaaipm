package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/madalin/forgedesk/internal/ai"
	"github.com/madalin/forgedesk/internal/auth"
	"github.com/madalin/forgedesk/internal/middleware"
	"github.com/madalin/forgedesk/internal/models"
	"github.com/madalin/forgedesk/internal/render"
	"github.com/madalin/forgedesk/internal/testutil"
)

// setupScorerSettingsEnv wires just the project-settings routes with an
// explicit list of configured scorer providers (issue #63). The list is
// what main.go passes from the live scorer registry, so a test can model
// a deployment that is missing a vendor's API key.
func setupScorerSettingsEnv(t *testing.T, providers []string) (*chi.Mux, *models.DB, *auth.SessionStore) {
	t.Helper()

	pool := testutil.SetupTestDB(t)
	db := models.NewDB(pool)
	sessions := auth.NewSessionStore(pool)
	engine, err := render.NewEngine(testutil.ProjectRoot()+"/templates", nil)
	if err != nil {
		t.Fatalf("loading templates: %v", err)
	}
	projH := NewProjectHandler(db, engine, providers)

	r := chi.NewRouter()
	r.Use(middleware.Recover)
	r.Group(func(r chi.Router) {
		r.Use(middleware.AuthMiddleware(sessions, db))
		r.Get("/orgs/{orgSlug}/projects/{projSlug}/settings", projH.ProjectSettings)
		r.Post("/orgs/{orgSlug}/projects/{projSlug}/settings/scorer", projH.UpdateScorerProvider)
	})
	return r, db, sessions
}

// seedOrgProject creates an org owned by the given user plus one project.
func seedOrgProject(t *testing.T, db *models.DB, email, role string) (*models.Organization, *models.Project, string) {
	t.Helper()
	ctx := context.Background()

	hash, _ := auth.HashPassword(context.Background(), "TestPassword123!")
	user, err := db.CreateUser(ctx, email, hash, "Test User", role)
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	org, err := db.CreateOrgWithOwnerTx(ctx, user.ID, "Org "+t.Name(), "org-"+strings.ToLower(t.Name()), models.OrgRoleOwner)
	if err != nil {
		t.Fatalf("CreateOrgWithOwnerTx: %v", err)
	}
	proj, err := db.CreateProject(ctx, org.ID, "P", "proj-"+strings.ToLower(t.Name()))
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	return org, proj, user.ID
}

func postScorer(t *testing.T, r *chi.Mux, cookie *http.Cookie, org *models.Organization, proj *models.Project, provider string) *httptest.ResponseRecorder {
	t.Helper()
	form := url.Values{"scorer_provider": {provider}}
	req := httptest.NewRequest(http.MethodPost,
		"/orgs/"+org.Slug+"/projects/"+proj.Slug+"/settings/scorer",
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(cookie)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func providerOf(t *testing.T, db *models.DB, projectID string) string {
	t.Helper()
	p, err := db.GetProjectScorerProvider(context.Background(), projectID)
	if err != nil {
		t.Fatalf("GetProjectScorerProvider: %v", err)
	}
	return p
}

// The dropdown is staff-only: clients must never see or set which AI
// vendor processes their project's text.
func TestUpdateScorerProvider_ClientForbidden(t *testing.T) {
	r, db, sessions := setupScorerSettingsEnv(t, []string{ai.ProviderGemini, ai.ProviderOpenAI})
	org, proj, _ := seedOrgProject(t, db, "client@test.com", "client")
	cookie := createAuthenticatedUser(t, db, sessions, "client2@test.com", "client")
	_ = org

	w := postScorer(t, r, cookie, org, proj, ai.ProviderOpenAI)

	// Exactly 403: the role check runs before getOrgAndProject, so a
	// client never reaches the lookup that could 404.
	if w.Code != http.StatusForbidden {
		t.Errorf("client POST: got %d, want 403", w.Code)
	}
	if got := providerOf(t, db, proj.ID); got != ai.ProviderGemini {
		t.Errorf("provider = %q, want it unchanged at gemini", got)
	}
}

func TestUpdateScorerProvider_StaffCanSetConfiguredProvider(t *testing.T) {
	r, db, sessions := setupScorerSettingsEnv(t, []string{ai.ProviderGemini, ai.ProviderOpenAI})
	_, proj, userID := seedOrgProject(t, db, "staff@test.com", "staff")
	org, err := db.GetOrgBySlug(context.Background(), "org-"+strings.ToLower(t.Name()))
	if err != nil {
		t.Fatalf("GetOrgBySlug: %v", err)
	}
	_ = userID
	cookie := createAuthenticatedUser(t, db, sessions, "staff2@test.com", "staff")

	w := postScorer(t, r, cookie, org, proj, ai.ProviderOpenAI)

	if w.Code != http.StatusOK {
		t.Fatalf("staff POST: got %d, want 200 — body: %s", w.Code, w.Body.String())
	}
	if got := providerOf(t, db, proj.ID); got != ai.ProviderOpenAI {
		t.Errorf("provider = %q, want openai", got)
	}
}

// A provider whose API key isn't configured must be rejected rather than
// silently stored — storing it would leave the project permanently
// unscored, visible only as a WARNING in the server log.
func TestUpdateScorerProvider_RejectsUnconfiguredProvider(t *testing.T) {
	r, db, sessions := setupScorerSettingsEnv(t, []string{ai.ProviderGemini})
	_, proj, _ := seedOrgProject(t, db, "staff@test.com", "staff")
	org, err := db.GetOrgBySlug(context.Background(), "org-"+strings.ToLower(t.Name()))
	if err != nil {
		t.Fatalf("GetOrgBySlug: %v", err)
	}
	cookie := createAuthenticatedUser(t, db, sessions, "staff2@test.com", "staff")

	// claude is a valid provider name but has no registered scorer here.
	w := postScorer(t, r, cookie, org, proj, ai.ProviderClaude)

	if w.Code != http.StatusBadRequest {
		t.Errorf("unconfigured provider: got %d, want 400", w.Code)
	}
	if got := providerOf(t, db, proj.ID); got != ai.ProviderGemini {
		t.Errorf("provider = %q, want it unchanged at gemini", got)
	}
}

// An absent scorer_provider field is a no-op, not an error. This is a
// real interaction, not a hypothetical: when the stored provider has lost
// its API key the dropdown renders it as a disabled option, and HTML form
// submission skips disabled options even when selected — so clicking Save
// without touching the dropdown submits no field at all. Returning 400
// there would show a bare error page for doing nothing wrong.
func TestUpdateScorerProvider_EmptySubmissionIsNoOp(t *testing.T) {
	r, db, sessions := setupScorerSettingsEnv(t, []string{ai.ProviderGemini})
	_, proj, _ := seedOrgProject(t, db, "staff@test.com", "staff")
	org, err := db.GetOrgBySlug(context.Background(), "org-"+strings.ToLower(t.Name()))
	if err != nil {
		t.Fatalf("GetOrgBySlug: %v", err)
	}
	cookie := createAuthenticatedUser(t, db, sessions, "staff2@test.com", "staff")

	w := postScorer(t, r, cookie, org, proj, "")

	if w.Code != http.StatusOK {
		t.Errorf("empty submission: got %d, want 200 (no-op)", w.Code)
	}
	if got := providerOf(t, db, proj.ID); got != ai.ProviderGemini {
		t.Errorf("provider = %q, want it unchanged at gemini", got)
	}
}

func TestUpdateScorerProvider_RejectsUnknownProvider(t *testing.T) {
	r, db, sessions := setupScorerSettingsEnv(t, []string{ai.ProviderGemini, ai.ProviderOpenAI})
	_, proj, _ := seedOrgProject(t, db, "staff@test.com", "staff")
	org, err := db.GetOrgBySlug(context.Background(), "org-"+strings.ToLower(t.Name()))
	if err != nil {
		t.Fatalf("GetOrgBySlug: %v", err)
	}
	cookie := createAuthenticatedUser(t, db, sessions, "staff2@test.com", "staff")

	w := postScorer(t, r, cookie, org, proj, "llama")

	if w.Code != http.StatusBadRequest {
		t.Errorf("unknown provider: got %d, want 400", w.Code)
	}
	if got := providerOf(t, db, proj.ID); got != ai.ProviderGemini {
		t.Errorf("provider = %q, want it unchanged at gemini", got)
	}
}

// The settings page must render the dropdown with only configured
// providers, and mark the project's current one as selected.
func TestProjectSettings_RendersScorerDropdown(t *testing.T) {
	r, db, sessions := setupScorerSettingsEnv(t, []string{ai.ProviderGemini, ai.ProviderOpenAI})
	_, proj, _ := seedOrgProject(t, db, "staff@test.com", "staff")
	org, err := db.GetOrgBySlug(context.Background(), "org-"+strings.ToLower(t.Name()))
	if err != nil {
		t.Fatalf("GetOrgBySlug: %v", err)
	}
	cookie := createAuthenticatedUser(t, db, sessions, "staff2@test.com", "staff")

	req := httptest.NewRequest(http.MethodGet,
		"/orgs/"+org.Slug+"/projects/"+proj.Slug+"/settings", http.NoBody)
	req.AddCookie(cookie)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("settings page: got %d, want 200", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, `name="scorer_provider"`) {
		t.Error("settings page should render the scorer_provider select")
	}
	// Configured providers appear; unconfigured ones must not be offered.
	if !strings.Contains(body, `value="openai"`) {
		t.Error("configured provider openai should be an option")
	}
	if strings.Contains(body, `value="claude"`) {
		t.Error("unconfigured provider claude must not be offered")
	}
}
