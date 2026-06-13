package handlers

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/madalin/forgedesk/internal/ai"
	"github.com/madalin/forgedesk/internal/auth"
	"github.com/madalin/forgedesk/internal/models"
	"github.com/madalin/forgedesk/internal/render"
	"github.com/madalin/forgedesk/internal/testutil"
)

// newScorerHandler builds a DebateHandler with no refiners and the given
// scorer (which may be nil) — enough to exercise the background retry
// sweep, which never touches the HTTP layer.
func newScorerHandler(t *testing.T, db *models.DB, scorer ai.Scorer) *DebateHandler {
	t.Helper()
	engine, err := render.NewEngine(testutil.ProjectRoot()+"/templates", nil)
	if err != nil {
		t.Fatalf("loading templates: %v", err)
	}
	return NewDebateHandler(db, engine, map[string]ai.Refiner{}, scorer, DefaultDebateConfig())
}

// seedStaleScorableDebate creates a debate with one accepted round whose
// decided_at the caller controls and whose effort score is still NULL —
// i.e. a debate whose accept-path scorer goroutine failed. Returns the
// IDs the retry sweep needs to write a score back.
func seedStaleScorableDebate(t *testing.T, db *models.DB, decidedAt time.Time, output string) (debateID, roundID, projectID string) {
	t.Helper()
	ctx := context.Background()

	hash, _ := auth.HashPassword("TestPassword123!")
	user, err := db.CreateUser(ctx, t.Name()+"@example.com", hash, "Retry User", "client")
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	org, err := db.CreateOrgWithOwnerTx(ctx, user.ID, "Org "+t.Name(), "org-"+t.Name(), models.OrgRoleOwner)
	if err != nil {
		t.Fatalf("CreateOrgWithOwnerTx: %v", err)
	}
	proj, err := db.CreateProject(ctx, org.ID, "P", "proj-"+t.Name())
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	ticket := &models.Ticket{
		ProjectID: proj.ID, Type: "feature", Title: "F",
		DescriptionMarkdown: "seed", Status: "backlog", Priority: "medium", CreatedBy: user.ID,
	}
	if ctErr := db.CreateTicket(ctx, ticket); ctErr != nil {
		t.Fatalf("CreateTicket: %v", ctErr)
	}
	deb, err := db.StartDebate(ctx, ticket.ID, proj.ID, org.ID, user.ID)
	if err != nil {
		t.Fatalf("StartDebate: %v", err)
	}
	if err := db.Pool.QueryRow(ctx, `
		INSERT INTO feature_debate_rounds
			(debate_id, round_number, provider, model, triggered_by,
			 input_text, output_text, status, decided_at)
		VALUES ($1, 1, 'gemini', 'gemini-2.5-flash', $2, 'seed', $3, 'accepted', $4)
		RETURNING id`,
		deb.ID, user.ID, output, decidedAt,
	).Scan(&roundID); err != nil {
		t.Fatalf("insert accepted round: %v", err)
	}
	return deb.ID, roundID, proj.ID
}

