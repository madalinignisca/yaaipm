package models

import (
	"context"
	"testing"
	"time"

	"github.com/madalin/forgedesk/internal/testutil"
)

// retry tuning shared by the claim tests. minAge is the "latest accept
// must be older than this" gate; base/max bound the exponential backoff.
const (
	testRetryMinAge = 5 * time.Minute
	testRetryBase   = 5 * time.Minute
	testRetryMax    = time.Hour
	testRetryLimit  = 10
)

// seedDebateWithAcceptedRound creates a fresh debate with a single
// accepted round whose decided_at the caller controls, so the claim
// tests can place the "last accept" before or after the staleness
// cutoff. The debate starts unscored (effort_scored_at NULL) and with no
// retry state, matching a debate whose scoreAfterAccept goroutine failed.
func seedDebateWithAcceptedRound(t *testing.T, db *DB, decidedAt time.Time, output string) (debateID, roundID string) {
	t.Helper()
	ctx := context.Background()

	orgID, userID, projectID, ticketID := seedFeatureTicket(t, db, "seed desc")
	deb, err := db.StartDebate(ctx, ticketID, projectID, orgID, userID)
	if err != nil {
		t.Fatalf("StartDebate: %v", err)
	}

	if err := db.Pool.QueryRow(ctx, `
		INSERT INTO feature_debate_rounds
			(debate_id, round_number, provider, model, triggered_by,
			 input_text, output_text, status, decided_at)
		VALUES ($1, 1, 'gemini', 'gemini-2.5-flash', $2, $3, $4, 'accepted', $5)
		RETURNING id`,
		deb.ID, userID, "seed desc", output, decidedAt,
	).Scan(&roundID); err != nil {
		t.Fatalf("insert accepted round: %v", err)
	}
	return deb.ID, roundID
}

// readRetryState returns the persisted backoff state for a debate.
func readRetryState(t *testing.T, db *DB, debateID string) (attempts int, nextAt *time.Time) {
	t.Helper()
	if err := db.Pool.QueryRow(context.Background(),
		`SELECT effort_retry_attempts, effort_retry_next_at FROM feature_debates WHERE id = $1`,
		debateID,
	).Scan(&attempts, &nextAt); err != nil {
		t.Fatalf("readRetryState: %v", err)
	}
	return attempts, nextAt
}

func TestClaimStaleEffortScores_ClaimsStaleDebate(t *testing.T) {
	pool := testutil.SetupTestDB(t)
	db := NewDB(pool)
	ctx := context.Background()

	now := time.Now().UTC().Truncate(time.Microsecond)
	debateID, roundID := seedDebateWithAcceptedRound(t, db, now.Add(-10*time.Minute), "scored output")

	claimed, err := db.ClaimStaleEffortScores(ctx, now, testRetryMinAge, testRetryBase, testRetryMax, testRetryLimit)
	if err != nil {
		t.Fatalf("ClaimStaleEffortScores: %v", err)
	}
	if len(claimed) != 1 {
		t.Fatalf("claimed %d debates, want 1", len(claimed))
	}
	got := claimed[0]
	if got.DebateID != debateID {
		t.Errorf("DebateID = %q, want %q", got.DebateID, debateID)
	}
	if got.RoundID != roundID {
		t.Errorf("RoundID = %q, want %q", got.RoundID, roundID)
	}
	if got.OutputText != "scored output" {
		t.Errorf("OutputText = %q, want %q", got.OutputText, "scored output")
	}

	// Claiming must bump attempts 0→1 and set the first-attempt lease to
	// now + base (base * 2^0).
	attempts, nextAt := readRetryState(t, db, debateID)
	if attempts != 1 {
		t.Errorf("attempts = %d, want 1", attempts)
	}
	if nextAt == nil {
		t.Fatalf("effort_retry_next_at not set after claim")
	}
	wantNext := now.Add(testRetryBase)
	if d := nextAt.Sub(wantNext); d < -time.Second || d > time.Second {
		t.Errorf("next_at = %v, want ~%v (delta %v)", nextAt, wantNext, d)
	}
}

