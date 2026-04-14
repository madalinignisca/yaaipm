package handlers

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
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

// setupDebateTestEnv builds a minimal router with just the three
// debate endpoints wired, using a FakeRefiner so tests don't burn a
// network round-trip to any AI vendor. Exercises the handler's auth /
// tenant / validation paths end-to-end through real middleware and DB.
func setupDebateTestEnv(t *testing.T) (*chi.Mux, *models.DB, *auth.SessionStore) {
	t.Helper()

	pool := testutil.SetupTestDB(t)
	db := models.NewDB(pool)
	sessions := auth.NewSessionStore(pool)
	engine, err := render.NewEngine(testutil.ProjectRoot()+"/templates", nil)
	if err != nil {
		t.Fatalf("loading templates: %v", err)
	}

	refiners := map[string]ai.Refiner{
		"claude": &ai.FakeRefiner{
			NameVal: "claude", ModelVal: ai.ModelClaudeSonnet46,
			OutputFunc: func(_ ai.RefineInput) (string, string, error) {
				return "refactored description from claude", ai.FinishReasonStop, nil
			},
		},
	}
	h := NewDebateHandler(db, engine, refiners, nil, DefaultDebateConfig())

	r := chi.NewRouter()
	r.Use(middleware.Recover)
	r.Group(func(r chi.Router) {
		r.Use(middleware.AuthMiddleware(sessions, db))
		r.Get("/tickets/{ticketID}/debate", h.ShowDebate)
		r.Post("/tickets/{ticketID}/debate/start", h.StartDebate)
		r.Post("/tickets/{ticketID}/debate/rounds", h.CreateRound)
		r.Post("/tickets/{ticketID}/debate/rounds/{roundID}/accept", h.AcceptRound)
		r.Post("/tickets/{ticketID}/debate/rounds/{roundID}/reject", h.RejectRound)
		r.Post("/tickets/{ticketID}/debate/undo", h.UndoRound)
	})
	return r, db, sessions
}

// seedAuthedFeatureTicket creates the full chain — user → session →
// org → project → feature ticket — needed to hit a debate endpoint as
// an authenticated user. Returns the ticket plus a session cookie
// that the caller attaches to their request.
//
// The ticket's ProjectID and Title are populated via CreateTicket's
// RETURNING so the caller can build URLs without an extra lookup.
func seedAuthedFeatureTicket(t *testing.T, db *models.DB, sessions *auth.SessionStore) (*models.Ticket, *http.Cookie) {
	t.Helper()
	ctx := context.Background()

	hash, _ := auth.HashPassword("TestPassword123!")
	user, err := db.CreateUser(ctx, t.Name()+"@example.com", hash, "Debate Test User", "client")
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	if _, execErr := db.Pool.Exec(ctx,
		`UPDATE users SET must_setup_2fa = false WHERE id = $1`, user.ID); execErr != nil {
		t.Fatalf("clearing must_setup_2fa: %v", execErr)
	}

	org, err := db.CreateOrgWithOwnerTx(ctx, user.ID,
		"Org "+t.Name(), "org-"+t.Name(), models.OrgRoleOwner)
	if err != nil {
		t.Fatalf("CreateOrgWithOwnerTx: %v", err)
	}
	proj, err := db.CreateProject(ctx, org.ID, "P", "proj-"+t.Name())
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	ticket := &models.Ticket{
		ProjectID: proj.ID, Type: "feature",
		Title: "Feature", DescriptionMarkdown: "Initial description",
		Status: "backlog", Priority: "medium", CreatedBy: user.ID,
	}
	if ctErr := db.CreateTicket(ctx, ticket); ctErr != nil {
		t.Fatalf("CreateTicket: %v", ctErr)
	}

	req := httptest.NewRequest(http.MethodPost, "/", http.NoBody)
	token, err := sessions.CreateSession(ctx, user.ID, false, req)
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	sess, err := sessions.GetSession(ctx, token)
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if err := sessions.MarkTwoFactorVerified(ctx, sess.ID); err != nil {
		t.Fatalf("MarkTwoFactorVerified: %v", err)
	}
	if err := sessions.SetSelectedOrg(ctx, sess.ID, org.ID); err != nil {
		t.Fatalf("SetSelectedOrg: %v", err)
	}

	// HttpOnly/Secure are meaningless to httptest's cookie jar but set
	// here to silence "sensitive cookie without HttpOnly/Secure flag"
	// static-analysis rules — matches the pattern in handlers_test.go.
	return ticket, &http.Cookie{
		Name: auth.SessionCookieName, Value: token, HttpOnly: true, Secure: true,
	}
}

