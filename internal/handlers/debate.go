// Package handlers — Feature Debate Mode (v1).
//
// This file implements the three endpoints landing in Task 7:
//   GET  /tickets/{ticketID}/debate          — ShowDebate
//   POST /tickets/{ticketID}/debate/start    — StartDebate
//   POST /tickets/{ticketID}/debate/rounds   — CreateRound
//
// Tasks 8 and 9 extend this handler with accept/reject/undo/approve/abandon.
// See docs/superpowers/specs/2026-04-14-feature-debate-mode-design.md §4.

package handlers

import (
	"context"
	"errors"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"github.com/madalin/forgedesk/internal/ai"
	"github.com/madalin/forgedesk/internal/auth"
	"github.com/madalin/forgedesk/internal/diff"
	"github.com/madalin/forgedesk/internal/middleware"
	"github.com/madalin/forgedesk/internal/models"
	"github.com/madalin/forgedesk/internal/render"
)

// ticketTypeFeature is the tickets.type value debate mode operates on.
// Centralized here so the literal doesn't repeat across requireDebate
// Context's type check and future authz/routing branches.
const ticketTypeFeature = "feature"

// DebateConfig groups the per-deployment tuning knobs for the debate
// flow. Defaults come from DefaultDebateConfig; production wiring in
// cmd/server/main.go can override from env or leave defaults in place.
type DebateConfig struct {
	ClientRoundCap      int           // per-feature cap for clients (staff/superadmin bypass)
	ClientDailyRoundCap int           // per-user daily safety fuse for clients
	MaxFeedbackLen      int           // max characters accepted in the feedback textarea
	MinOutputLen        int           // minimum AI output length to accept
	StaleReservationAge time.Duration // orphan-recovery threshold for in_flight_request_id
	AICallTimeout       time.Duration // context timeout wrapping every refiner.Refine call
}

// DefaultDebateConfig returns the spec-aligned defaults (§3.3, §6).
func DefaultDebateConfig() DebateConfig {
	return DebateConfig{
		ClientRoundCap:      10,
		ClientDailyRoundCap: 50,
		MaxFeedbackLen:      2000,
		MinOutputLen:        10,
		StaleReservationAge: 90 * time.Second,
		AICallTimeout:       60 * time.Second,
	}
}

// DebateHandler serves the debate flow endpoints. Wired once in
// cmd/server/main.go with the refiner registry (provider name →
// Refiner), the scorer (Gemini in v1), and a config block.
//
// The refiners map may be smaller than 3 when an operator deployment
// is missing an API key — the handler validates the provider name on
// each request and returns 400 "unknown provider" for keys absent from
// the map. Never silently falls back to a different vendor.
type DebateHandler struct {
	db       *models.DB
	engine   *render.Engine
	refiners map[string]ai.Refiner
	scorer   ai.Scorer
	cfg      DebateConfig
}

// NewDebateHandler constructs the handler. refiners are keyed by
// Refiner.Name() ("claude", "gemini", "openai"); the scorer can be
// nil if GEMINI_API_KEY isn't configured (scoring then silently skips
// and the sidebar shows the empty state).
func NewDebateHandler(db *models.DB, engine *render.Engine,
	refiners map[string]ai.Refiner, scorer ai.Scorer, cfg DebateConfig,
) *DebateHandler {
	return &DebateHandler{db: db, engine: engine, refiners: refiners, scorer: scorer, cfg: cfg}
}

// debateContext bundles the three things every endpoint looks up in
// its preamble: the session user, the active org, and the target
// ticket. Encapsulating this saves ~8 lines of boilerplate per handler
// and makes tenant-isolation failures surface as a single error path.
type debateContext struct {
	user   *models.User
	org    *models.Organization
	ticket *models.Ticket
}

