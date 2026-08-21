package handlers

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/madalin/forgedesk/internal/ai"
	"github.com/madalin/forgedesk/internal/auth"
	"github.com/madalin/forgedesk/internal/middleware"
	"github.com/madalin/forgedesk/internal/models"
	"github.com/madalin/forgedesk/internal/render"
	"github.com/madalin/forgedesk/internal/testutil"
)

// setupDebateTestEnvWithRefiner is setupDebateTestEnv but also returns
// the FakeRefiner so budget-enforcement tests can assert CallCount == 0
// — the only way to prove a blocked round truly made no AI call, rather
// than just inferring it from the round row being absent.
func setupDebateTestEnvWithRefiner(t *testing.T) (*chi.Mux, *models.DB, *auth.SessionStore, *ai.FakeRefiner) {
	t.Helper()

	pool := testutil.SetupTestDB(t)
	db := models.NewDB(pool)
	sessions := auth.NewSessionStore(pool)
	engine, err := render.NewEngine(testutil.ProjectRoot()+"/templates", nil)
	if err != nil {
		t.Fatalf("loading templates: %v", err)
	}

	refiner := &ai.FakeRefiner{
		NameVal: "claude", ModelVal: ai.ModelClaudeSonnet46,
		OutputFunc: func(_ ai.RefineInput) (string, string, error) {
			return "refactored description from claude", ai.FinishReasonStop, nil
		},
	}
	refiners := map[string]ai.Refiner{"claude": refiner}
	h := NewDebateHandler(db, engine, refiners, map[string]ai.Scorer{}, DefaultDebateConfig())

	r := chi.NewRouter()
	r.Use(middleware.Recover)
	r.Group(func(r chi.Router) {
		r.Use(middleware.AuthMiddleware(sessions, db))
		r.Post("/tickets/{ticketID}/debate/start", h.StartDebate)
		r.Post("/tickets/{ticketID}/debate/rounds", h.CreateRound)
	})
	return r, db, sessions, refiner
}

// ── Unit tests: checkOrgBudget in isolation ─────────────────────────

// TestCheckOrgBudget_NilCapAlwaysPasses proves an org with no cap set
// behaves identically to today — success criterion 1 in the spec.
func TestCheckOrgBudget_NilCapAlwaysPasses(t *testing.T) {
	_, db, sessions := setupDebateTestEnv(t)
	ticket, _ := seedAuthedFeatureTicket(t, db, sessions)
	org := loadTicketOrg(t, db, ticket)

	h := NewDebateHandler(db, nil, nil, nil, DefaultDebateConfig())
	if err := h.checkOrgBudget(context.Background(), org); err != nil {
		t.Errorf("checkOrgBudget with nil cap = %v, want nil", err)
	}
}

// TestCheckOrgBudget_UnderCapPasses proves spend strictly below the cap
// does not block.
func TestCheckOrgBudget_UnderCapPasses(t *testing.T) {
	_, db, sessions := setupDebateTestEnv(t)
	ticket, _ := seedAuthedFeatureTicket(t, db, sessions)
	org := loadTicketOrg(t, db, ticket)

	if err := db.UpdateOrgMonthlyBudget(context.Background(), org.ID, ticket.CreatedBy, int64PtrDebate(10_000)); err != nil {
		t.Fatalf("UpdateOrgMonthlyBudget: %v", err)
	}
	org = loadTicketOrg(t, db, ticket)

	h := NewDebateHandler(db, nil, nil, nil, DefaultDebateConfig())
	if err := h.checkOrgBudget(context.Background(), org); err != nil {
		t.Errorf("checkOrgBudget under cap = %v, want nil", err)
	}
}