func TestStartDebate_CreatesActiveRowAndRedirects(t *testing.T) {
	r, db, sessions := setupDebateTestEnv(t)
	ticket, cookie := seedAuthedFeatureTicket(t, db, sessions)

	req := httptest.NewRequest(http.MethodPost,
		"/tickets/"+ticket.ID+"/debate/start", http.NoBody)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Errorf("status = %d, want %d; body = %q", rec.Code, http.StatusSeeOther, rec.Body.String())
	}
	if got := rec.Header().Get("Hx-Redirect"); got != "/tickets/"+ticket.ID+"/debate" {
		t.Errorf("Hx-Redirect = %q, want /tickets/%s/debate", got, ticket.ID)
	}

	deb, err := db.GetActiveDebate(context.Background(), ticket.ID)
	if err != nil {
		t.Fatalf("no active debate after StartDebate: %v", err)
	}
	if deb.Status != "active" {
		t.Errorf("status = %q, want active", deb.Status)
	}
	if deb.SeedDescription != "Initial description" {
		t.Errorf("seed = %q, want %q", deb.SeedDescription, "Initial description")
	}
}

func TestShowDebate_NoActiveReturnsEmptyState(t *testing.T) {
	r, db, sessions := setupDebateTestEnv(t)
	ticket, cookie := seedAuthedFeatureTicket(t, db, sessions)

	req := httptest.NewRequest(http.MethodGet, "/tickets/"+ticket.ID+"/debate", http.NoBody)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200; body first 200 chars = %q", rec.Code, rec.Body.String()[:min(200, rec.Body.Len())])
	}
	// Skeleton template emits "No active debate" in the empty state.
	// Task 10 replaces that with richer UI but keeps the marker.
	if !bytes.Contains(rec.Body.Bytes(), []byte("No active debate")) {
		t.Error("empty-state marker missing from response body")
	}
	// Visit must NOT create a row (spec §4.1 lock-on-visit prevention).
	if _, err := db.GetActiveDebate(context.Background(), ticket.ID); err == nil {
		t.Error("GET /debate created a row; lock-on-visit prevention regressed")
	}
}

func TestDebate_RejectsNonFeatureTicket(t *testing.T) {
	r, db, sessions := setupDebateTestEnv(t)
	ctx := context.Background()

	hash, _ := auth.HashPassword("TestPassword123!")
	user, err := db.CreateUser(ctx, "bug-owner@example.com", hash, "Bug Owner", "client")
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	if _, execErr := db.Pool.Exec(ctx, `UPDATE users SET must_setup_2fa = false WHERE id = $1`, user.ID); execErr != nil {
		t.Fatalf("clearing 2fa: %v", execErr)
	}
	org, err := db.CreateOrgWithOwnerTx(ctx, user.ID, "Bug Org", "bug-org", models.OrgRoleOwner)
	if err != nil {
		t.Fatalf("CreateOrgWithOwnerTx: %v", err)
	}
	proj, err := db.CreateProject(ctx, org.ID, "P", "bug-proj")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	bug := &models.Ticket{
		ProjectID: proj.ID, Type: "bug", Title: "A bug", DescriptionMarkdown: "x",
		Status: "backlog", Priority: "medium", CreatedBy: user.ID,
	}
	if ctErr := db.CreateTicket(ctx, bug); ctErr != nil {
		t.Fatalf("CreateTicket: %v", ctErr)
	}
	req0 := httptest.NewRequest(http.MethodPost, "/", http.NoBody)
	token, err := sessions.CreateSession(ctx, user.ID, false, req0)
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	sess, _ := sessions.GetSession(ctx, token)
	_ = sessions.MarkTwoFactorVerified(ctx, sess.ID)
	_ = sessions.SetSelectedOrg(ctx, sess.ID, org.ID)
	cookie := &http.Cookie{
		Name: auth.SessionCookieName, Value: token, HttpOnly: true, Secure: true,
	}

	req := httptest.NewRequest(http.MethodPost, "/tickets/"+bug.ID+"/debate/start", http.NoBody)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400; body = %q", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "features only") {
		t.Errorf("expected 'features only', got: %q", rec.Body.String())
	}
}

