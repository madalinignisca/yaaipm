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
	"strconv"
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

// Client-facing error copy shared across debate handlers (UI refactor
// spec §5.4). Centralized so wording stays consistent across the ~20
// call sites.
const (
	debateMsgStale = "This page is out of date — reload to see the latest state."
	debateMsgInfra = "Something went wrong on our side — nothing was changed."
)

// DebateConfig groups the per-deployment tuning knobs for the debate
// flow. Defaults come from DefaultDebateConfig; production wiring in
// cmd/server/main.go can override from env or leave defaults in place.
type DebateConfig struct {
	ClientRoundCap      int           // per-feature cap for clients (staff/superadmin bypass)
	ClientDailyRoundCap int           // per-user daily safety fuse for clients
	MaxFeedbackLen      int           // max characters accepted in the feedback textarea
	MaxTextLen          int           // max characters for seed/description text (seed/description size cap)
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
		MaxTextLen:          20000,
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

	// Load the ticket's project so PageData.ActiveProject is set — the
	// base layout uses {{.User}}/{{.Org}}/{{.Projects}}/{{.ActiveProject}}
	// to render the authenticated sidebar/nav. Without these fields
	// populated, the layout falls back to the auth-card shell (the one
	// used for /login, /verify-2fa) and the user loses the app chrome
	// entirely.
	proj, err := h.db.GetProjectByID(r.Context(), dctx.ticket.ProjectID)
	if err != nil {
		h.engine.RenderError(w, http.StatusInternalServerError, "internal error")
		return
	}

	var view DebateView
	var chip EffortChipView
	if deb != nil {
		view = buildDebateView(deb, rounds, auth.IsStaffOrAbove(dctx.user.Role))
		chip = buildEffortChipView(deb, rounds, dctx.ticket.ID, time.Now(), h.cfg.StaleReservationAge)
	}

	_ = h.engine.Render(w, r, "debate.html", render.PageData{
		Title:         "Refine — " + dctx.ticket.Title,
		User:          dctx.user,
		Org:           dctx.org,
		Orgs:          middleware.GetOrgs(r),
		Projects:      middleware.GetProjects(r),
		ActiveProject: proj,
		ProjectID:     dctx.ticket.ProjectID,
		CurrentPath:   r.URL.Path,
		Data: map[string]any{
			"Ticket":    dctx.ticket,
			"Org":       dctx.org,
			"User":      dctx.user,
			"Debate":    deb,
			"Rounds":    rounds,
			"View":      view,
			"Chip":      chip,
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
		h.renderDebateError(w, r, http.StatusBadRequest, "Unknown AI provider.")
		return
	}
	feedback := strings.TrimSpace(r.FormValue("feedback"))
	if len(feedback) > h.cfg.MaxFeedbackLen {
		h.renderDebateError(w, r, http.StatusRequestEntityTooLarge,
			"Your feedback is too long — please shorten it.")
		return
	}

	deb, err := h.db.GetActiveDebate(r.Context(), dctx.ticket.ID)
	if errors.Is(err, pgx.ErrNoRows) {
		h.renderDebateError(w, r, http.StatusBadRequest,
			"No refining session is active — start one first.")
		return
	}
	if err != nil {
		h.renderDebateError(w, r, http.StatusInternalServerError,
			debateMsgInfra)
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
			h.renderDebateError(w, r, http.StatusTooManyRequests,
				"You've reached the suggestion limit for this feature — ask us if you need more.")
			return
		}
		if dailyCount, dcErr := h.db.CountUserRoundsLast24h(r.Context(), dctx.user.ID); dcErr == nil && dailyCount >= h.cfg.ClientDailyRoundCap {
			h.renderDebateError(w, r, http.StatusTooManyRequests,
				"You've reached the daily suggestion limit — try again tomorrow or ask us if you need more.")
			return
		}
	}

	// Step 2: reservation tx. Micro-hold on the debate row, commit fast.
	snapshot, err := h.reserveInFlight(r.Context(), deb.ID)
	if err != nil {
		h.writeReservationError(w, r, err)
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
		// context.WithoutCancel so a client disconnect between
		// ReserveInFlight success and this cleanup doesn't leak a
		// set in_flight_request_id. The stale-recovery window in
		// ReserveInFlight (StaleReservationAge, 90s) is the
		// fallback, but eager cleanup avoids that delay entirely.
		_ = h.db.ClearInFlight(context.WithoutCancel(r.Context()), deb.ID)
		// Log the detailed error server-side for operator triage;
		// surface a generic message to the client so we don't leak
		// API keys, internal paths, or provider response fragments
		// via the error envelope.
		log.Printf("debate CreateRound: refiner %s failed for debate %s: %v",
			refiner.Name(), deb.ID, err)
		h.renderDebateError(w, r, http.StatusBadGateway,
			debateProviderLabel(refiner.Name())+" couldn't produce a suggestion. Nothing was changed — try again.")
		return
	}

	// Step 4: output validation.
	text := strings.TrimSpace(out.Text)
	if text == "" || len(text) < h.cfg.MinOutputLen || out.FinishReason == ai.FinishReasonLength {
		// context.WithoutCancel so a client disconnect between
		// ReserveInFlight success and this cleanup doesn't leak a
		// set in_flight_request_id. The stale-recovery window in
		// ReserveInFlight (StaleReservationAge, 90s) is the
		// fallback, but eager cleanup avoids that delay entirely.
		_ = h.db.ClearInFlight(context.WithoutCancel(r.Context()), deb.ID)
		h.renderDebateError(w, r, http.StatusBadGateway,
			debateProviderLabel(refiner.Name())+" couldn't produce a suggestion. Nothing was changed — try again.")
		return
	}

	// Step 5: insert tx.
	unified := diff.ComputeUnified(snapshot, text)
	_, centsDelta, err := h.insertRound(r.Context(), models.InsertDebateRoundInput{
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
		// context.WithoutCancel so a client disconnect between
		// ReserveInFlight success and this cleanup doesn't leak a
		// set in_flight_request_id. The stale-recovery window in
		// ReserveInFlight (StaleReservationAge, 90s) is the
		// fallback, but eager cleanup avoids that delay entirely.
		_ = h.db.ClearInFlight(context.WithoutCancel(r.Context()), deb.ID)
		h.writeInsertError(w, r, err)
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

	h.renderWorkspaceUpdate(w, r, dctx, "")
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

// renderDebateError keeps spec §3.3's status-code discipline but gives
// HTMX requests a human-readable banner (UI refactor spec §5.4). The
// htmx:beforeSwap listener in init.js permits the swap; HX-Retarget
// puts the banner in #debate-flash so the composer/suggestion in
// #debate-stage is never destroyed by an error.
func (h *DebateHandler) renderDebateError(w http.ResponseWriter, r *http.Request, status int, msg string) {
	if r.Header.Get("HX-Request") != "true" {
		http.Error(w, msg, status)
		return
	}
	w.Header().Set("HX-Retarget", "#debate-flash")
	w.Header().Set("HX-Reswap", "innerHTML")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_ = h.engine.RenderPartial(w, "debate_error.html", map[string]any{"Message": msg})
}

// debateProviderLabel mirrors render.go's providerLabel for handler-
// side error copy without importing the render FuncMap.
// NOTE: update alongside render.go's providerLabel when new refiners are wired in cmd/server/main.go.
func debateProviderLabel(name string) string {
	switch name {
	case "claude":
		return "Claude"
	case "gemini":
		return "Gemini"
	case "openai":
		return "ChatGPT"
	default:
		return "The AI provider"
	}
}

// writeReservationError maps ReserveInFlight's sentinel errors to the
// right HTTP status per spec §3.3. Keeps error branches distinct —
// never collapses "not active" / "in flight" / "generic infra" into a
// single path.
func (h *DebateHandler) writeReservationError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, models.ErrDebateNotActive):
		h.renderDebateError(w, r, http.StatusConflict,
			debateMsgStale)
	case errors.Is(err, models.ErrInFlightAIRequest):
		h.renderDebateError(w, r, http.StatusConflict,
			"A suggestion is already being written — give it a few seconds.")
	default:
		h.renderDebateError(w, r, http.StatusInternalServerError,
			debateMsgInfra)
	}
}

// writeInsertError maps InsertDebateRoundTx's sentinel errors.
func (h *DebateHandler) writeInsertError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, models.ErrStaleAIInput):
		h.renderDebateError(w, r, http.StatusConflict,
			"The description changed while the AI was writing — nothing was saved, please try again.")
	case errors.Is(err, models.ErrDebateNotActive):
		h.renderDebateError(w, r, http.StatusConflict,
			debateMsgStale)
	case errors.Is(err, models.ErrInReviewRoundExists):
		h.renderDebateError(w, r, http.StatusConflict,
			"A suggestion is already waiting — accept or dismiss it first.")
	default:
		h.renderDebateError(w, r, http.StatusInternalServerError,
			debateMsgInfra)
	}
}