// requireDebateContext validates auth + tenant + feature-type in one
// pass. Returns an http status + error for the caller to surface; the
// error discipline table lives in spec §3.3.
func (h *DebateHandler) requireDebateContext(r *http.Request) (debateContext, int, error) {
	user := middleware.GetUser(r)
	if user == nil {
		return debateContext{}, http.StatusUnauthorized, errors.New("not authenticated")
	}

	ticketID := chi.URLParam(r, "ticketID")
	if ticketID == "" {
		return debateContext{}, http.StatusBadRequest, errors.New("missing ticket id")
	}

	var ticket *models.Ticket
	var err error
	if auth.IsStaffOrAbove(user.Role) {
		ticket, err = h.db.GetTicket(r.Context(), ticketID)
	} else {
		org := middleware.GetOrg(r)
		if org == nil {
			return debateContext{}, http.StatusNotFound, errors.New("not found")
		}
		ticket, err = h.db.GetTicketScoped(r.Context(), ticketID, org.ID)
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return debateContext{}, http.StatusNotFound, errors.New("not found")
	}
	if err != nil {
		return debateContext{}, http.StatusInternalServerError, err
	}

	if ticket.Type != ticketTypeFeature {
		// Spec §3.3 — debate is for features only.
		return debateContext{}, http.StatusBadRequest, errors.New("debate is for features only")
	}

	// Resolve the org that owns the ticket's project. For client users,
	// AuthMiddleware guarantees an org context AND GetTicketScoped's
	// WHERE clause already enforces that context equals the ticket's
	// project's org, so middleware.GetOrg is authoritative for clients.
	//
	// For staff, the session's selected org may differ from the
	// ticket's project org (staff can view tickets across orgs). In
	// that case we look up the project's org from the DB so the debate
	// is correctly scoped — otherwise we'd either stamp the debate
	// with the wrong org_id or fail with an empty UUID at insert.
	org := middleware.GetOrg(r)
	if auth.IsStaffOrAbove(user.Role) {
		proj, err := h.db.GetProjectByID(r.Context(), ticket.ProjectID)
		if err != nil {
			return debateContext{}, http.StatusInternalServerError, err
		}
		if org == nil || org.ID != proj.OrgID {
			org, err = h.db.GetOrgByID(r.Context(), proj.OrgID)
			if err != nil {
				return debateContext{}, http.StatusInternalServerError, err
			}
		}
	}
	if org == nil {
		// Client users hit this only if AuthMiddleware regresses; defend
		// against that by refusing rather than proceeding with nil org.
		return debateContext{}, http.StatusBadRequest, errors.New("organization context required")
	}
	return debateContext{user: user, org: org, ticket: ticket}, 0, nil
}

// providerNames returns a stable-order slice of the refiners registered
// on this handler. Used by the template to render the AI-picker
// buttons.
func (h *DebateHandler) providerNames() []string {
	names := make([]string, 0, len(h.refiners))
	// Fixed order so the button layout doesn't shuffle between requests.
	for _, n := range []string{"claude", "gemini", "openai"} {
		if _, ok := h.refiners[n]; ok {
			names = append(names, n)
		}
	}
	return names
}

// ── GET /tickets/{ticketID}/debate ────────────────────────────────

// ShowDebate renders the debate page in one of two states:
//   - no active debate → empty-state with a "Start debate" button (Task 10 UI)
//   - active debate    → full debate timeline + sidebar (Task 10 UI)
//
// Intentionally does NOT create a debate row on visit — spec §4.1
// prohibits "lock-on-visit". The ticket description stays directly
// editable for all other users until someone explicitly POSTs /start.
func (h *DebateHandler) ShowDebate(w http.ResponseWriter, r *http.Request) {
	dctx, code, err := h.requireDebateContext(r)
	if err != nil {
		h.engine.RenderError(w, code, err.Error())
		return
	}

	var deb *models.FeatureDebate
	deb, err = h.db.GetActiveDebate(r.Context(), dctx.ticket.ID)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		h.engine.RenderError(w, http.StatusInternalServerError, "internal error")
		return
	}

	var rounds []models.DebateRound
	if deb != nil {
		rounds, err = h.db.GetDebateRounds(r.Context(), deb.ID)
		if err != nil {
			h.engine.RenderError(w, http.StatusInternalServerError, "internal error")
			return
		}
	}

	_ = h.engine.Render(w, r, "debate.html", render.PageData{
		Title: "Debate — " + dctx.ticket.Title,
		Data: map[string]any{
			"Ticket":    dctx.ticket,
			"Org":       dctx.org,
			"User":      dctx.user,
			"Debate":    deb,
			"Rounds":    rounds,
			"Providers": h.providerNames(),
			"IsStaff":   auth.IsStaffOrAbove(dctx.user.Role),
		},
	})
}

// ── POST /tickets/{ticketID}/debate/start ─────────────────────────