func TestCreateRound_RejectsMissingActiveDebate(t *testing.T) {
	r, db, sessions := setupDebateTestEnv(t)
	ticket, cookie := seedAuthedFeatureTicket(t, db, sessions)

	// Skip /start — try to create a round with no active debate.
	req := httptest.NewRequest(http.MethodPost, "/tickets/"+ticket.ID+"/debate/rounds",
		strings.NewReader("provider=claude"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400; body = %q", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "no active debate") {
		t.Errorf("expected 'no active debate', got: %q", rec.Body.String())
	}
}

func TestCreateRound_RejectsUnknownProvider(t *testing.T) {
	r, db, sessions := setupDebateTestEnv(t)
	ticket, cookie := seedAuthedFeatureTicket(t, db, sessions)

	// Need an active debate to reach the provider check.
	// Issue /start via the router so state matches what a real client
	// would see, rather than calling db.StartDebate directly.
	startReq := httptest.NewRequest(http.MethodPost, "/tickets/"+ticket.ID+"/debate/start", http.NoBody)
	startReq.AddCookie(cookie)
	startRec := httptest.NewRecorder()
	r.ServeHTTP(startRec, startReq)
	if startRec.Code != http.StatusSeeOther {
		t.Fatalf("setup: /start failed: %d %s", startRec.Code, startRec.Body.String())
	}

	req := httptest.NewRequest(http.MethodPost, "/tickets/"+ticket.ID+"/debate/rounds",
		strings.NewReader("provider=mistral")) // not registered
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400; body = %q", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "unknown provider") {
		t.Errorf("expected 'unknown provider', got: %q", rec.Body.String())
	}
}

// startAndCreateRounds runs /start and then N round-creation requests
// via the fake claude refiner, each auto-accepted. Returns the
// accepted round records in creation order (length N). Used by
// accept / reject / undo tests below.
func startAndCreateRounds(t *testing.T, r *chi.Mux, db *models.DB, cookie *http.Cookie, ticketID string, outputs []string) []*models.DebateRound {
	t.Helper()
	ctx := context.Background()

	// /start
	startRec := httptest.NewRecorder()
	startReq := httptest.NewRequest(http.MethodPost, "/tickets/"+ticketID+"/debate/start", http.NoBody)
	startReq.AddCookie(cookie)
	r.ServeHTTP(startRec, startReq)
	if startRec.Code != http.StatusSeeOther {
		t.Fatalf("/start: %d %s", startRec.Code, startRec.Body.String())
	}

	deb, err := db.GetActiveDebate(ctx, ticketID)
	if err != nil {
		t.Fatalf("GetActiveDebate: %v", err)
	}

	// The default fake refiner returns a constant string; we need to
	// vary output per round so cascading-undo tests can distinguish
	// rounds. Reach into the handler's refiner registry via the env
	// we built in setupDebateTestEnv — but the test doesn't expose
	// it. Instead: create rounds one at a time, manually updating the
	// FakeRefiner's OutputFunc between calls via a closure variable.
	// Simpler: use db-level shortcuts since the fake refiner's input
	// doesn't vary meaningfully per round in these tests.
	//
	// For tests that DO need unique output per round, skip the HTTP
	// /rounds path and insert rounds + accept them directly via
	// AcceptRoundTx. That's what this helper does — it's an
	// internal/testing-only shortcut, not a reflection of production
	// flow (where the only way to create a round is through /rounds).
	var accepted []*models.DebateRound
	for i, out := range outputs {
		currentText := deb.SeedDescription
		if i > 0 {
			currentText = outputs[i-1]
		}
		// Insert an in_review round under a short tx (re-using
		// InsertDebateRoundTx against a fresh tx lock).
		tx, err := db.Pool.Begin(ctx)
		if err != nil {
			t.Fatalf("Begin: %v", err)
		}
		round, _, err := db.InsertDebateRoundTx(ctx, tx, models.InsertDebateRoundInput{
			DebateID:    deb.ID,
			Provider:    "claude",
			Model:       ai.ModelClaudeSonnet46,
			TriggeredBy: deb.StartedBy,
			InputText:   currentText,
			OutputText:  out,
			CostMicros:  0,
		}, currentText)
		if err != nil {
			_ = tx.Rollback(ctx)
			t.Fatalf("InsertDebateRoundTx: %v", err)
		}
		if commitErr := tx.Commit(ctx); commitErr != nil {
			t.Fatalf("Commit: %v", commitErr)
		}

		// Accept via the HTTP handler so we exercise the production
		// tx path including the FOR UPDATE status check.
		acceptRec := httptest.NewRecorder()
		acceptReq := httptest.NewRequest(http.MethodPost,
			"/tickets/"+ticketID+"/debate/rounds/"+round.ID+"/accept", http.NoBody)
		acceptReq.AddCookie(cookie)
		r.ServeHTTP(acceptRec, acceptReq)
		if acceptRec.Code != http.StatusOK {
			t.Fatalf("/accept round %d: %d %s", i+1, acceptRec.Code, acceptRec.Body.String())
		}
		// Re-fetch the round to get its updated (accepted) state.
		rounds, err := db.GetDebateRounds(ctx, deb.ID)
		if err != nil {
			t.Fatalf("GetDebateRounds: %v", err)
		}
		for j := range rounds {
			if rounds[j].ID == round.ID {
				accepted = append(accepted, &rounds[j])
				break
			}
		}
	}
	return accepted
}