// ── POST /tickets/{ticketID}/debate/rounds/{roundID}/accept ───────

// AcceptRound marks an in_review round as accepted, updating
// feature_debates.current_text to the round's output. After the
// accept tx commits, fires scoreAfterAccept as a fire-and-forget
// goroutine (spec §4.3 step 7) so the user's request returns
// immediately instead of waiting on the scorer's 60s AI call.
//
// The scorer goroutine uses context.WithoutCancel so the user
// closing the browser tab doesn't cancel the scorer tx mid-flight —
// we've already billed for the refiner call that just succeeded;
// the scorer call is a separate billable event that should either
// complete cleanly or log a WARN, never leak half-done state.
func (h *DebateHandler) AcceptRound(w http.ResponseWriter, r *http.Request) {
	dctx, code, err := h.requireDebateContext(r)
	if err != nil {
		h.engine.RenderError(w, code, err.Error())
		return
	}
	roundID := chi.URLParam(r, "roundID")
	if roundID == "" {
		h.renderDebateError(w, r, http.StatusBadRequest,
			"That suggestion no longer exists — reload the page.")
		return
	}

	deb, err := h.db.GetActiveDebate(r.Context(), dctx.ticket.ID)
	if errors.Is(err, pgx.ErrNoRows) {
		h.renderDebateError(w, r, http.StatusConflict,
			debateMsgStale)
		return
	}
	if err != nil {
		h.renderDebateError(w, r, http.StatusInternalServerError,
			debateMsgInfra)
		return
	}

	round, err := h.acceptRoundUnderLock(r.Context(), deb.ID, roundID)
	if err != nil {
		h.writeAcceptError(w, r, err)
		return
	}

	// Fire-and-forget scorer. context.WithoutCancel so a client
	// disconnect doesn't cancel the in-flight scorer tx; we still
	// honor the adapter's own AICallTimeout so the goroutine can't
	// leak indefinitely.
	//
	// Pass round.OutputText directly rather than re-reading
	// feature_debates.current_text inside the goroutine. Re-reading
	// would race with subsequent accept/undo operations: a concurrent
	// undo could shift current_text to an older value before the
	// goroutine runs, causing the scorer to evaluate stale content
	// while the result is still associated with this round's ID. The
	// direct-pass path guarantees scorer input == round output.
	if h.scorer != nil {
		go h.scoreAfterAccept(context.WithoutCancel(r.Context()),
			deb.ID, round.ID, dctx.ticket.ProjectID, round.OutputText)
	}

	h.renderWorkspaceUpdate(w, r, dctx, "")
}

