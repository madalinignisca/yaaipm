package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/madalin/forgedesk/internal/ai"
	"github.com/madalin/forgedesk/internal/models"
	"github.com/madalin/forgedesk/internal/render"
	"github.com/madalin/forgedesk/internal/testutil"
)

// newMultiScorerHandler builds a handler with an explicit scorer
// registry, so a test can control exactly which providers are available
// and which one the project selects (issue #63).
func newMultiScorerHandler(t *testing.T, db *models.DB, scorers map[string]ai.Scorer) *DebateHandler {
	t.Helper()
	engine, err := render.NewEngine(testutil.ProjectRoot()+"/templates", nil)
	if err != nil {
		t.Fatalf("loading templates: %v", err)
	}
	return NewDebateHandler(db, engine, map[string]ai.Refiner{}, scorers, DefaultDebateConfig())
}

func fakeScorerFor(provider, model string) *ai.FakeScorer {
	return &ai.FakeScorer{
		NameVal: provider, ModelVal: model,
		Result: ai.ScoreResult{
			Score: 6, Hours: 9, Reasoning: "scored by " + provider,
			Usage: ai.RefineUsage{CostMicros: 4000, Model: model},
		},
	}
}

// readEffortScorer returns the recorded provider/model for a debate.
func readEffortScorer(t *testing.T, db *models.DB, debateID string) (provider, model *string, score *int) {
	t.Helper()
	if err := db.Pool.QueryRow(context.Background(),
		`SELECT effort_scorer_provider, effort_scorer_model, effort_score
		   FROM feature_debates WHERE id = $1`, debateID,
	).Scan(&provider, &model, &score); err != nil {
		t.Fatalf("read debate effort scorer: %v", err)
	}
	return provider, model, score
}

// The whole point of #63: the project's configured provider decides which
// scorer runs, and the score records who produced it.
func TestRetryStaleEffortScores_UsesProjectConfiguredProvider(t *testing.T) {
	pool := testutil.SetupTestDB(t)
	db := models.NewDB(pool)
	ctx := context.Background()

	debateID, _, projectID := seedStaleScorableDebate(t, db, time.Now().Add(-10*time.Minute), "the accepted text")
	if err := db.UpdateProjectScorerProvider(ctx, projectID, ai.ProviderOpenAI); err != nil {
		t.Fatalf("UpdateProjectScorerProvider: %v", err)
	}

	openaiScorer := fakeScorerFor(ai.ProviderOpenAI, ai.ModelGPT5Mini)
	geminiScorer := fakeScorerFor(ai.ProviderGemini, ai.ModelGeminiFlash)
	h := newMultiScorerHandler(t, db, map[string]ai.Scorer{
		ai.ProviderOpenAI: openaiScorer,
		ai.ProviderGemini: geminiScorer,
	})

	h.RetryStaleEffortScores(ctx)

	if openaiScorer.CallCount != 1 {
		t.Errorf("openai scorer CallCount = %d, want 1", openaiScorer.CallCount)
	}
	// The default provider must NOT have been consulted — this is the
	// assertion that fails if resolution silently falls back to gemini.
	if geminiScorer.CallCount != 0 {
		t.Errorf("gemini scorer CallCount = %d, want 0 (project selected openai)", geminiScorer.CallCount)
	}

	provider, model, score := readEffortScorer(t, db, debateID)
	if score == nil {
		t.Fatal("effort_score is NULL, want it written")
	}
	if provider == nil || *provider != ai.ProviderOpenAI {
		t.Errorf("effort_scorer_provider = %s, want openai", derefOrNull(provider))
	}
	if model == nil || *model != ai.ModelGPT5Mini {
		t.Errorf("effort_scorer_model = %s, want %s", derefOrNull(model), ai.ModelGPT5Mini)
	}
}

// derefOrNull renders a nullable column for failure messages — %v on a
// *string prints the pointer address, which says nothing useful.
func derefOrNull(s *string) string {
	if s == nil {
		return "NULL"
	}
	return *s
}