// TestCheckOrgBudget_AtOrOverCapBlocks proves the boundary is >=, not >
// — the off-by-one a naive implementation would get backwards. Includes
// the "cap 0, spend 0" edge from Debate 2: 0 >= 0 must block.
func TestCheckOrgBudget_AtOrOverCapBlocks(t *testing.T) {
	tests := []struct {
		name        string
		capCents    int64
		spendMicros int64
	}{
		{"exactly at cap", 100, 100 * microsPerCentDebate},
		{"over cap", 100, 150 * microsPerCentDebate},
		{"cap zero, spend zero", 0, 0},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, db, sessions := setupDebateTestEnv(t)
			ticket, _ := seedAuthedFeatureTicket(t, db, sessions)
			org := loadTicketOrg(t, db, ticket)

			if err := db.UpdateOrgMonthlyBudget(context.Background(), org.ID, ticket.CreatedBy, int64PtrDebate(tc.capCents)); err != nil {
				t.Fatalf("UpdateOrgMonthlyBudget: %v", err)
			}
			org = loadTicketOrg(t, db, ticket)

			if tc.spendMicros > 0 {
				seedSpendForTicket(t, db, ticket, tc.spendMicros, time.Now().UTC())
			}

			h := NewDebateHandler(db, nil, nil, nil, DefaultDebateConfig())
			err := h.checkOrgBudget(context.Background(), org)
			if !errors.Is(err, errBudgetExceeded) {
				t.Errorf("checkOrgBudget(cap=%d, spend=%d) = %v, want errBudgetExceeded", tc.capCents, tc.spendMicros, err)
			}
		})
	}
}

// TestCheckOrgBudget_OverflowAtMaxBound proves a cap at the 100M-cent
// bound with large seeded costs still compares correctly and blocks —
// not wrap, not a false 503 (Debate 2's overflow case).
func TestCheckOrgBudget_OverflowAtMaxBound(t *testing.T) {
	_, db, sessions := setupDebateTestEnv(t)
	ticket, _ := seedAuthedFeatureTicket(t, db, sessions)
	org := loadTicketOrg(t, db, ticket)

	const maxCapCents = 100_000_000
	if err := db.UpdateOrgMonthlyBudget(context.Background(), org.ID, ticket.CreatedBy, int64PtrDebate(maxCapCents)); err != nil {
		t.Fatalf("UpdateOrgMonthlyBudget: %v", err)
	}
	org = loadTicketOrg(t, db, ticket)

	// Spend well past the max cap in micros.
	seedSpendForTicket(t, db, ticket, int64(maxCapCents)*microsPerCentDebate+1, time.Now().UTC())

	h := NewDebateHandler(db, nil, nil, nil, DefaultDebateConfig())
	err := h.checkOrgBudget(context.Background(), org)
	if !errors.Is(err, errBudgetExceeded) {
		t.Errorf("checkOrgBudget at max bound with overshoot = %v, want errBudgetExceeded", err)
	}
}

// TestCheckOrgBudget_AggregateFailure_FailsClosed isolates a genuine
// aggregate-read failure: passing an org with a non-UUID ID makes
// SumOrgDebateSpendMicros's query itself error (a real Postgres cast
// failure, not a fabricated one), letting this test exercise the actual
// fail-closed branch instead of asserting it by inspection. A cancelled
// context was tried first and rejected — it fails the earlier ticket/
// auth lookups in a full HTTP round-trip before ever reaching this
// aggregate, so it can't isolate this specific failure mode (plan step 7).
func TestCheckOrgBudget_AggregateFailure_FailsClosed(t *testing.T) {
	_, db, sessions := setupDebateTestEnv(t)
	ticket, _ := seedAuthedFeatureTicket(t, db, sessions)
	org := loadTicketOrg(t, db, ticket)
	if err := db.UpdateOrgMonthlyBudget(context.Background(), org.ID, ticket.CreatedBy, int64PtrDebate(100)); err != nil {
		t.Fatalf("UpdateOrgMonthlyBudget: %v", err)
	}
	org = loadTicketOrg(t, db, ticket)
	org.ID = "not-a-valid-uuid"

	h := NewDebateHandler(db, nil, nil, nil, DefaultDebateConfig())
	err := h.checkOrgBudget(context.Background(), org)
	if !errors.Is(err, errBudgetUnavailable) {
		t.Errorf("checkOrgBudget with unreadable aggregate = %v, want errBudgetUnavailable (fail closed)", err)
	}
}