// acceptRoundUnderLock runs the accept transaction: lock debate row,
// lock round row, verify status == 'active' at the debate level,
// update round + current_text, commit.
func (h *DebateHandler) acceptRoundUnderLock(ctx context.Context, debateID, roundID string) (*models.DebateRound, error) {
	tx, err := h.db.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// Lock the debate row first. All state-changing operations take
	// this lock as their first statement (spec §3.3 concurrency model)
	// so accept / reject / undo / approve / abandon serialize cleanly.
	var status string
	if qErr := tx.QueryRow(ctx,
		`SELECT status FROM feature_debates WHERE id = $1 FOR UPDATE`, debateID,
	).Scan(&status); qErr != nil {
		return nil, qErr
	}
	if status != models.DebateStatusActive {
		return nil, models.ErrDebateNotActive
	}

	round, err := h.db.AcceptRoundTx(ctx, tx, debateID, roundID)
	if err != nil {
		return nil, err
	}
	if commitErr := tx.Commit(ctx); commitErr != nil {
		return nil, commitErr
	}
	return round, nil
}

// writeAcceptError maps accept-path sentinels to the right status.
// ErrRoundNotInReview surfaces as 409 (stale client view), distinct
// from pgx.ErrNoRows (404, round doesn't exist) and infra failures.
func (h *DebateHandler) writeAcceptError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		h.renderDebateError(w, r, http.StatusNotFound,
			"That suggestion no longer exists — reload the page.")
	case errors.Is(err, models.ErrDebateNotActive):
		h.renderDebateError(w, r, http.StatusConflict,
			debateMsgStale)
	case errors.Is(err, models.ErrRoundNotInReview):
		h.renderDebateError(w, r, http.StatusConflict,
			"This suggestion was already decided — reload to continue.")
	default:
		h.renderDebateError(w, r, http.StatusInternalServerError,
			debateMsgInfra)
	}
}