func TestClaimStaleEffortScores_SkipsRecentlyAccepted(t *testing.T) {
	pool := testutil.SetupTestDB(t)
	db := NewDB(pool)
	ctx := context.Background()

	now := time.Now().UTC().Truncate(time.Microsecond)
	// Accepted 1 minute ago — inside the 5-minute staleness window, so the
	// accept-path scorer goroutine may still be in flight. Must not claim.
	seedDebateWithAcceptedRound(t, db, now.Add(-1*time.Minute), "fresh")

	claimed, err := db.ClaimStaleEffortScores(ctx, now, testRetryMinAge, testRetryBase, testRetryMax, testRetryLimit)
	if err != nil {
		t.Fatalf("ClaimStaleEffortScores: %v", err)
	}
	if len(claimed) != 0 {
		t.Fatalf("claimed %d, want 0 (accept too recent)", len(claimed))
	}
}

func TestClaimStaleEffortScores_SkipsAlreadyScored(t *testing.T) {
	pool := testutil.SetupTestDB(t)
	db := NewDB(pool)
	ctx := context.Background()

	now := time.Now().UTC().Truncate(time.Microsecond)
	debateID, _ := seedDebateWithAcceptedRound(t, db, now.Add(-10*time.Minute), "out")
	if _, err := db.Pool.Exec(ctx,
		`UPDATE feature_debates SET effort_score = 5, effort_hours = 8, effort_scored_at = now() WHERE id = $1`,
		debateID,
	); err != nil {
		t.Fatalf("mark scored: %v", err)
	}

	claimed, err := db.ClaimStaleEffortScores(ctx, now, testRetryMinAge, testRetryBase, testRetryMax, testRetryLimit)
	if err != nil {
		t.Fatalf("ClaimStaleEffortScores: %v", err)
	}
	if len(claimed) != 0 {
		t.Fatalf("claimed %d, want 0 (already scored)", len(claimed))
	}
}

func TestClaimStaleEffortScores_SkipsInBackoff(t *testing.T) {
	pool := testutil.SetupTestDB(t)
	db := NewDB(pool)
	ctx := context.Background()

	now := time.Now().UTC().Truncate(time.Microsecond)
	debateID, _ := seedDebateWithAcceptedRound(t, db, now.Add(-10*time.Minute), "out")
	// Lease still in the future — a prior attempt is backing off.
	if _, err := db.Pool.Exec(ctx,
		`UPDATE feature_debates SET effort_retry_attempts = 1, effort_retry_next_at = $2 WHERE id = $1`,
		debateID, now.Add(10*time.Minute),
	); err != nil {
		t.Fatalf("set backoff: %v", err)
	}

	claimed, err := db.ClaimStaleEffortScores(ctx, now, testRetryMinAge, testRetryBase, testRetryMax, testRetryLimit)
	if err != nil {
		t.Fatalf("ClaimStaleEffortScores: %v", err)
	}
	if len(claimed) != 0 {
		t.Fatalf("claimed %d, want 0 (in backoff)", len(claimed))
	}
}

func TestClaimStaleEffortScores_SkipsAbandoned(t *testing.T) {
	pool := testutil.SetupTestDB(t)
	db := NewDB(pool)
	ctx := context.Background()

	now := time.Now().UTC().Truncate(time.Microsecond)
	debateID, _ := seedDebateWithAcceptedRound(t, db, now.Add(-10*time.Minute), "out")
	if _, err := db.Pool.Exec(ctx,
		`UPDATE feature_debates SET status = 'abandoned' WHERE id = $1`, debateID,
	); err != nil {
		t.Fatalf("abandon: %v", err)
	}

	claimed, err := db.ClaimStaleEffortScores(ctx, now, testRetryMinAge, testRetryBase, testRetryMax, testRetryLimit)
	if err != nil {
		t.Fatalf("ClaimStaleEffortScores: %v", err)
	}
	if len(claimed) != 0 {
		t.Fatalf("claimed %d, want 0 (abandoned)", len(claimed))
	}
}