// ── Helpers ──────────────────────────────────────────────────────────

func int64PtrDebate(v int64) *int64 { return &v }

const microsPerCentDebate = 10_000

func loadTicketOrg(t *testing.T, db *models.DB, ticket *models.Ticket) *models.Organization {
	t.Helper()
	proj, err := db.GetProjectByID(context.Background(), ticket.ProjectID)
	if err != nil {
		t.Fatalf("GetProjectByID: %v", err)
	}
	org, err := db.GetOrgByID(context.Background(), proj.OrgID)
	if err != nil {
		t.Fatalf("GetOrgByID: %v", err)
	}
	return org
}

// seedSpendForTicket starts (or reuses) an active debate on the ticket
// and inserts one raw feature_debate_rounds row carrying the given cost,
// so budget tests can place spend precisely without burning a fake AI
// round through the full CreateRound flow.
func seedSpendForTicket(t *testing.T, db *models.DB, ticket *models.Ticket, costMicros int64, createdAt time.Time) {
	t.Helper()
	ctx := context.Background()

	proj, err := db.GetProjectByID(ctx, ticket.ProjectID)
	if err != nil {
		t.Fatalf("GetProjectByID: %v", err)
	}
	deb, err := db.GetActiveDebate(ctx, ticket.ID)
	if err != nil {
		deb, err = db.StartDebate(ctx, ticket.ID, ticket.ProjectID, proj.OrgID, ticket.CreatedBy)
		if err != nil {
			t.Fatalf("StartDebate: %v", err)
		}
	}

	var roundNum int
	if err := db.Pool.QueryRow(ctx,
		`SELECT COALESCE(MAX(round_number), 0) + 1 FROM feature_debate_rounds WHERE debate_id = $1`,
		deb.ID).Scan(&roundNum); err != nil {
		t.Fatalf("computing next round_number: %v", err)
	}

	if _, err := db.Pool.Exec(ctx,
		`INSERT INTO feature_debate_rounds (
			debate_id, round_number, provider, model, triggered_by,
			input_text, output_text, status, cost_micros, created_at
		) VALUES ($1, $2, 'claude', 'claude-test', $3, 'in', 'out', 'accepted', $4, $5)`,
		deb.ID, roundNum, ticket.CreatedBy, costMicros, createdAt,
	); err != nil {
		t.Fatalf("seeding spend round: %v", err)
	}
}

// ── HTTP-level enforcement tests: CreateRound ───────────────────────

func startDebate(t *testing.T, r *chi.Mux, ticket *models.Ticket, cookie *http.Cookie) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/tickets/"+ticket.ID+"/debate/start", http.NoBody)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("StartDebate: status = %d, body: %s", rec.Code, rec.Body.String())
	}
}