func TestRetryStaleEffortScores_ScoresStaleDebate(t *testing.T) {
	pool := testutil.SetupTestDB(t)
	db := models.NewDB(pool)
	ctx := context.Background()

	debateID, roundID, _ := seedStaleScorableDebate(t, db, time.Now().Add(-10*time.Minute), "the accepted text")
	scorer := &ai.FakeScorer{Result: ai.ScoreResult{
		Score: 7, Hours: 12, Reasoning: "auth refactor is the risk",
		Usage: ai.RefineUsage{CostMicros: 5000},
	}}
	h := newScorerHandler(t, db, scorer)

	h.RetryStaleEffortScores(ctx)

	if scorer.CallCount != 1 {
		t.Errorf("scorer CallCount = %d, want 1", scorer.CallCount)
	}

	var score, hours *int
	var reasoning *string
	var scoredAt *time.Time
	var lastRound *string
	var total int64
	if err := db.Pool.QueryRow(ctx, `
		SELECT effort_score, effort_hours, effort_reasoning, effort_scored_at,
		       last_scored_round_id, total_cost_micros
		  FROM feature_debates WHERE id = $1`, debateID,
	).Scan(&score, &hours, &reasoning, &scoredAt, &lastRound, &total); err != nil {
		t.Fatalf("read debate: %v", err)
	}
	if score == nil || *score != 7 {
		t.Errorf("effort_score = %v, want 7", score)
	}
	if hours == nil || *hours != 12 {
		t.Errorf("effort_hours = %v, want 12", hours)
	}
	if reasoning == nil || *reasoning != "auth refactor is the risk" {
		t.Errorf("effort_reasoning = %v", reasoning)
	}
	if scoredAt == nil {
		t.Errorf("effort_scored_at still NULL after successful retry")
	}
	if lastRound == nil || *lastRound != roundID {
		t.Errorf("last_scored_round_id = %v, want %q", lastRound, roundID)
	}
	if total != 5000 {
		t.Errorf("total_cost_micros = %d, want 5000", total)
	}

	var roundCost *int64
	if err := db.Pool.QueryRow(ctx,
		`SELECT scorer_cost_micros FROM feature_debate_rounds WHERE id = $1`, roundID,
	).Scan(&roundCost); err != nil {
		t.Fatalf("read round cost: %v", err)
	}
	if roundCost == nil || *roundCost != 5000 {
		t.Errorf("round scorer_cost_micros = %v, want 5000", roundCost)
	}
}

func TestRetryStaleEffortScores_FailureLeavesNullAndBacksOff(t *testing.T) {
	pool := testutil.SetupTestDB(t)
	db := models.NewDB(pool)
	ctx := context.Background()

	debateID, _, _ := seedStaleScorableDebate(t, db, time.Now().Add(-10*time.Minute), "text")
	scorer := &ai.FakeScorer{Err: errors.New("provider down")}
	h := newScorerHandler(t, db, scorer)

	h.RetryStaleEffortScores(ctx)

	if scorer.CallCount != 1 {
		t.Errorf("scorer CallCount = %d, want 1", scorer.CallCount)
	}

	var score *int
	var attempts int
	var nextAt *time.Time
	if err := db.Pool.QueryRow(ctx,
		`SELECT effort_score, effort_retry_attempts, effort_retry_next_at FROM feature_debates WHERE id = $1`, debateID,
	).Scan(&score, &attempts, &nextAt); err != nil {
		t.Fatalf("read debate: %v", err)
	}
	if score != nil {
		t.Errorf("effort_score = %v, want NULL after scorer failure", *score)
	}
	// The claim ran (bumped attempts) and set a backoff lease, so the next
	// sweep waits rather than hammering a dead provider.
	if attempts != 1 {
		t.Errorf("effort_retry_attempts = %d, want 1", attempts)
	}
	if nextAt == nil {
		t.Errorf("effort_retry_next_at not set — failed retry won't back off")
	}
}

func TestRetryStaleEffortScores_NilScorerNoOp(t *testing.T) {
	pool := testutil.SetupTestDB(t)
	db := models.NewDB(pool)
	ctx := context.Background()

	debateID, _, _ := seedStaleScorableDebate(t, db, time.Now().Add(-10*time.Minute), "text")
	h := newScorerHandler(t, db, nil)

	// Must not panic and must not claim (no scorer = nothing to do).
	h.RetryStaleEffortScores(ctx)

	var attempts int
	if err := db.Pool.QueryRow(ctx,
		`SELECT effort_retry_attempts FROM feature_debates WHERE id = $1`, debateID,
	).Scan(&attempts); err != nil {
		t.Fatalf("read debate: %v", err)
	}
	if attempts != 0 {
		t.Errorf("effort_retry_attempts = %d, want 0 (nil scorer must not claim)", attempts)
	}
}