// Spec §3.2: a provider with no registered scorer is skipped with a
// warning, never silently swapped for a different vendor. Recording a
// Gemini score on a project that asked for Claude would misattribute both
// the estimate and its cost.
func TestRetryStaleEffortScores_UnregisteredProviderSkipsWithoutFallback(t *testing.T) {
	pool := testutil.SetupTestDB(t)
	db := models.NewDB(pool)
	ctx := context.Background()

	debateID, _, projectID := seedStaleScorableDebate(t, db, time.Now().Add(-10*time.Minute), "the accepted text")
	if err := db.UpdateProjectScorerProvider(ctx, projectID, ai.ProviderClaude); err != nil {
		t.Fatalf("UpdateProjectScorerProvider: %v", err)
	}

	// Claude is configured on the project but absent from the registry —
	// the shape of a deployment missing ANTHROPIC_API_KEY.
	geminiScorer := fakeScorerFor(ai.ProviderGemini, ai.ModelGeminiFlash)
	h := newMultiScorerHandler(t, db, map[string]ai.Scorer{ai.ProviderGemini: geminiScorer})

	h.RetryStaleEffortScores(ctx)

	if geminiScorer.CallCount != 0 {
		t.Errorf("gemini scorer CallCount = %d, want 0 — no silent fallback", geminiScorer.CallCount)
	}
	provider, _, score := readEffortScorer(t, db, debateID)
	if score != nil {
		t.Errorf("effort_score = %d, want NULL (nothing should have scored)", *score)
	}
	if provider != nil {
		t.Errorf("effort_scorer_provider = %q, want NULL", *provider)
	}
}

// An empty registry (no provider API keys at all) must be a no-op rather
// than a nil-map panic — this guards the len() gate that replaced the old
// nil-scorer check.
func TestRetryStaleEffortScores_EmptyRegistryIsNoOp(t *testing.T) {
	pool := testutil.SetupTestDB(t)
	db := models.NewDB(pool)
	ctx := context.Background()

	debateID, _, _ := seedStaleScorableDebate(t, db, time.Now().Add(-10*time.Minute), "the accepted text")
	h := newMultiScorerHandler(t, db, map[string]ai.Scorer{})

	h.RetryStaleEffortScores(ctx)

	_, _, score := readEffortScorer(t, db, debateID)
	if score != nil {
		t.Errorf("effort_score = %d, want NULL", *score)
	}

	// The sweep must not have burned a retry attempt either: with no
	// scorer there was nothing to try, and consuming backoff would delay
	// real scoring once a key is configured.
	var attempts int
	if err := db.Pool.QueryRow(ctx,
		`SELECT effort_retry_attempts FROM feature_debates WHERE id = $1`, debateID).Scan(&attempts); err != nil {
		t.Fatalf("read attempts: %v", err)
	}
	if attempts != 0 {
		t.Errorf("effort_retry_attempts = %d, want 0 (nothing was claimed)", attempts)
	}
}

// acceptRoundOK POSTs the accept endpoint and requires a 200. Accepting
// must succeed regardless of whether scoring can run afterwards — the
// score is fire-and-forget.
func acceptRoundOK(t *testing.T, r *chi.Mux, cookie *http.Cookie, ticketID, roundID string) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost,
		"/tickets/"+ticketID+"/debate/rounds/"+roundID+"/accept", http.NoBody)
	req.Header.Set("Hx-Request", "true")
	req.AddCookie(cookie)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("accept: got %d, want 200 — body: %s", w.Code, w.Body.String())
	}
}

// waitForEffortScore polls until the accept path's fire-and-forget
// scoring goroutine has written a score for the given round.
func waitForEffortScore(t *testing.T, db *models.DB, ticketID, roundID string) bool {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		deb, err := db.GetActiveDebate(context.Background(), ticketID)
		if err != nil {
			return false
		}
		if deb.LastScoredRoundID != nil && *deb.LastScoredRoundID == roundID {
			return true
		}
		time.Sleep(50 * time.Millisecond)
	}
	return false
}