func postCreateRound(t *testing.T, r *chi.Mux, ticket *models.Ticket, cookie *http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	form := url.Values{"provider": {"claude"}, "feedback": {"please improve this feature"}}
	req := httptest.NewRequest(http.MethodPost, "/tickets/"+ticket.ID+"/debate/rounds", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

func countRoundsForTicket(t *testing.T, db *models.DB, ticket *models.Ticket) int {
	t.Helper()
	var count int
	if err := db.Pool.QueryRow(context.Background(),
		`SELECT COUNT(*) FROM feature_debate_rounds r
		   JOIN feature_debates d ON d.id = r.debate_id
		  WHERE d.ticket_id = $1`, ticket.ID).Scan(&count); err != nil {
		t.Fatalf("counting rounds: %v", err)
	}
	return count
}

// TestCreateRound_NoCapSucceeds pins success criterion 1: an org with
// no cap behaves identically to today.
func TestCreateRound_NoCapSucceeds(t *testing.T) {
	r, db, sessions := setupDebateTestEnv(t)
	ticket, cookie := seedAuthedFeatureTicket(t, db, sessions)
	startDebate(t, r, ticket, cookie)

	rec := postCreateRound(t, r, ticket, cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("CreateRound with no cap: status = %d, body: %s", rec.Code, rec.Body.String())
	}
	if got := countRoundsForTicket(t, db, ticket); got != 1 {
		t.Errorf("round count = %d, want 1", got)
	}
}

// TestCreateRound_UnderCapSucceeds is success criterion 2.
func TestCreateRound_UnderCapSucceeds(t *testing.T) {
	r, db, sessions := setupDebateTestEnv(t)
	ticket, cookie := seedAuthedFeatureTicket(t, db, sessions)
	org := loadTicketOrg(t, db, ticket)
	if err := db.UpdateOrgMonthlyBudget(context.Background(), org.ID, ticket.CreatedBy, int64PtrDebate(100_00)); err != nil {
		t.Fatalf("UpdateOrgMonthlyBudget: %v", err)
	}
	startDebate(t, r, ticket, cookie)

	rec := postCreateRound(t, r, ticket, cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("CreateRound under cap: status = %d, body: %s", rec.Code, rec.Body.String())
	}
}

// TestCreateRound_AtOrOverCapBlocks_NoAICall is success criterion 3: at
// or over the monthly cap, CreateRound must 429 with NO AI call made
// (FakeRefiner.CallCount stays 0) and NO round row inserted — the whole
// point of placing this check before the reservation tx.
func TestCreateRound_AtOrOverCapBlocks_NoAICall(t *testing.T) {
	r, db, sessions, refiner := setupDebateTestEnvWithRefiner(t)
	ticket, cookie := seedAuthedFeatureTicket(t, db, sessions)
	org := loadTicketOrg(t, db, ticket)
	if err := db.UpdateOrgMonthlyBudget(context.Background(), org.ID, ticket.CreatedBy, int64PtrDebate(100)); err != nil {
		t.Fatalf("UpdateOrgMonthlyBudget: %v", err)
	}
	startDebate(t, r, ticket, cookie)
	// Pre-existing spend at exactly the cap.
	seedSpendForTicket(t, db, ticket, 100*microsPerCentDebate, time.Now().UTC())

	rec := postCreateRound(t, r, ticket, cookie)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("CreateRound at cap: status = %d, want 429, body: %s", rec.Code, rec.Body.String())
	}
	if refiner.CallCount != 0 {
		t.Errorf("FakeRefiner.CallCount = %d, want 0 — a blocked round must never reach the AI call", refiner.CallCount)
	}
	// Only the one round seeded via seedSpendForTicket must exist — no
	// second row from a round that should never have been created.
	if got := countRoundsForTicket(t, db, ticket); got != 1 {
		t.Errorf("round count after blocked request = %d, want 1 (only the pre-seeded spend row)", got)
	}
}

// TestCreateRound_CapZero_BlocksButOtherActionsStillWork is success
// criterion 4: cap 0 blocks NEW rounds, but StartDebate itself (already
// exercised via startDebate above) and — critically — actions that
// don't spend money must still work. UndoRound/AcceptRound/ApproveDebate
// need an existing round to act on, which CreateRound is specifically
// blocked from producing; StartDebate is the one action from spec §5's
// table this test can exercise without a chicken-and-egg problem, so it
// re-confirms StartDebate succeeds even once the org is fully capped.
func TestCreateRound_CapZero_BlocksButOtherActionsStillWork(t *testing.T) {
	r, db, sessions := setupDebateTestEnv(t)
	ticket, cookie := seedAuthedFeatureTicket(t, db, sessions)
	org := loadTicketOrg(t, db, ticket)
	if err := db.UpdateOrgMonthlyBudget(context.Background(), org.ID, ticket.CreatedBy, int64PtrDebate(0)); err != nil {
		t.Fatalf("UpdateOrgMonthlyBudget: %v", err)
	}

	// StartDebate has no AI call and costs nothing (spec §5) — must
	// succeed even at cap 0.
	startDebate(t, r, ticket, cookie)

	rec := postCreateRound(t, r, ticket, cookie)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("CreateRound at cap 0: status = %d, want 429, body: %s", rec.Code, rec.Body.String())
	}
	if got := countRoundsForTicket(t, db, ticket); got != 0 {
		t.Errorf("round count at cap 0 = %d, want 0", got)
	}
}