func TestAcceptRound_UpdatesCurrentText(t *testing.T) {
	r, db, sessions := setupDebateTestEnv(t)
	ticket, cookie := seedAuthedFeatureTicket(t, db, sessions)
	startAndCreateRounds(t, r, db, cookie, ticket.ID, []string{"round 1 output"})

	deb, err := db.GetActiveDebate(context.Background(), ticket.ID)
	if err != nil {
		t.Fatalf("GetActiveDebate: %v", err)
	}
	if deb.CurrentText != "round 1 output" {
		t.Errorf("current_text = %q, want %q", deb.CurrentText, "round 1 output")
	}
}

func TestRejectRound_LeavesCurrentTextUnchanged(t *testing.T) {
	r, db, sessions := setupDebateTestEnv(t)
	ticket, cookie := seedAuthedFeatureTicket(t, db, sessions)
	ctx := context.Background()

	// /start to open a debate.
	startRec := httptest.NewRecorder()
	startReq := httptest.NewRequest(http.MethodPost,
		"/tickets/"+ticket.ID+"/debate/start", http.NoBody)
	startReq.AddCookie(cookie)
	r.ServeHTTP(startRec, startReq)

	deb, _ := db.GetActiveDebate(ctx, ticket.ID)
	originalText := deb.CurrentText

	// Manually insert an in_review round.
	tx, _ := db.Pool.Begin(ctx)
	round, _, err := db.InsertDebateRoundTx(ctx, tx, models.InsertDebateRoundInput{
		DebateID:    deb.ID,
		Provider:    "claude",
		Model:       ai.ModelClaudeSonnet46,
		TriggeredBy: deb.StartedBy,
		InputText:   originalText,
		OutputText:  "a rejected draft",
	}, originalText)
	if err != nil {
		t.Fatalf("InsertDebateRoundTx: %v", err)
	}
	_ = tx.Commit(ctx)

	rejectRec := httptest.NewRecorder()
	rejectReq := httptest.NewRequest(http.MethodPost,
		"/tickets/"+ticket.ID+"/debate/rounds/"+round.ID+"/reject", http.NoBody)
	rejectReq.AddCookie(cookie)
	r.ServeHTTP(rejectRec, rejectReq)

	if rejectRec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200; body = %q", rejectRec.Code, rejectRec.Body.String())
	}
	debReloaded, _ := db.GetActiveDebate(ctx, ticket.ID)
	if debReloaded.CurrentText != originalText {
		t.Errorf("reject changed current_text: %q → %q", originalText, debReloaded.CurrentText)
	}
}