// StartDebate creates the active debate row (idempotent under
// concurrent requests via StartDebate's ON CONFLICT pattern) and
// redirects to the debate page. Only this explicit POST engages the
// IsDebateActive lockout on the ticket's description.
func (h *DebateHandler) StartDebate(w http.ResponseWriter, r *http.Request) {
	dctx, code, err := h.requireDebateContext(r)
	if err != nil {
		h.engine.RenderError(w, code, err.Error())
		return
	}

	if _, err = h.db.StartDebate(r.Context(),
		dctx.ticket.ID, dctx.ticket.ProjectID, dctx.org.ID, dctx.user.ID,
	); err != nil {
		h.engine.RenderError(w, http.StatusInternalServerError, "could not start debate")
		return
	}

	w.Header().Set("Hx-Redirect", "/tickets/"+dctx.ticket.ID+"/debate")
	w.WriteHeader(http.StatusSeeOther)
}

// (projectOrgID helper removed — requireDebateContext now resolves
// org from the ticket's project directly, so there's never an empty
// dctx.org at this point.)

// ── POST /tickets/{ticketID}/debate/rounds ────────────────────────

// CreateRound runs one AI refactoring round per spec §4.2's two-phase
// reservation flow:
//  1. Validate user + caps outside any tx.
//  2. Reservation tx: ReserveInFlight under FOR UPDATE, snapshot
//     current_text, commit (releases lock for the long AI call).
//  3. AI call with AICallTimeout, no DB lock held.
//  4. Output validation — reject empty/short/truncated.
//  5. Insert tx: re-lock debate row, validate snapshot still matches
//     current_text (otherwise 409 stale), insert round, clear
//     in_flight flags, update accumulator, commit.
//  6. Increment project_costs.debate rollup outside any tx.
//
// Returns an HTMX partial on success (the new round card) that Task
// 10's template will render; for now with the skeleton template we
// just return the partial name and round data.
func (h *DebateHandler) CreateRound(w http.ResponseWriter, r *http.Request) {
	dctx, code, err := h.requireDebateContext(r)
	if err != nil {
		h.engine.RenderError(w, code, err.Error())
		return
	}

	// Step 1: parse + validate input outside any tx.
	providerName := r.FormValue("provider")
	refiner, ok := h.refiners[providerName]
	if !ok {
		http.Error(w, "unknown provider", http.StatusBadRequest)
		return
	}
	feedback := strings.TrimSpace(r.FormValue("feedback"))
	if len(feedback) > h.cfg.MaxFeedbackLen {
		http.Error(w, "feedback too long", http.StatusRequestEntityTooLarge)
		return
	}

	deb, err := h.db.GetActiveDebate(r.Context(), dctx.ticket.ID)
	if errors.Is(err, pgx.ErrNoRows) {
		http.Error(w, "no active debate — call /start first", http.StatusBadRequest)
		return
	}
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	// Caps — v1 coarse safety fuse per spec §6. Re-checked implicitly
	// by the debate-row FOR UPDATE in step 2 for same-debate races,
	// but concurrent requests against DIFFERENT debates (same user,
	// different features) can still pass both pre-checks before either
	// inserts. A pg_advisory_xact_lock on the user's ID would close
	// the gap; v1 deliberately doesn't add it since phase-2 issue #64
	// (per-org monthly $ budget) supersedes this fuse anyway, and a
	// buggy script hitting N tickets at once is a rare shape compared
	// to hitting one ticket N times.
	if !auth.IsStaffOrAbove(dctx.user.Role) {
		if roundCount, rcErr := h.db.CountActiveRoundsForDebate(r.Context(), deb.ID); rcErr == nil && roundCount >= h.cfg.ClientRoundCap {
			http.Error(w, "round cap reached for this feature", http.StatusTooManyRequests)
			return
		}
		if dailyCount, dcErr := h.db.CountUserRoundsLast24h(r.Context(), dctx.user.ID); dcErr == nil && dailyCount >= h.cfg.ClientDailyRoundCap {
			http.Error(w, "daily round limit reached — try again tomorrow or ask staff to bypass", http.StatusTooManyRequests)
			return
		}
	}

	// Step 2: reservation tx. Micro-hold on the debate row, commit fast.
	snapshot, err := h.reserveInFlight(r.Context(), deb.ID)
	if err != nil {
		h.writeReservationError(w, err)
		return
	}

	// Step 3: AI call, no DB lock.
	callCtx, cancel := context.WithTimeout(r.Context(), h.cfg.AICallTimeout)
	defer cancel()
	out, err := refiner.Refine(callCtx, ai.RefineInput{
		CurrentText:  snapshot,
		Feedback:     feedback,
		SystemPrompt: "", // adapter falls back to embedded prompt
	})
	if err != nil {
		_ = h.db.ClearInFlight(r.Context(), deb.ID)
		// Log the detailed error server-side for operator triage;
		// surface a generic message to the client so we don't leak
		// API keys, internal paths, or provider response fragments
		// via the error envelope.
		log.Printf("debate CreateRound: refiner %s failed for debate %s: %v",
			refiner.Name(), deb.ID, err)
		http.Error(w, "AI call failed; please retry", http.StatusBadGateway)
		return
	}

	// Step 4: output validation.
	text := strings.TrimSpace(out.Text)
	if text == "" || len(text) < h.cfg.MinOutputLen || out.FinishReason == ai.FinishReasonLength {
		_ = h.db.ClearInFlight(r.Context(), deb.ID)
		http.Error(w, "AI returned invalid or truncated output", http.StatusBadGateway)
		return
	}

	// Step 5: insert tx.
	unified := diff.ComputeUnified(snapshot, text)
	round, centsDelta, err := h.insertRound(r.Context(), models.InsertDebateRoundInput{
		DebateID:     deb.ID,
		Provider:     refiner.Name(),
		Model:        refiner.Model(),
		TriggeredBy:  dctx.user.ID,
		Feedback:     feedback,
		InputText:    snapshot,
		OutputText:   text,
		DiffUnified:  unified,
		InputTokens:  out.Usage.InputTokens,
		OutputTokens: out.Usage.OutputTokens,
		CostMicros:   out.Usage.CostMicros,
	}, snapshot)
	if err != nil {
		_ = h.db.ClearInFlight(r.Context(), deb.ID)
		h.writeInsertError(w, err)
		return
	}

	// Step 6: cost rollup — non-fatal. The canonical per-round data
	// lives on feature_debate_rounds.cost_micros; project_costs is a
	// rollup that can be reconstructed by SUM-ing rounds. We log
	// failures at WARN so operators can spot persistent rollup
	// drift without users seeing errors on successful rounds.
	if err := h.db.IncrementProjectCostCents(r.Context(), dctx.ticket.ProjectID, centsDelta); err != nil {
		log.Printf("debate CreateRound: project_costs rollup failed for debate %s: %v", deb.ID, err)
	}

	_ = h.engine.RenderPartial(w, "debate_round.html", round)
}