// scoreAfterAccept runs the Scorer outside the accept tx (spec §4.3
// step 7 — holding FOR UPDATE across a 60s AI call would serialize
// Abandon/Undo behind every accept). On success, applies the score
// conditionally (UpdateEffortScoreCondTx silently discards out-of-
// order responses) and persists the scorer cost on the round row.
// All three updates happen under a fresh debate-row lock to stay
// consistent with the total_cost_micros accumulator (spec §6).
//
// Runs in its own goroutine; errors are logged at WARN and never
// surfaced to the user (the accept already succeeded). The context
// comes from context.WithoutCancel so client disconnect doesn't
// abort the billing update.
func (h *DebateHandler) scoreAfterAccept(ctx context.Context, debateID, roundID, projectID, textToScore string) {
	// Respect the adapter's own timeout envelope so this goroutine
	// can't wedge indefinitely on a stuck network call.
	callCtx, cancel := context.WithTimeout(ctx, h.cfg.AICallTimeout)
	defer cancel()

	res, err := h.scorer.Score(callCtx, textToScore)
	if err != nil {
		log.Printf("debate scoreAfterAccept: scorer failed for debate %s round %s: %v", debateID, roundID, err)
		return
	}

	// Accumulator + conditional score update under a fresh lock.
	tx, err := h.db.Pool.Begin(ctx)
	if err != nil {
		log.Printf("debate scoreAfterAccept: Begin: %v", err)
		return
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var oldTotal int64
	if qErr := tx.QueryRow(ctx,
		`SELECT total_cost_micros FROM feature_debates WHERE id = $1 FOR UPDATE`, debateID,
	).Scan(&oldTotal); qErr != nil {
		log.Printf("debate scoreAfterAccept: lock debate %s: %v", debateID, qErr)
		return
	}

	if uErr := h.db.UpdateScorerCostMicros(ctx, tx, roundID, res.Usage.CostMicros); uErr != nil {
		log.Printf("debate scoreAfterAccept: update scorer cost on round %s: %v", roundID, uErr)
		return
	}
	if uErr := h.db.UpdateEffortScoreCondTx(ctx, tx, debateID, roundID, res.Score, res.Hours, res.Reasoning); uErr != nil {
		log.Printf("debate scoreAfterAccept: UpdateEffortScoreCondTx debate %s: %v", debateID, uErr)
		return
	}

	newTotal := oldTotal + res.Usage.CostMicros
	if _, uErr := tx.Exec(ctx,
		`UPDATE feature_debates SET total_cost_micros = $1, updated_at = now() WHERE id = $2`,
		newTotal, debateID,
	); uErr != nil {
		log.Printf("debate scoreAfterAccept: update total_cost_micros debate %s: %v", debateID, uErr)
		return
	}

	if commitErr := tx.Commit(ctx); commitErr != nil {
		log.Printf("debate scoreAfterAccept: Commit debate %s: %v", debateID, commitErr)
		return
	}

	centsDelta := newTotal/10000 - oldTotal/10000
	if incErr := h.db.IncrementProjectCostCents(ctx, projectID, centsDelta); incErr != nil {
		log.Printf("debate scoreAfterAccept: project_costs rollup for scorer cost: %v", incErr)
	}
}

// ── POST /tickets/{ticketID}/debate/rounds/{roundID}/reject ───────

// RejectRound marks an in_review round as rejected without touching
// current_text. Returns the full workspace update via renderWorkspaceUpdate,
// with the rejected round's feedback pre-filled in the composer textarea.
func (h *DebateHandler) RejectRound(w http.ResponseWriter, r *http.Request) {
	dctx, code, err := h.requireDebateContext(r)
	if err != nil {
		h.engine.RenderError(w, code, err.Error())
		return
	}
	roundID := chi.URLParam(r, "roundID")
	if roundID == "" {
		h.renderDebateError(w, r, http.StatusBadRequest,
			"That suggestion no longer exists — reload the page.")
		return
	}

	deb, err := h.db.GetActiveDebate(r.Context(), dctx.ticket.ID)
	if errors.Is(err, pgx.ErrNoRows) {
		h.renderDebateError(w, r, http.StatusConflict,
			debateMsgStale)
		return
	}
	if err != nil {
		h.renderDebateError(w, r, http.StatusInternalServerError,
			debateMsgInfra)
		return
	}

	feedback, rejectErr := h.rejectRoundUnderLock(r.Context(), deb.ID, roundID)
	if rejectErr != nil {
		h.writeAcceptError(w, r, rejectErr) // same sentinel mapping as accept
		return
	}

	h.renderWorkspaceUpdate(w, r, dctx, feedback)
}

// rejectRoundUnderLock opens a transaction, locks the debate row, reads the
// round's feedback (so the caller can pre-fill the composer without a second
// round-trip), and delegates the status update to RejectRoundTx.
// It returns the feedback string (empty when none) and any error.
func (h *DebateHandler) rejectRoundUnderLock(ctx context.Context, debateID, roundID string) (string, error) {
	tx, err := h.db.Pool.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var status string
	if qErr := tx.QueryRow(ctx,
		`SELECT status FROM feature_debates WHERE id = $1 FOR UPDATE`, debateID,
	).Scan(&status); qErr != nil {
		return "", qErr
	}
	if status != models.DebateStatusActive {
		return "", models.ErrDebateNotActive
	}

	// Read feedback before the UPDATE so it is available to the caller
	// without a post-commit re-query of all rounds.
	var feedbackPtr *string
	_ = tx.QueryRow(ctx,
		`SELECT feedback FROM feature_debate_rounds WHERE id = $1`, roundID,
	).Scan(&feedbackPtr)
	var feedback string
	if feedbackPtr != nil {
		feedback = *feedbackPtr
	}

	if rErr := h.db.RejectRoundTx(ctx, tx, debateID, roundID); rErr != nil {
		return "", rErr
	}
	if cErr := tx.Commit(ctx); cErr != nil {
		return "", cErr
	}
	return feedback, nil
}

// ── POST /tickets/{ticketID}/debate/undo?from=N ───────────────────

// UndoRound cascading-deletes every round with round_number >= N
// and recomputes current_text. Returns the full workspace update
// (composer + OOB document/versions/chip) so the stage and all three
// regions refresh in one response without a page reload.
func (h *DebateHandler) UndoRound(w http.ResponseWriter, r *http.Request) {
	dctx, code, err := h.requireDebateContext(r)
	if err != nil {
		h.engine.RenderError(w, code, err.Error())
		return
	}

	fromStr := r.URL.Query().Get("from")
	fromN, err := strconv.Atoi(fromStr)
	if err != nil || fromN < 1 {
		h.renderDebateError(w, r, http.StatusBadRequest, "Invalid restore target.")
		return
	}

	deb, err := h.db.GetActiveDebate(r.Context(), dctx.ticket.ID)
	if errors.Is(err, pgx.ErrNoRows) {
		h.renderDebateError(w, r, http.StatusConflict,
			debateMsgStale)
		return
	}
	if err != nil {
		h.renderDebateError(w, r, http.StatusInternalServerError,
			debateMsgInfra)
		return
	}

	if undoErr := h.undoUnderLock(r.Context(), deb.ID, fromN); undoErr != nil {
		switch {
		case errors.Is(undoErr, models.ErrDebateNotActive):
			h.renderDebateError(w, r, http.StatusConflict,
				debateMsgStale)
		case errors.Is(undoErr, models.ErrNoRoundsToUndo):
			// ?from=999 on a 3-round debate, etc. Prevents accidental
			// effort-field wipes on range targets that delete no rows.
			h.renderDebateError(w, r, http.StatusNotFound,
				"That version no longer exists — reload the page.")
		default:
			log.Printf("debate UndoRound: %v", undoErr)
			h.renderDebateError(w, r, http.StatusInternalServerError,
				debateMsgInfra)
		}
		return
	}

	h.renderWorkspaceUpdate(w, r, dctx, "")
}

// ── POST /tickets/{ticketID}/debate/approve ──────────────────────

// ApproveDebate writes the debate's current_text to the ticket's
// description_markdown and marks the debate 'approved'. Guarded by
// the spec §4.5 compare-and-swap: if the ticket's description no
// longer equals original_ticket_description (caught for example when
// a v0.1.0 pod edits it during a rolling upgrade before this PR
// landed), the approve refuses with 409 so the user is told to
// Abandon and manually reconcile.
//
// On success returns Hx-Redirect to the ticket detail page — the
// debate is now terminal, the debate page's GET handler would show
// an empty-state anyway.
func (h *DebateHandler) ApproveDebate(w http.ResponseWriter, r *http.Request) {
	dctx, code, err := h.requireDebateContext(r)
	if err != nil {
		h.engine.RenderError(w, code, err.Error())
		return
	}

	// No pre-check GetActiveDebate: approveUnderLock runs a JOIN that
	// both finds the active debate AND takes FOR UPDATE in one
	// statement. pgx.ErrNoRows from that path means no active debate;
	// ErrDebateNotActive means a row exists but isn't active. Both
	// surface as 409 at the user level with distinct messages.
	if approveErr := h.approveUnderLock(r.Context(), dctx.ticket.ID); approveErr != nil {
		switch {
		case errors.Is(approveErr, pgx.ErrNoRows):
			h.renderDebateError(w, r, http.StatusConflict,
				debateMsgStale)
		case errors.Is(approveErr, models.ErrDebateNotActive):
			h.renderDebateError(w, r, http.StatusConflict,
				debateMsgStale)
		case errors.Is(approveErr, models.ErrExternalDescriptionEdit):
			// Tell the user exactly how to resolve: Stop refining unlocks
			// the ticket description for manual edit, then they can
			// start a new debate from the current state.
			h.renderDebateError(w, r, http.StatusConflict,
				"The ticket description was edited outside this session. To resolve: click Stop refining to unlock it, merge the changes manually, then start refining again.")
		default:
			log.Printf("debate ApproveDebate: %v", approveErr)
			h.renderDebateError(w, r, http.StatusInternalServerError,
				debateMsgInfra)
		}
		return
	}

	w.Header().Set("Hx-Redirect", "/tickets/"+dctx.ticket.ID)
	w.WriteHeader(http.StatusSeeOther)
}

// approveUnderLock looks up the active debate for the ticket and runs
// ApproveDebateTx under the combined debate+ticket row lock. No
// separate GetActiveDebate round-trip: the JOIN inside ApproveDebateTx
// (which already locks both rows) handles the lookup atomically.
func (h *DebateHandler) approveUnderLock(ctx context.Context, ticketID string) error {
	// Resolve active debate id inside a short query; ApproveDebateTx
	// expects the debate id alongside the ticket id so its JOIN filter
	// matches both. Using a regular SELECT here (not FOR UPDATE) is
	// safe because ApproveDebateTx re-locks the debate row in its own
	// tx with FOR UPDATE — this first read is just to resolve the id.
	var debateID string
	if err := h.db.Pool.QueryRow(ctx,
		`SELECT id FROM feature_debates WHERE ticket_id = $1 AND status = 'active' LIMIT 1`,
		ticketID,
	).Scan(&debateID); err != nil {
		return err
	}

	tx, err := h.db.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if aErr := h.db.ApproveDebateTx(ctx, tx, debateID, ticketID); aErr != nil {
		return aErr
	}
	return tx.Commit(ctx)
}

// ── POST /tickets/{ticketID}/debate/abandon ──────────────────────

// AbandonDebate marks the debate as abandoned without touching the
// ticket description. Also force-clears any lingering
// in_flight_request_id as the escape hatch for orphaned reservations
// whose stale-recovery timer hasn't fired yet.
func (h *DebateHandler) AbandonDebate(w http.ResponseWriter, r *http.Request) {
	dctx, code, err := h.requireDebateContext(r)
	if err != nil {
		h.engine.RenderError(w, code, err.Error())
		return
	}

	if abandonErr := h.abandonUnderLock(r.Context(), dctx.ticket.ID); abandonErr != nil {
		switch {
		case errors.Is(abandonErr, pgx.ErrNoRows):
			h.renderDebateError(w, r, http.StatusConflict,
				debateMsgStale)
		case errors.Is(abandonErr, models.ErrDebateNotActive):
			h.renderDebateError(w, r, http.StatusConflict,
				debateMsgStale)
		default:
			log.Printf("debate AbandonDebate: %v", abandonErr)
			h.renderDebateError(w, r, http.StatusInternalServerError,
				debateMsgInfra)
		}
		return
	}

	w.Header().Set("Hx-Redirect", "/tickets/"+dctx.ticket.ID)
	w.WriteHeader(http.StatusSeeOther)
}

// abandonUnderLock resolves the active debate and runs the atomic
// AbandonDebateTx. No pre-check round-trip: AbandonDebateTx uses an
// UPDATE ... WHERE status='active' that completes in one statement
// on the common path.
func (h *DebateHandler) abandonUnderLock(ctx context.Context, ticketID string) error {
	var debateID string
	if err := h.db.Pool.QueryRow(ctx,
		`SELECT id FROM feature_debates WHERE ticket_id = $1 AND status = 'active' LIMIT 1`,
		ticketID,
	).Scan(&debateID); err != nil {
		return err
	}

	tx, err := h.db.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if aErr := h.db.AbandonDebateTx(ctx, tx, debateID); aErr != nil {
		return aErr
	}
	return tx.Commit(ctx)
}

func (h *DebateHandler) undoUnderLock(ctx context.Context, debateID string, fromN int) error {
	tx, err := h.db.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var status string
	if qErr := tx.QueryRow(ctx,
		`SELECT status FROM feature_debates WHERE id = $1 FOR UPDATE`, debateID,
	).Scan(&status); qErr != nil {
		return qErr
	}
	if status != models.DebateStatusActive {
		return models.ErrDebateNotActive
	}
	if uErr := h.db.UndoRoundsFromTx(ctx, tx, debateID, fromN); uErr != nil {
		return uErr
	}
	return tx.Commit(ctx)
}

// renderWorkspaceUpdate emits the full post-mutation response: primary
// content for #debate-stage (suggestion if one is pending, composer
// otherwise) followed by OOB fragments for the document, versions rail,
// and effort chip. One response, four regions, no reload (UI refactor
// spec §5.3 — replaces the v0.2.0 Hx-Refresh/Hx-Redirect reloads).
func (h *DebateHandler) renderWorkspaceUpdate(w http.ResponseWriter, r *http.Request, dctx debateContext, feedback string) {
	deb, rounds, ok := h.loadActiveDebateAndRounds(w, r, dctx)
	if !ok {
		return
	}
	isStaff := auth.IsStaffOrAbove(dctx.user.Role)
	view := buildDebateView(deb, rounds, isStaff)
	chip := buildEffortChipView(deb, rounds, dctx.ticket.ID, time.Now(), h.cfg.StaleReservationAge)
	chip.OOB = true

	if view.Pending != nil {
		_ = h.engine.RenderPartial(w, "debate_suggestion.html", map[string]any{
			"TicketID": dctx.ticket.ID, "Pending": view.Pending,
		})
	} else {
		_ = h.engine.RenderPartial(w, "debate_composer.html", map[string]any{
			"TicketID": dctx.ticket.ID, "Providers": h.providerNames(), "Feedback": feedback,
		})
	}
	_ = h.engine.RenderPartial(w, "debate_document.html", map[string]any{
		"OOB": true, "TicketID": dctx.ticket.ID, "View": view, "Debate": deb, "Viewing": nil,
	})
	_ = h.engine.RenderPartial(w, "debate_versions.html", map[string]any{
		"OOB": true, "TicketID": dctx.ticket.ID, "View": view,
	})
	_ = h.engine.RenderPartial(w, "debate_effort_chip.html", chip)
}

// ── GET /tickets/{ticketID}/debate/document ───────────────────────

// ShowDocument returns the #debate-document partial in current mode —
// the "Back to current" target when viewing an older version.
func (h *DebateHandler) ShowDocument(w http.ResponseWriter, r *http.Request) {
	dctx, code, err := h.requireDebateContext(r)
	if err != nil {
		h.engine.RenderError(w, code, err.Error())
		return
	}
	deb, rounds, ok := h.loadActiveDebateAndRounds(w, r, dctx)
	if !ok {
		return
	}
	view := buildDebateView(deb, rounds, auth.IsStaffOrAbove(dctx.user.Role))
	_ = h.engine.RenderPartial(w, "debate_document.html", map[string]any{
		"OOB": false, "TicketID": dctx.ticket.ID, "View": view, "Debate": deb, "Viewing": nil,
	})
}

// ── GET /tickets/{ticketID}/debate/versions/{roundID} ─────────────

// ShowVersion renders an older ACCEPTED version (or "original" for the
// seed) read-only into #debate-document. Dismissed or unknown rounds
// are 404 — dismissed suggestions never became a version.
func (h *DebateHandler) ShowVersion(w http.ResponseWriter, r *http.Request) {
	dctx, code, err := h.requireDebateContext(r)
	if err != nil {
		h.engine.RenderError(w, code, err.Error())
		return
	}
	deb, rounds, ok := h.loadActiveDebateAndRounds(w, r, dctx)
	if !ok {
		return
	}
	view := buildDebateView(deb, rounds, auth.IsStaffOrAbove(dctx.user.Role))

	roundID := chi.URLParam(r, "roundID")
	var viewing *ViewingVersion
	if roundID == "original" {
		viewing = &ViewingVersion{Label: 0, Text: deb.SeedDescription, RestoreFrom: 1}
	} else {
		label := 0
		for _, rd := range rounds { // ASC — count accepted to derive the label
			if rd.Status == "accepted" {
				label++
				if rd.ID == roundID {
					viewing = &ViewingVersion{Label: label, Text: rd.OutputText, RestoreFrom: rd.RoundNumber + 1}
					break
				}
			}
		}
	}
	if viewing == nil {
		h.renderDebateError(w, r, http.StatusNotFound, "That version no longer exists — reload the page.")
		return
	}
	_ = h.engine.RenderPartial(w, "debate_document.html", map[string]any{
		"OOB": false, "TicketID": dctx.ticket.ID, "View": view, "Debate": deb, "Viewing": viewing,
	})
}

// loadActiveDebateAndRounds wraps the shared GET-partial preamble: 409
// banner when the debate is no longer active (stale tab), 500 banner
// on infra errors. ok=false means a response was already written.
func (h *DebateHandler) loadActiveDebateAndRounds(w http.ResponseWriter, r *http.Request, dctx debateContext) (*models.FeatureDebate, []models.DebateRound, bool) {
	deb, err := h.db.GetActiveDebate(r.Context(), dctx.ticket.ID)
	if errors.Is(err, pgx.ErrNoRows) {
		h.renderDebateError(w, r, http.StatusConflict, debateMsgStale)
		return nil, nil, false
	}
	if err != nil {
		h.renderDebateError(w, r, http.StatusInternalServerError, debateMsgInfra)
		return nil, nil, false
	}
	rounds, err := h.db.GetDebateRounds(r.Context(), deb.ID)
	if err != nil {
		h.renderDebateError(w, r, http.StatusInternalServerError, debateMsgInfra)
		return nil, nil, false
	}
	return deb, rounds, true
}

// ── POST /tickets/{ticketID}/debate/seed ──────────────────────────

// EditSeed updates the starting text before any round exists. Returns
// the refreshed #debate-document partial (the edit form lives inside it).
// Allowed only while status=active, no in-flight AI reservation, and
// zero rounds of any status exist (debate spec §4.1/§4.2).
func (h *DebateHandler) EditSeed(w http.ResponseWriter, r *http.Request) {
	dctx, code, err := h.requireDebateContext(r)
	if err != nil {
		h.engine.RenderError(w, code, err.Error())
		return
	}
	seed := strings.TrimSpace(r.FormValue("seed"))
	if seed == "" {
		h.renderDebateError(w, r, http.StatusBadRequest, "The starting text can't be empty.")
		return
	}
	if len(seed) > h.cfg.MaxTextLen {
		h.renderDebateError(w, r, http.StatusRequestEntityTooLarge, "The starting text is too long.")
		return
	}
	deb, err := h.db.GetActiveDebate(r.Context(), dctx.ticket.ID)
	if errors.Is(err, pgx.ErrNoRows) {
		h.renderDebateError(w, r, http.StatusConflict, debateMsgStale)
		return
	}
	if err != nil {
		h.renderDebateError(w, r, http.StatusInternalServerError, debateMsgInfra)
		return
	}
	switch err := h.db.UpdateDebateSeed(r.Context(), deb.ID, seed); {
	case err == nil:
	case errors.Is(err, models.ErrSeedFrozen):
		h.renderDebateError(w, r, http.StatusBadRequest, "The starting text is locked once a suggestion exists.")
		return
	case errors.Is(err, models.ErrInFlightAIRequest):
		h.renderDebateError(w, r, http.StatusConflict, "A suggestion is being written — wait for it before editing.")
		return
	case errors.Is(err, models.ErrDebateNotActive):
		h.renderDebateError(w, r, http.StatusConflict, debateMsgStale)
		return
	default:
		h.renderDebateError(w, r, http.StatusInternalServerError, debateMsgInfra)
		return
	}
	h.ShowDocument(w, r) // re-render the document partial with the new text
}

// ── GET /tickets/{ticketID}/debate/effort ─────────────────────────

// EffortChip returns the effort-chip partial. The server decides the
// polling behavior per response (UI refactor spec §5.2): hx-trigger is
// included only while the debate is active and the latest accept is
// younger than StaleReservationAge. Terminal or missing debates render
// a static chip — never an error, the chip is informational.
func (h *DebateHandler) EffortChip(w http.ResponseWriter, r *http.Request) {
	dctx, code, err := h.requireDebateContext(r)
	if err != nil {
		h.engine.RenderError(w, code, err.Error())
		return
	}

	deb, err := h.db.GetActiveDebate(r.Context(), dctx.ticket.ID)
	if errors.Is(err, pgx.ErrNoRows) {
		// Approved/abandoned in another tab: static empty chip, no poll.
		// Intentionally NOT loadActiveDebateAndRounds: the chip degrades gracefully instead of returning the 409 stale-tab banner.
		_ = h.engine.RenderPartial(w, "debate_effort_chip.html",
			EffortChipView{Debate: &models.FeatureDebate{}, TicketID: dctx.ticket.ID})
		return
	}
	if err != nil {
		h.renderDebateError(w, r, http.StatusInternalServerError, debateMsgInfra)
		return
	}
	rounds, err := h.db.GetDebateRounds(r.Context(), deb.ID)
	if err != nil {
		h.renderDebateError(w, r, http.StatusInternalServerError, debateMsgInfra)
		return
	}
	chip := buildEffortChipView(deb, rounds, dctx.ticket.ID, time.Now(), h.cfg.StaleReservationAge)
	_ = h.engine.RenderPartial(w, "debate_effort_chip.html", chip)
}
