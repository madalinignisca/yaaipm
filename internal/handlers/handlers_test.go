package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/madalin/forgedesk/internal/auth"
	"github.com/madalin/forgedesk/internal/mail"
	"github.com/madalin/forgedesk/internal/middleware"
	"github.com/madalin/forgedesk/internal/models"
	"github.com/madalin/forgedesk/internal/render"
	"github.com/madalin/forgedesk/internal/testutil"
	"github.com/madalin/forgedesk/internal/ws"
)

func setupTestRouter(t *testing.T) (*chi.Mux, *models.DB, *auth.SessionStore, *render.Engine) {
	t.Helper()

	pool := testutil.SetupTestDB(t)
	db := models.NewDB(pool)
	sessions := auth.NewSessionStore(pool)

	templatesDir := testutil.ProjectRoot() + "/templates"
	if _, err := os.Stat(templatesDir); os.IsNotExist(err) {
		t.Skipf("templates directory not found at %s", templatesDir)
	}

	engine, err := render.NewEngine(templatesDir, nil)
	if err != nil {
		t.Fatalf("loading templates: %v", err)
	}

	aesKey := testutil.TestAESKey
	mailer := mail.NewMailer("", "", "", "", "")
	baseURL := "http://localhost:8081"

	authH := NewAuthHandler(db, sessions, engine, aesKey, false)
	dashH := NewDashboardHandler(db, engine)
	orgH := NewOrgHandler(db, engine, mailer, baseURL)
	projH := NewProjectHandler(db, engine)
	ticketH := NewTicketHandler(db, engine)
	commentH := NewCommentHandler(db, engine)
	adminH := NewAdminHandler(db, engine)
	accountH := NewAccountHandler(db, sessions, engine)
	inviteH := NewInviteHandler(db, sessions, engine, mailer, aesKey, baseURL, false)
	chatHub := ws.NewHub()
	go chatHub.Run()
	assistantH := NewAssistantHandler(db, engine, nil, chatHub) // nil gemini client — feature disabled in tests

	r := chi.NewRouter()
	r.Use(middleware.Recover)

	r.Get("/login", authH.LoginPage)
	r.Post("/login", authH.Login)
	r.Get("/register", authH.RegisterPage)
	r.Post("/register", authH.Register)
	r.Get("/setup-2fa", authH.Setup2FAPage)
	r.Get("/setup-2fa/totp", authH.Setup2FATOTP)
	r.Post("/setup-2fa/totp/verify", authH.VerifySetupTOTP)
	r.Get("/verify-2fa", authH.Verify2FAPage)
	r.Post("/verify-2fa", authH.Verify2FA)
	r.Get("/invite/{token}", inviteH.InviteRegisterPage)
	r.Post("/invite/{token}", inviteH.InviteRegister)

	r.Group(func(r chi.Router) {
		r.Use(middleware.AuthMiddleware(sessions, db))
		r.Post("/logout", authH.Logout)
		r.Get("/", dashH.Dashboard)
		r.Get("/account/settings", accountH.AccountSettingsPage)
		r.Post("/account/password", accountH.ChangePassword)
		r.Post("/account/email", accountH.ChangeEmail)
		r.Post("/invitations/{invitationID}/accept", inviteH.AcceptInvitation)
		r.Post("/invitations/{invitationID}/decline", inviteH.DeclineInvitation)
		r.Post("/orgs", orgH.CreateOrg)
		r.Get("/orgs/{orgSlug}", orgH.OrgPage)
		r.Get("/orgs/{orgSlug}/settings", orgH.OrgSettings)
		r.Post("/orgs/{orgSlug}/invitations", orgH.InviteMember)
		r.Delete("/orgs/{orgSlug}/invitations/{invitationID}", inviteH.RevokeInvitation)
		r.Post("/orgs/{orgSlug}/projects", projH.CreateProject)
		r.Get("/orgs/{orgSlug}/projects/{projSlug}/brief", projH.ProjectBrief)
		r.Put("/orgs/{orgSlug}/projects/{projSlug}/brief", projH.UpdateBrief)
		r.Get("/orgs/{orgSlug}/projects/{projSlug}/features", projH.ProjectFeatures)
		r.Get("/orgs/{orgSlug}/projects/{projSlug}/bugs", projH.ProjectBugs)
		r.Get("/orgs/{orgSlug}/projects/{projSlug}/gantt", projH.ProjectGantt)
		r.Post("/tickets", ticketH.CreateTicket)
		r.Get("/tickets/{ticketID}", ticketH.TicketDetail)
		r.Patch("/tickets/{ticketID}/status", ticketH.UpdateStatus)
		r.Patch("/tickets/{ticketID}/agent", ticketH.UpdateAgentMode)
		r.Post("/tickets/{ticketID}/comments", commentH.CreateComment)
		r.Get("/ws/assistant/{projectID}", assistantH.HandleWebSocket)
		r.Delete("/assistant/conversations/{convID}", assistantH.DeleteConversation)
		r.Group(func(r chi.Router) {
			r.Use(middleware.RequireRole(auth.RoleSuperAdmin))
			r.Get("/admin", adminH.AdminPage)
		})
	})

	return r, db, sessions, engine
}