// TestCreateRound_Rollover is success criterion 7: a previous month's
// spend must not count toward the current month's cap, and a blocked
// org must unblock at the new month with no intervention.
func TestCreateRound_Rollover(t *testing.T) {
	r, db, sessions := setupDebateTestEnv(t)
	ticket, cookie := seedAuthedFeatureTicket(t, db, sessions)
	org := loadTicketOrg(t, db, ticket)
	if err := db.UpdateOrgMonthlyBudget(context.Background(), org.ID, ticket.CreatedBy, int64PtrDebate(100)); err != nil {
		t.Fatalf("UpdateOrgMonthlyBudget: %v", err)
	}
	startDebate(t, r, ticket, cookie)

	// All of last month's spend, well past the cap — must NOT count.
	lastMonth := time.Now().UTC().AddDate(0, -1, 0)
	seedSpendForTicket(t, db, ticket, 10_000*microsPerCentDebate, lastMonth)

	rec := postCreateRound(t, r, ticket, cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("CreateRound with only prior-month spend: status = %d, want 200, body: %s", rec.Code, rec.Body.String())
	}
}

// TestCreateRound_ScorerCostCountsTowardCap is success criterion 9:
// scorer_cost_micros must count toward the aggregate even though the
// scorer never creates a round of its own.
func TestCreateRound_ScorerCostCountsTowardCap(t *testing.T) {
	r, db, sessions := setupDebateTestEnv(t)
	ticket, cookie := seedAuthedFeatureTicket(t, db, sessions)
	org := loadTicketOrg(t, db, ticket)
	if err := db.UpdateOrgMonthlyBudget(context.Background(), org.ID, ticket.CreatedBy, int64PtrDebate(100)); err != nil {
		t.Fatalf("UpdateOrgMonthlyBudget: %v", err)
	}
	startDebate(t, r, ticket, cookie)

	deb, err := db.GetActiveDebate(context.Background(), ticket.ID)
	if err != nil {
		t.Fatalf("GetActiveDebate: %v", err)
	}
	// Zero refiner cost, ALL of it scorer cost — this must still trip
	// the cap, proving the aggregate isn't refiner-cost-only.
	if _, err := db.Pool.Exec(context.Background(),
		`INSERT INTO feature_debate_rounds (
			debate_id, round_number, provider, model, triggered_by,
			input_text, output_text, status, cost_micros, scorer_cost_micros, created_at
		) VALUES ($1, 1, 'claude', 'claude-test', $2, 'in', 'out', 'accepted', 0, $3, now())`,
		deb.ID, ticket.CreatedBy, 100*microsPerCentDebate,
	); err != nil {
		t.Fatalf("seeding scorer-cost round: %v", err)
	}

	rec := postCreateRound(t, r, ticket, cookie)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("CreateRound with scorer-only spend at cap: status = %d, want 429, body: %s", rec.Code, rec.Body.String())
	}
}