// The accept path resolves the provider inside its scoring goroutine,
// reading dctx.ticket.ProjectID — plumbing unique to this call site and
// not covered by the sweep tests. Asserts the configured provider runs
// and is what gets recorded against the score.
func TestAcceptRound_UsesProjectConfiguredProvider(t *testing.T) {
	openaiScorer := fakeScorerFor(ai.ProviderOpenAI, ai.ModelGPT5Mini)
	geminiScorer := fakeScorerFor(ai.ProviderGemini, ai.ModelGeminiFlash)

	r, db, sessions := setupDebateTestEnvWithRegistry(t, map[string]ai.Scorer{
		ai.ProviderOpenAI: openaiScorer,
		ai.ProviderGemini: geminiScorer,
	})
	ticket, cookie := seedAuthedFeatureTicket(t, db, sessions)
	if err := db.UpdateProjectScorerProvider(context.Background(), ticket.ProjectID, ai.ProviderOpenAI); err != nil {
		t.Fatalf("UpdateProjectScorerProvider: %v", err)
	}
	deb, round := insertInReviewRound(t, db, cookie, r, ticket.ID, "scored text")

	acceptRoundOK(t, r, cookie, ticket.ID, round.ID)

	if !waitForEffortScore(t, db, ticket.ID, round.ID) {
		t.Fatal("scorer did not write a score within 5s")
	}
	if openaiScorer.CallCount != 1 {
		t.Errorf("openai scorer CallCount = %d, want 1", openaiScorer.CallCount)
	}
	if geminiScorer.CallCount != 0 {
		t.Errorf("gemini scorer CallCount = %d, want 0 (project selected openai)", geminiScorer.CallCount)
	}

	provider, model, score := readEffortScorer(t, db, deb.ID)
	if score == nil {
		t.Fatal("effort_score is NULL, want it written")
	}
	if provider == nil || *provider != ai.ProviderOpenAI {
		t.Errorf("effort_scorer_provider = %s, want openai", derefOrNull(provider))
	}
	if model == nil || *model != ai.ModelGPT5Mini {
		t.Errorf("effort_scorer_model = %s, want %s", derefOrNull(model), ai.ModelGPT5Mini)
	}
}

// Accept must still succeed when the project names a provider with no
// registered scorer — the round is accepted, the score is simply skipped
// with a WARNING, and no other vendor is substituted.
func TestAcceptRound_UnregisteredProviderStillAcceptsWithoutScoring(t *testing.T) {
	geminiScorer := fakeScorerFor(ai.ProviderGemini, ai.ModelGeminiFlash)
	r, db, sessions := setupDebateTestEnvWithRegistry(t, map[string]ai.Scorer{
		ai.ProviderGemini: geminiScorer,
	})
	ticket, cookie := seedAuthedFeatureTicket(t, db, sessions)
	// Claude configured but absent from the registry: a deployment with
	// no ANTHROPIC_API_KEY.
	if err := db.UpdateProjectScorerProvider(context.Background(), ticket.ProjectID, ai.ProviderClaude); err != nil {
		t.Fatalf("UpdateProjectScorerProvider: %v", err)
	}
	deb, round := insertInReviewRound(t, db, cookie, r, ticket.ID, "scored text")

	// The accept itself must not fail just because scoring can't run.
	acceptRoundOK(t, r, cookie, ticket.ID, round.ID)

	// Give the goroutine a chance to (not) score.
	time.Sleep(300 * time.Millisecond)

	if geminiScorer.CallCount != 0 {
		t.Errorf("gemini scorer CallCount = %d, want 0 — no silent fallback", geminiScorer.CallCount)
	}
	provider, _, score := readEffortScorer(t, db, deb.ID)
	if score != nil {
		t.Errorf("effort_score = %d, want NULL", *score)
	}
	if provider != nil {
		t.Errorf("effort_scorer_provider = %q, want NULL", *provider)
	}
}

// ClaimStaleEffortScores joins the project's provider into each claimed
// row so the sweep needs no extra query. Pinned directly because a
// regression here would silently degrade to the zero value "", which
// looks exactly like an unregistered provider.
func TestClaimStaleEffortScores_ReturnsProjectScorerProvider(t *testing.T) {
	pool := testutil.SetupTestDB(t)
	db := models.NewDB(pool)
	ctx := context.Background()

	_, _, projectID := seedStaleScorableDebate(t, db, time.Now().Add(-10*time.Minute), "text")
	if err := db.UpdateProjectScorerProvider(ctx, projectID, ai.ProviderClaude); err != nil {
		t.Fatalf("UpdateProjectScorerProvider: %v", err)
	}

	claimed, err := db.ClaimStaleEffortScores(ctx, time.Now(),
		5*time.Minute, time.Minute, time.Hour, 10)
	if err != nil {
		t.Fatalf("ClaimStaleEffortScores: %v", err)
	}
	if len(claimed) != 1 {
		t.Fatalf("claimed %d debates, want 1", len(claimed))
	}
	if claimed[0].ScorerProvider != ai.ProviderClaude {
		t.Errorf("ScorerProvider = %q, want claude", claimed[0].ScorerProvider)
	}
}