// createAuthenticatedUser creates a fully authenticated user and returns the session cookie.
func createAuthenticatedUser(t *testing.T, db *models.DB, sessions *auth.SessionStore, email, role string) *http.Cookie {
	t.Helper()
	ctx := context.Background()

	hash, _ := auth.HashPassword("TestPassword123!")
	user, err := db.CreateUser(ctx, email, hash, "Test User", role)
	if err != nil {
		t.Fatalf("creating user: %v", err)
	}

	// Mark 2FA as done so user is fully authenticated
	db.Pool.Exec(ctx, `UPDATE users SET must_setup_2fa = false WHERE id = $1`, user.ID)

	req := httptest.NewRequest(http.MethodPost, "/", nil)
	token, err := sessions.CreateSession(ctx, user.ID, false, req)
	if err != nil {
		t.Fatal(err)
	}

	sess, _ := sessions.GetSession(ctx, token)
	sessions.MarkTwoFactorVerified(ctx, sess.ID)

	return &http.Cookie{Name: auth.SessionCookieName, Value: token}
}

func TestLoginPageRenders(t *testing.T) {
	r, _, _, _ := setupTestRouter(t)

	req := httptest.NewRequest(http.MethodGet, "/login", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "Sign in") {
		t.Error("login page should contain 'Sign in'")
	}
}

func TestRegisterPageRenders(t *testing.T) {
	r, _, _, _ := setupTestRouter(t)

	req := httptest.NewRequest(http.MethodGet, "/register", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "Register") {
		t.Error("register page should contain 'Register'")
	}
}

func TestRegisterAndLogin(t *testing.T) {
	r, _, _, _ := setupTestRouter(t)

	// Register (first user = superadmin)
	form := url.Values{"name": {"Admin"}, "email": {"admin@test.com"}, "password": {"SecurePassword123!"}}
	req := httptest.NewRequest(http.MethodPost, "/register", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("register: expected 200, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "Account created") {
		t.Error("should show success message")
	}

	// Login
	loginForm := url.Values{"email": {"admin@test.com"}, "password": {"SecurePassword123!"}}
	req2 := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(loginForm.Encode()))
	req2.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec2 := httptest.NewRecorder()
	r.ServeHTTP(rec2, req2)

	if rec2.Code != http.StatusSeeOther {
		t.Fatalf("login: expected 303, got %d", rec2.Code)
	}

	// First login should redirect to setup-2fa (must_setup_2fa = true)
	loc := rec2.Header().Get("Location")
	if loc != "/setup-2fa" {
		t.Errorf("expected redirect to /setup-2fa, got %s", loc)
	}

	// Should have session cookie
	cookies := rec2.Result().Cookies()
	found := false
	for _, c := range cookies {
		if c.Name == auth.SessionCookieName {
			found = true
			if !c.HttpOnly {
				t.Error("cookie should be HttpOnly")
			}
		}
	}
	if !found {
		t.Error("session cookie should be set")
	}
}