// TestCreateRound_RoleAppropriateWording is success criterion 8 (role
// wording): a client sees no-AI-internals copy, an owner/admin/staff
// sees actionable copy naming the setting.
//
// seedAuthedFeatureTicket makes its user the org OWNER, so the
// client-wording case needs a direct role downgrade to 'member' rather
// than a fresh seed (plan step 7 test-plan note).
func TestCreateRound_RoleAppropriateWording(t *testing.T) {
	t.Run("member sees client wording", func(t *testing.T) {
		r, db, sessions := setupDebateTestEnv(t)
		ticket, cookie := seedAuthedFeatureTicket(t, db, sessions)
		org := loadTicketOrg(t, db, ticket)
		if _, err := db.Pool.Exec(context.Background(),
			`UPDATE org_memberships SET role = 'member' WHERE user_id = $1 AND org_id = $2`,
			ticket.CreatedBy, org.ID); err != nil {
			t.Fatalf("downgrading role to member: %v", err)
		}
		if err := db.UpdateOrgMonthlyBudget(context.Background(), org.ID, ticket.CreatedBy, int64PtrDebate(0)); err != nil {
			t.Fatalf("UpdateOrgMonthlyBudget: %v", err)
		}
		startDebate(t, r, ticket, cookie)

		rec := postCreateRound(t, r, ticket, cookie)
		if rec.Code != http.StatusTooManyRequests {
			t.Fatalf("status = %d, want 429", rec.Code)
		}
		body := rec.Body.String()
		if !strings.Contains(body, "resets at the start of next month") {
			t.Errorf("member wording missing client-safe copy, got: %q", body)
		}
		if strings.Contains(body, "organization settings") {
			t.Errorf("member wording must not tell a non-manager to change settings, got: %q", body)
		}
	})

	t.Run("owner sees actionable wording", func(t *testing.T) {
		r, db, sessions := setupDebateTestEnv(t)
		ticket, cookie := seedAuthedFeatureTicket(t, db, sessions)
		org := loadTicketOrg(t, db, ticket)
		if err := db.UpdateOrgMonthlyBudget(context.Background(), org.ID, ticket.CreatedBy, int64PtrDebate(0)); err != nil {
			t.Fatalf("UpdateOrgMonthlyBudget: %v", err)
		}
		startDebate(t, r, ticket, cookie)

		rec := postCreateRound(t, r, ticket, cookie)
		if rec.Code != http.StatusTooManyRequests {
			t.Fatalf("status = %d, want 429", rec.Code)
		}
		body := rec.Body.String()
		if !strings.Contains(body, "organization settings") {
			t.Errorf("owner wording should point at organization settings, got: %q", body)
		}
	})

	t.Run("staff sees actionable wording on a foreign org", func(t *testing.T) {
		r, db, sessions := setupDebateTestEnv(t)
		ticket, _ := seedAuthedFeatureTicket(t, db, sessions)
		org := loadTicketOrg(t, db, ticket)
		if err := db.UpdateOrgMonthlyBudget(context.Background(), org.ID, ticket.CreatedBy, int64PtrDebate(0)); err != nil {
			t.Fatalf("UpdateOrgMonthlyBudget: %v", err)
		}

		staffHash, _ := auth.HashPassword("TestPassword123!")
		staffUser, err := db.CreateUser(context.Background(), "staff-wording@example.com", staffHash, "Staff", "staff")
		if err != nil {
			t.Fatalf("CreateUser: %v", err)
		}
		if _, execErr := db.Pool.Exec(context.Background(),
			`UPDATE users SET must_setup_2fa = false WHERE id = $1`, staffUser.ID); execErr != nil {
			t.Fatalf("clearing must_setup_2fa: %v", execErr)
		}
		req := httptest.NewRequest(http.MethodPost, "/", http.NoBody)
		token, err := sessions.CreateSession(context.Background(), staffUser.ID, false, req)
		if err != nil {
			t.Fatalf("CreateSession: %v", err)
		}
		sess, err := sessions.GetSession(context.Background(), token)
		if err != nil {
			t.Fatalf("GetSession: %v", err)
		}
		if err := sessions.MarkTwoFactorVerified(context.Background(), sess.ID); err != nil {
			t.Fatalf("MarkTwoFactorVerified: %v", err)
		}
		if err := sessions.SetSelectedOrg(context.Background(), sess.ID, org.ID); err != nil {
			t.Fatalf("SetSelectedOrg: %v", err)
		}
		staffCookie := &http.Cookie{Name: auth.SessionCookieName, Value: token, HttpOnly: true, Secure: true}

		startDebate(t, r, ticket, staffCookie)
		rec := postCreateRound(t, r, ticket, staffCookie)
		if rec.Code != http.StatusTooManyRequests {
			t.Fatalf("status = %d, want 429, body: %s", rec.Code, rec.Body.String())
		}
		body := rec.Body.String()
		if !strings.Contains(body, "organization settings") {
			t.Errorf("staff wording should point at organization settings, got: %q", body)
		}
	})
}