func TestUndoRound_CascadesLaterRoundsAndResetsEffort(t *testing.T) {
	r, db, sessions := setupDebateTestEnv(t)
	ticket, cookie := seedAuthedFeatureTicket(t, db, sessions)
	ctx := context.Background()

	// 3 accepted rounds.
	rounds := startAndCreateRounds(t, r, db, cookie, ticket.ID,
		[]string{"round 1 out", "round 2 out", "round 3 out"})
	if len(rounds) != 3 {
		t.Fatalf("expected 3 accepted rounds, got %d", len(rounds))
	}

	// Manually set effort fields so we can verify they get cleared.
	_, err := db.Pool.Exec(ctx, `
		UPDATE feature_debates
		   SET effort_score = 7, effort_hours = 10, effort_reasoning = 'test',
		       effort_scored_at = now(), last_scored_round_id = $1
		 WHERE ticket_id = $2`, rounds[2].ID, ticket.ID)
	if err != nil {
		t.Fatalf("seeding effort fields: %v", err)
	}

	// Undo from round 2 — should delete rounds 2 and 3, leave round 1.
	undoRec := httptest.NewRecorder()
	undoReq := httptest.NewRequest(http.MethodPost,
		"/tickets/"+ticket.ID+"/debate/undo?from=2", http.NoBody)
	undoReq.AddCookie(cookie)
	r.ServeHTTP(undoRec, undoReq)

	if undoRec.Code != http.StatusSeeOther {
		t.Errorf("undo status = %d, want %d", undoRec.Code, http.StatusSeeOther)
	}

	deb, _ := db.GetActiveDebate(ctx, ticket.ID)
	if deb.CurrentText != "round 1 out" {
		t.Errorf("current_text = %q, want 'round 1 out'", deb.CurrentText)
	}
	remaining, _ := db.GetDebateRounds(ctx, deb.ID)
	if len(remaining) != 1 {
		t.Errorf("remaining rounds = %d, want 1", len(remaining))
	}
	if deb.EffortScore != nil {
		t.Errorf("effort_score should be nil after undo, got %d", *deb.EffortScore)
	}
	if deb.LastScoredRoundID != nil {
		t.Errorf("last_scored_round_id should be nil after undo")
	}
}

func TestUndoRound_AllRoundsFallsBackToSeed(t *testing.T) {
	r, db, sessions := setupDebateTestEnv(t)
	ticket, cookie := seedAuthedFeatureTicket(t, db, sessions)
	startAndCreateRounds(t, r, db, cookie, ticket.ID,
		[]string{"round 1", "round 2"})

	undoRec := httptest.NewRecorder()
	undoReq := httptest.NewRequest(http.MethodPost,
		"/tickets/"+ticket.ID+"/debate/undo?from=1", http.NoBody)
	undoReq.AddCookie(cookie)
	r.ServeHTTP(undoRec, undoReq)

	if undoRec.Code != http.StatusSeeOther {
		t.Errorf("status = %d, want 303; body = %q", undoRec.Code, undoRec.Body.String())
	}
	deb, _ := db.GetActiveDebate(context.Background(), ticket.ID)
	if deb.CurrentText != deb.SeedDescription {
		t.Errorf("current_text should fall back to seed after full undo: got %q, want %q",
			deb.CurrentText, deb.SeedDescription)
	}
}

func TestUndoRound_RejectsInvalidFrom(t *testing.T) {
	r, db, sessions := setupDebateTestEnv(t)
	ticket, cookie := seedAuthedFeatureTicket(t, db, sessions)

	// Start a debate first so the /undo handler reaches its query-param check.
	startReq := httptest.NewRequest(http.MethodPost,
		"/tickets/"+ticket.ID+"/debate/start", http.NoBody)
	startReq.AddCookie(cookie)
	r.ServeHTTP(httptest.NewRecorder(), startReq)

	for _, qs := range []string{"", "from=0", "from=abc"} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost,
			"/tickets/"+ticket.ID+"/debate/undo?"+qs, http.NoBody)
		req.AddCookie(cookie)
		r.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("qs=%q: status = %d, want 400", qs, rec.Code)
		}
	}
}