func TestRegisterShortPassword(t *testing.T) {
	r, _, _, _ := setupTestRouter(t)

	form := url.Values{"name": {"Short"}, "email": {"short@test.com"}, "password": {"short"}}
	req := httptest.NewRequest(http.MethodPost, "/register", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if !strings.Contains(rec.Body.String(), "at least 12 characters") {
		t.Error("should show password length error")
	}
}

func TestRegisterMissingFields(t *testing.T) {
	r, _, _, _ := setupTestRouter(t)

	form := url.Values{"name": {""}, "email": {""}, "password": {""}}
	req := httptest.NewRequest(http.MethodPost, "/register", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if !strings.Contains(rec.Body.String(), "All fields are required") {
		t.Error("should show fields required error")
	}
}

func TestLoginWrongPassword(t *testing.T) {
	r, db, _, _ := setupTestRouter(t)
	ctx := context.Background()

	hash, _ := auth.HashPassword("CorrectPassword1")
	db.CreateUser(ctx, "wrong@test.com", hash, "Wrong", "client")

	form := url.Values{"email": {"wrong@test.com"}, "password": {"WrongPassword11"}}
	req := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 (re-render login), got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "Invalid email or password") {
		t.Error("should show error message")
	}
}

func TestLoginNonexistentUser(t *testing.T) {
	r, _, _, _ := setupTestRouter(t)

	form := url.Values{"email": {"ghost@test.com"}, "password": {"doesntmatter1"}}
	req := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if !strings.Contains(rec.Body.String(), "Invalid email or password") {
		t.Error("should show error message")
	}
}

func TestDashboardRequiresAuth(t *testing.T) {
	r, _, _, _ := setupTestRouter(t)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Errorf("expected 303, got %d", rec.Code)
	}
}

func TestDashboardAuthenticated(t *testing.T) {
	r, db, sessions, _ := setupTestRouter(t)
	cookie := createAuthenticatedUser(t, db, sessions, "dash@test.com", "superadmin")

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "Dashboard") {
		t.Error("should contain Dashboard")
	}
}

func TestCreateOrg(t *testing.T) {
	r, db, sessions, _ := setupTestRouter(t)
	cookie := createAuthenticatedUser(t, db, sessions, "org@test.com", "superadmin")

	form := url.Values{"name": {"ACME Corp"}}
	req := httptest.NewRequest(http.MethodPost, "/orgs", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("expected 303, got %d", rec.Code)
	}
	loc := rec.Header().Get("Location")
	if !strings.Contains(loc, "/orgs/acme-corp") {
		t.Errorf("expected redirect to /orgs/acme-corp, got %s", loc)
	}
}