func TestClaimStaleEffortScores_SkipsNoAcceptedRound(t *testing.T) {
	pool := testutil.SetupTestDB(t)
	db := NewDB(pool)
	ctx := context.Background()

	now := time.Now().UTC().Truncate(time.Microsecond)
	orgID, userID, projectID, ticketID := seedFeatureTicket(t, db, "seed desc")
	deb, err := db.StartDebate(ctx, ticketID, projectID, orgID, userID)
	if err != nil {
		t.Fatalf("StartDebate: %v", err)
	}
	// An in_review round only — nothing accepted, so there is nothing to
	// score and the empty-state sidebar copy is correct.
	if _, execErr := db.Pool.Exec(ctx, `
		INSERT INTO feature_debate_rounds
			(debate_id, round_number, provider, model, triggered_by, input_text, output_text, status)
		VALUES ($1, 1, 'gemini', 'gemini-2.5-flash', $2, 'in', 'out', 'in_review')`,
		deb.ID, userID,
	); execErr != nil {
		t.Fatalf("insert in_review round: %v", execErr)
	}

	claimed, err := db.ClaimStaleEffortScores(ctx, now, testRetryMinAge, testRetryBase, testRetryMax, testRetryLimit)
	if err != nil {
		t.Fatalf("ClaimStaleEffortScores: %v", err)
	}
	if len(claimed) != 0 {
		t.Fatalf("claimed %d, want 0 (no accepted round)", len(claimed))
	}
}

func TestClaimStaleEffortScores_ExponentialBackoff(t *testing.T) {
	pool := testutil.SetupTestDB(t)
	db := NewDB(pool)
	ctx := context.Background()

	now := time.Now().UTC().Truncate(time.Microsecond)
	debateID, _ := seedDebateWithAcceptedRound(t, db, now.Add(-30*time.Minute), "out")
	// Pretend two attempts already happened, with a lease that has since
	// elapsed. The next claim is attempt #3, so backoff = base * 2^2.
	if _, err := db.Pool.Exec(ctx,
		`UPDATE feature_debates SET effort_retry_attempts = 2, effort_retry_next_at = $2 WHERE id = $1`,
		debateID, now.Add(-1*time.Minute),
	); err != nil {
		t.Fatalf("seed attempts: %v", err)
	}

	claimed, err := db.ClaimStaleEffortScores(ctx, now, testRetryMinAge, testRetryBase, testRetryMax, testRetryLimit)
	if err != nil {
		t.Fatalf("ClaimStaleEffortScores: %v", err)
	}
	if len(claimed) != 1 {
		t.Fatalf("claimed %d, want 1", len(claimed))
	}

	attempts, nextAt := readRetryState(t, db, debateID)
	if attempts != 3 {
		t.Errorf("attempts = %d, want 3", attempts)
	}
	wantNext := now.Add(testRetryBase * 4) // base * 2^2
	if d := nextAt.Sub(wantNext); d < -time.Second || d > time.Second {
		t.Errorf("next_at = %v, want ~%v (delta %v)", nextAt, wantNext, d)
	}
}

func TestClaimStaleEffortScores_BackoffCappedAtMax(t *testing.T) {
	pool := testutil.SetupTestDB(t)
	db := NewDB(pool)
	ctx := context.Background()

	now := time.Now().UTC().Truncate(time.Microsecond)
	debateID, _ := seedDebateWithAcceptedRound(t, db, now.Add(-30*time.Minute), "out")
	// 20 prior attempts → base * 2^20 would be ~10 years; the cap must
	// clamp it to max so a permanently-dead provider still gets retried
	// roughly hourly rather than drifting off to never.
	if _, err := db.Pool.Exec(ctx,
		`UPDATE feature_debates SET effort_retry_attempts = 20, effort_retry_next_at = $2 WHERE id = $1`,
		debateID, now.Add(-1*time.Minute),
	); err != nil {
		t.Fatalf("seed attempts: %v", err)
	}

	if _, err := db.ClaimStaleEffortScores(ctx, now, testRetryMinAge, testRetryBase, testRetryMax, testRetryLimit); err != nil {
		t.Fatalf("ClaimStaleEffortScores: %v", err)
	}

	_, nextAt := readRetryState(t, db, debateID)
	wantNext := now.Add(testRetryMax)
	if d := nextAt.Sub(wantNext); d < -time.Second || d > time.Second {
		t.Errorf("next_at = %v, want capped ~%v (delta %v)", nextAt, wantNext, d)
	}
}
