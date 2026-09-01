package models

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/madalin/forgedesk/internal/testutil"
)

// backfillStatements pulls the INSERT statements out of the real migration
// file rather than restating them here. A copy in the test would be free to
// drift from the SQL that actually ships, and the backfill runs exactly once
// against production — there is no second chance to notice.
func backfillStatements(t *testing.T) []string {
	t.Helper()
	path := filepath.Join(testutil.ProjectRoot(), "migrations", "000036_debate_spend_ledger.up.sql")
	src, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading migration: %v", err)
	}

	var out []string
	for stmt := range strings.SplitSeq(string(src), ";") {
		if regexp.MustCompile(`(?is)INSERT\s+INTO\s+debate_spend`).MatchString(stmt) {
			out = append(out, stmt+";")
		}
	}
	if len(out) != 2 {
		t.Fatalf("expected 2 backfill INSERTs in the migration, found %d — "+
			"if the migration changed shape this test is no longer testing it", len(out))
	}
	return out
}

// TestBackfillRecoversPreMigrationSpend covers the deploy-time trap in #129.
//
// Enforcement now reads debate_spend. Pointing it at an empty table would drop
// every org's observed spend to zero and silently lift every cap for the rest
// of the month, which is a worse failure than the bug being fixed. The
// migration backfills from surviving rounds; this proves that it does.
//
// It also proves the backfill is re-runnable. Migrations here are applied
// BEFORE the new image rolls out, so rounds created in that window are written
// by old code that knows nothing about the ledger. Running the same backfill
// again after the rollout must pick those up without double-counting the rows
// it already inserted.
func TestBackfillRecoversPreMigrationSpend(t *testing.T) {
	pool := testutil.SetupTestDB(t)
	db := NewDB(pool)
	ctx := context.Background()

	orgID, userID, projID, ticketID := seedFeatureTicket(t, db, "desc")
	deb, err := db.StartDebate(ctx, ticketID, projID, orgID, userID)
	if err != nil {
		t.Fatalf("StartDebate: %v", err)
	}

	now := time.Now().UTC()
	from, to := CurrentUTCMonthRange(now)
	seedRoundAt(t, db, deb.ID, userID, 1, int64Ptr(1_000_000), int64Ptr(250_000), "accepted", from.Add(time.Hour))
	seedRoundAt(t, db, deb.ID, userID, 2, int64Ptr(500_000), nil, "rejected", from.Add(2*time.Hour))

	const wantTotal = int64(1_000_000 + 250_000 + 500_000)

	// Simulate the pre-migration world: the round rows exist and carry their
	// costs, but nothing has ever written the ledger.
	if _, delErr := db.Pool.Exec(ctx, `DELETE FROM debate_spend`); delErr != nil {
		t.Fatalf("clearing ledger: %v", delErr)
	}
	if spend, sErr := db.SumOrgDebateSpendMicros(ctx, orgID, from, to); sErr != nil || spend != 0 {
		t.Fatalf("precondition: spend = %d (err %v), want 0 before backfill", spend, sErr)
	}

	stmts := backfillStatements(t)
	for _, stmt := range stmts {
		if _, execErr := db.Pool.Exec(ctx, stmt); execErr != nil {
			t.Fatalf("running backfill: %v", execErr)
		}
	}

	got, err := db.SumOrgDebateSpendMicros(ctx, orgID, from, to)
	if err != nil {
		t.Fatalf("SumOrgDebateSpendMicros: %v", err)
	}
	if got != wantTotal {
		t.Fatalf("after backfill spend = %d, want %d — caps would be wrong for the rest of the month", got, wantTotal)
	}

	// Re-run: must be a no-op, not a doubling.
	for _, stmt := range stmts {
		if _, execErr := db.Pool.Exec(ctx, stmt); execErr != nil {
			t.Fatalf("re-running backfill: %v", execErr)
		}
	}
	again, err := db.SumOrgDebateSpendMicros(ctx, orgID, from, to)
	if err != nil {
		t.Fatalf("SumOrgDebateSpendMicros (second): %v", err)
	}
	if again != wantTotal {
		t.Errorf("backfill is not idempotent: re-running took spend %d -> %d", wantTotal, again)
	}
}