// reserveInFlight wraps the reservation-tx boilerplate. Returns
// (snapshot, error) where error is one of ErrDebateNotActive /
// ErrInFlightAIRequest / generic infra error.
func (h *DebateHandler) reserveInFlight(ctx context.Context, debateID string) (string, error) {
	tx, err := h.db.Pool.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	_, snapshot, err := h.db.ReserveInFlight(ctx, tx, debateID, h.cfg.StaleReservationAge)
	if err != nil {
		return "", err
	}
	if commitErr := tx.Commit(ctx); commitErr != nil {
		return "", commitErr
	}
	return snapshot, nil
}

// insertRound wraps the insert-tx boilerplate.
func (h *DebateHandler) insertRound(
	ctx context.Context,
	in models.InsertDebateRoundInput,
	snapshot string,
) (*models.DebateRound, int64, error) {
	tx, err := h.db.Pool.Begin(ctx)
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	round, centsDelta, err := h.db.InsertDebateRoundTx(ctx, tx, in, snapshot)
	if err != nil {
		return nil, 0, err
	}
	if commitErr := tx.Commit(ctx); commitErr != nil {
		return nil, 0, commitErr
	}
	return round, centsDelta, nil
}

// writeReservationError maps ReserveInFlight's sentinel errors to the
// right HTTP status per spec §3.3. Keeps error branches distinct —
// never collapses "not active" / "in flight" / "generic infra" into a
// single path.
func (h *DebateHandler) writeReservationError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, models.ErrDebateNotActive):
		http.Error(w, "debate not active", http.StatusConflict)
	case errors.Is(err, models.ErrInFlightAIRequest):
		http.Error(w, "another AI request is in flight; wait for it", http.StatusConflict)
	default:
		http.Error(w, "internal error", http.StatusInternalServerError)
	}
}

// writeInsertError maps InsertDebateRoundTx's sentinel errors.
func (h *DebateHandler) writeInsertError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, models.ErrStaleAIInput):
		http.Error(w, "feature description changed while AI was processing — please retry", http.StatusConflict)
	case errors.Is(err, models.ErrDebateNotActive):
		http.Error(w, "debate not active", http.StatusConflict)
	case errors.Is(err, models.ErrInReviewRoundExists):
		http.Error(w, "another round is already in review — accept or reject it first", http.StatusConflict)
	default:
		http.Error(w, "internal error", http.StatusInternalServerError)
	}
}