func TestOrgPage(t *testing.T) {
	r, db, sessions, _ := setupTestRouter(t)
	cookie := createAuthenticatedUser(t, db, sessions, "orgpage@test.com", "superadmin")
	ctx := context.Background()

	db.CreateOrg(ctx, "View Org", "view-org")

	req := httptest.NewRequest(http.MethodGet, "/orgs/view-org", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
}

func TestCreateProject(t *testing.T) {
	r, db, sessions, _ := setupTestRouter(t)
	cookie := createAuthenticatedUser(t, db, sessions, "proj@test.com", "superadmin")
	ctx := context.Background()

	db.CreateOrg(ctx, "Proj Org", "proj-org")

	form := url.Values{"name": {"My Project"}}
	req := httptest.NewRequest(http.MethodPost, "/orgs/proj-org/projects", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("expected 303, got %d", rec.Code)
	}
}

func TestProjectBriefPage(t *testing.T) {
	r, db, sessions, _ := setupTestRouter(t)
	cookie := createAuthenticatedUser(t, db, sessions, "brief@test.com", "superadmin")
	ctx := context.Background()

	org, _ := db.CreateOrg(ctx, "Brief Org", "brief-org")
	db.CreateProject(ctx, org.ID, "Brief Project", "brief-proj")

	req := httptest.NewRequest(http.MethodGet, "/orgs/brief-org/projects/brief-proj/brief", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
}

func TestProjectFeaturesPage(t *testing.T) {
	r, db, sessions, _ := setupTestRouter(t)
	cookie := createAuthenticatedUser(t, db, sessions, "feat@test.com", "superadmin")
	ctx := context.Background()

	org, _ := db.CreateOrg(ctx, "Feat Org", "feat-org")
	db.CreateProject(ctx, org.ID, "Feat Project", "feat-proj")

	req := httptest.NewRequest(http.MethodGet, "/orgs/feat-org/projects/feat-proj/features", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
}

func TestProjectBugsPage(t *testing.T) {
	r, db, sessions, _ := setupTestRouter(t)
	cookie := createAuthenticatedUser(t, db, sessions, "bugs@test.com", "superadmin")
	ctx := context.Background()

	org, _ := db.CreateOrg(ctx, "Bugs Org", "bugs-org")
	db.CreateProject(ctx, org.ID, "Bugs Project", "bugs-proj")

	req := httptest.NewRequest(http.MethodGet, "/orgs/bugs-org/projects/bugs-proj/bugs", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
}

func TestProjectGanttPage(t *testing.T) {
	r, db, sessions, _ := setupTestRouter(t)
	cookie := createAuthenticatedUser(t, db, sessions, "gantt@test.com", "superadmin")
	ctx := context.Background()

	org, _ := db.CreateOrg(ctx, "Gantt Org", "gantt-org")
	db.CreateProject(ctx, org.ID, "Gantt Project", "gantt-proj")

	req := httptest.NewRequest(http.MethodGet, "/orgs/gantt-org/projects/gantt-proj/gantt", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
}

func TestCreateTicket(t *testing.T) {
	r, db, sessions, _ := setupTestRouter(t)
	cookie := createAuthenticatedUser(t, db, sessions, "ticket@test.com", "superadmin")
	ctx := context.Background()

	org, _ := db.CreateOrg(ctx, "Ticket Org", "ticket-org")
	proj, _ := db.CreateProject(ctx, org.ID, "Ticket Proj", "ticket-proj")

	form := url.Values{
		"project_id": {proj.ID},
		"title":      {"New Feature"},
		"type":       {"feature"},
		"priority":   {"high"},
	}
	req := httptest.NewRequest(http.MethodPost, "/tickets", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Referer", "/orgs/ticket-org/projects/ticket-proj/features")
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("expected 303, got %d", rec.Code)
	}
}

func TestTicketDetail(t *testing.T) {
	r, db, sessions, _ := setupTestRouter(t)
	cookie := createAuthenticatedUser(t, db, sessions, "detail@test.com", "superadmin")
	ctx := context.Background()

	org, _ := db.CreateOrg(ctx, "Detail Org", "detail-org")
	proj, _ := db.CreateProject(ctx, org.ID, "Detail Proj", "detail-proj")

	user, _ := db.GetUserByEmail(ctx, "detail@test.com")
	ticket := &models.Ticket{
		ProjectID: proj.ID, Type: "task", Title: "Detail Task",
		Status: "backlog", Priority: "medium", CreatedBy: user.ID,
	}
	db.CreateTicket(ctx, ticket)

	req := httptest.NewRequest(http.MethodGet, "/tickets/"+ticket.ID, nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "Detail Task") {
		t.Error("should contain ticket title")
	}
}

func TestUpdateTicketStatus(t *testing.T) {
	r, db, sessions, _ := setupTestRouter(t)
	cookie := createAuthenticatedUser(t, db, sessions, "status@test.com", "superadmin")
	ctx := context.Background()

	org, _ := db.CreateOrg(ctx, "Status Org", "status-org")
	proj, _ := db.CreateProject(ctx, org.ID, "Status Proj", "status-proj")

	user, _ := db.GetUserByEmail(ctx, "status@test.com")
	ticket := &models.Ticket{
		ProjectID: proj.ID, Type: "task", Title: "Status Task",
		Status: "backlog", Priority: "medium", CreatedBy: user.ID,
	}
	db.CreateTicket(ctx, ticket)

	form := url.Values{"status": {"ready"}}
	req := httptest.NewRequest(http.MethodPatch, "/tickets/"+ticket.ID+"/status", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	updated, _ := db.GetTicket(ctx, ticket.ID)
	if updated.Status != "ready" {
		t.Errorf("status = %q, want ready", updated.Status)
	}
}

func TestCreateComment(t *testing.T) {
	r, db, sessions, _ := setupTestRouter(t)
	cookie := createAuthenticatedUser(t, db, sessions, "comment@test.com", "superadmin")
	ctx := context.Background()

	org, _ := db.CreateOrg(ctx, "Comment Org", "comment-org")
	proj, _ := db.CreateProject(ctx, org.ID, "Comment Proj", "comment-proj")

	user, _ := db.GetUserByEmail(ctx, "comment@test.com")
	ticket := &models.Ticket{
		ProjectID: proj.ID, Type: "task", Title: "Comment Task",
		Status: "backlog", Priority: "medium", CreatedBy: user.ID,
	}
	db.CreateTicket(ctx, ticket)

	form := url.Values{"body": {"This is a test comment"}}
	req := httptest.NewRequest(http.MethodPost, "/tickets/"+ticket.ID+"/comments", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	comments, _ := db.ListComments(ctx, ticket.ID)
	if len(comments) != 1 {
		t.Fatalf("expected 1 comment, got %d", len(comments))
	}
}

func TestAdminPageSuperadminOnly(t *testing.T) {
	r, db, sessions, _ := setupTestRouter(t)

	// Client should be forbidden
	clientCookie := createAuthenticatedUser(t, db, sessions, "client@test.com", "client")
	req := httptest.NewRequest(http.MethodGet, "/admin", nil)
	req.AddCookie(clientCookie)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Errorf("client: expected 403, got %d", rec.Code)
	}

	// Superadmin should see admin page
	adminCookie := createAuthenticatedUser(t, db, sessions, "admin@test.com", "superadmin")
	req2 := httptest.NewRequest(http.MethodGet, "/admin", nil)
	req2.AddCookie(adminCookie)
	rec2 := httptest.NewRecorder()
	r.ServeHTTP(rec2, req2)

	if rec2.Code != http.StatusOK {
		t.Fatalf("superadmin: expected 200, got %d", rec2.Code)
	}
	if !strings.Contains(rec2.Body.String(), "Admin Panel") {
		t.Error("should contain Admin Panel")
	}
}

func TestLogout(t *testing.T) {
	r, db, sessions, _ := setupTestRouter(t)
	cookie := createAuthenticatedUser(t, db, sessions, "logout@test.com", "superadmin")

	req := httptest.NewRequest(http.MethodPost, "/logout", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("expected 303, got %d", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "/login" {
		t.Errorf("expected redirect to /login, got %s", loc)
	}
}

func TestSlugify(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"ACME Corp", "acme-corp"},
		{"Hello World!", "hello-world"},
		{"  spaces  ", "spaces"},
		{"already-slugged", "already-slugged"},
		{"MiXeD CaSe 123", "mixed-case-123"},
		{"Special @#$ Chars", "special-chars"},
	}

	for _, tc := range tests {
		t.Run(tc.input, func(t *testing.T) {
			got := slugify(tc.input)
			if got != tc.want {
				t.Errorf("slugify(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

func TestUpdateAgentModeForbiddenForClient(t *testing.T) {
	r, db, sessions, _ := setupTestRouter(t)
	ctx := context.Background()

	cookie := createAuthenticatedUser(t, db, sessions, "agentclient@test.com", "client")

	org, _ := db.CreateOrg(ctx, "Agent Org", "agent-org")
	proj, _ := db.CreateProject(ctx, org.ID, "Agent Proj", "agent-proj")
	user, _ := db.GetUserByEmail(ctx, "agentclient@test.com")
	db.AddOrgMember(ctx, user.ID, org.ID, "member")

	ticket := &models.Ticket{
		ProjectID: proj.ID, Type: "task", Title: "Agent Task",
		Status: "backlog", Priority: "medium", CreatedBy: user.ID,
	}
	db.CreateTicket(ctx, ticket)

	form := url.Values{"agent_mode": {"plan"}, "agent_name": {"claude"}}
	req := httptest.NewRequest(http.MethodPatch, "/tickets/"+ticket.ID+"/agent", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Errorf("expected 403, got %d", rec.Code)
	}
}

func TestOrgSettingsPage(t *testing.T) {
	r, db, sessions, _ := setupTestRouter(t)
	cookie := createAuthenticatedUser(t, db, sessions, "settings@test.com", "superadmin")
	ctx := context.Background()

	db.CreateOrg(ctx, "Settings Org", "settings-org")

	req := httptest.NewRequest(http.MethodGet, "/orgs/settings-org/settings", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "Members") {
		t.Error("should contain Members section")
	}
}
