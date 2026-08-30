package models

import (
	"context"
	"testing"
	"time"

	"github.com/madalin/forgedesk/internal/testutil"
)

// TestUndoDoesNotReclaimBudgetHeadroom pins #129.
//
// Budget enforcement sums the cost columns on live round rows, and undo
// hard-deletes rounds. The provider call already happened and the invoice is
// real, so deleting the row must not reduce what the monthly cap counts —
// otherwise an org near its cap buys back headroom for money it has spent.
//
// The assertion is deliberately independent of HOW that is fixed (ledger,
// soft-delete, accumulator): spend observed for the month must not go down
// because a user undid something.
func TestUndoDoesNotReclaimBudgetHeadroom(t *testing.T) {
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
	inRange := from.Add(time.Hour)

	// Three rounds really billed this month: 1.00 + (0.50 + 0.25) + 0.10.
	seedRoundAt(t, db, deb.ID, userID, 1, int64Ptr(1_000_000), nil, "accepted", inRange)
	seedRoundAt(t, db, deb.ID, userID, 2, int64Ptr(500_000), int64Ptr(250_000), "accepted", inRange)
	seedRoundAt(t, db, deb.ID, userID, 3, int64Ptr(100_000), nil, "accepted", inRange)

	spentBefore, err := db.SumOrgDebateSpendMicros(ctx, orgID, from, to)
	if err != nil {
		t.Fatalf("SumOrgDebateSpendMicros (before): %v", err)
	}
	const wantBefore = int64(1_000_000 + 500_000 + 250_000 + 100_000)
	if spentBefore != wantBefore {
		t.Fatalf("spend before undo = %d, want %d", spentBefore, wantBefore)
	}

	// Undo rounds 2 and 3 — a legitimate, expected user action.
	tx, err := db.Pool.Begin(ctx)
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if uErr := db.UndoRoundsFromTx(ctx, tx, deb.ID, 2); uErr != nil {
		_ = tx.Rollback(ctx)
		t.Fatalf("UndoRoundsFromTx: %v", uErr)
	}
	if cErr := tx.Commit(ctx); cErr != nil {
		t.Fatalf("Commit: %v", cErr)
	}

	spentAfter, err := db.SumOrgDebateSpendMicros(ctx, orgID, from, to)
	if err != nil {
		t.Fatalf("SumOrgDebateSpendMicros (after): %v", err)
	}

	if spentAfter != spentBefore {
		t.Errorf(
			"undo reclaimed budget headroom: spend went %d -> %d (gave back %d micros already paid to the provider)",
			spentBefore, spentAfter, spentBefore-spentAfter,
		)
	}
}
