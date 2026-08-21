package models

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/madalin/forgedesk/internal/testutil"
)

func int64Ptr(v int64) *int64 { return &v }

// TestOrgMonthlyBudgetColumn_DefaultsNil proves the new column round-trips
// through the shared orgColumns/scanOrg pair used by all nine org queries.
// A mismatched column order is a RUNTIME error (wrong field gets the wrong
// value), not a compile error — this is the cheapest possible tripwire for
// that class of bug (plan step 1).
func TestOrgMonthlyBudgetColumn_DefaultsNil(t *testing.T) {
	pool := testutil.SetupTestDB(t)
	db := NewDB(pool)
	ctx := context.Background()

	org, err := db.CreateOrg(ctx, "Budget Org", "budget-org")
	if err != nil {
		t.Fatalf("CreateOrg: %v", err)
	}
	if org.MonthlyBudgetCents != nil {
		t.Fatalf("MonthlyBudgetCents on create = %v, want nil (unlimited by default)", org.MonthlyBudgetCents)
	}

	// Round-trip through GetOrgBySlug to prove the scan (not just the
	// struct default) picks up the column in the right position.
	fetched, err := db.GetOrgBySlug(ctx, "budget-org")
	if err != nil {
		t.Fatalf("GetOrgBySlug: %v", err)
	}
	if fetched.MonthlyBudgetCents != nil {
		t.Fatalf("MonthlyBudgetCents after fetch = %v, want nil", fetched.MonthlyBudgetCents)
	}
}

// seedBudgetOrgWithUser creates one org + one owning user, the minimal
// fixture every UpdateOrgMonthlyBudget test needs.
func seedBudgetOrgWithUser(t *testing.T, db *DB) (orgID, userID string) {
	t.Helper()
	ctx := context.Background()

	user, err := db.CreateUser(ctx, t.Name()+"@example.com", "$argon2id$fakehash", "Budget Test User", "client")
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	org, err := db.CreateOrgWithOwnerTx(ctx, user.ID, "Org "+t.Name(), "org-"+t.Name(), OrgRoleOwner)
	if err != nil {
		t.Fatalf("CreateOrgWithOwnerTx: %v", err)
	}
	return org.ID, user.ID
}

// TestUpdateOrgMonthlyBudget_SetAndClear covers the round-trip and the
// audit row's first-change shape (old_cents IS NULL — proves the SELECT
// ... FOR UPDATE inside the tx reads the pre-image, not a stale default).
func TestUpdateOrgMonthlyBudget_SetAndClear(t *testing.T) {
	pool := testutil.SetupTestDB(t)
	db := NewDB(pool)
	ctx := context.Background()

	orgID, userID := seedBudgetOrgWithUser(t, db)

	if err := db.UpdateOrgMonthlyBudget(ctx, orgID, userID, int64Ptr(5000)); err != nil {
		t.Fatalf("UpdateOrgMonthlyBudget(set): %v", err)
	}
	org, err := db.GetOrgByID(ctx, orgID)
	if err != nil {
		t.Fatalf("GetOrgByID: %v", err)
	}
	if org.MonthlyBudgetCents == nil || *org.MonthlyBudgetCents != 5000 {
		t.Fatalf("MonthlyBudgetCents = %v, want 5000", org.MonthlyBudgetCents)
	}

	var oldCents, newCents *int64
	var changedBy *string
	if err := pool.QueryRow(ctx,
		`SELECT old_cents, new_cents, changed_by FROM org_budget_changes WHERE org_id = $1`,
		orgID).Scan(&oldCents, &newCents, &changedBy); err != nil {
		t.Fatalf("querying audit row: %v", err)
	}
	if oldCents != nil {
		t.Fatalf("first change old_cents = %v, want nil", oldCents)
	}
	if newCents == nil || *newCents != 5000 {
		t.Fatalf("first change new_cents = %v, want 5000", newCents)
	}
	if changedBy == nil || *changedBy != userID {
		t.Fatalf("changed_by = %v, want %s", changedBy, userID)
	}

	// Clear back to unlimited.
	if err := db.UpdateOrgMonthlyBudget(ctx, orgID, userID, nil); err != nil {
		t.Fatalf("UpdateOrgMonthlyBudget(clear): %v", err)
	}
	org2, err := db.GetOrgByID(ctx, orgID)
	if err != nil {
		t.Fatalf("GetOrgByID after clear: %v", err)
	}
	if org2.MonthlyBudgetCents != nil {
		t.Fatalf("MonthlyBudgetCents after clear = %v, want nil", org2.MonthlyBudgetCents)
	}

	var count int
	if err := pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM org_budget_changes WHERE org_id = $1`, orgID).Scan(&count); err != nil {
		t.Fatalf("counting audit rows: %v", err)
	}
	if count != 2 {
		t.Fatalf("audit row count = %d, want 2 (one per successful update)", count)
	}

	// Second change's old_cents must reflect the prior value, not NULL again.
	var secondOld *int64
	if err := pool.QueryRow(ctx,
		`SELECT old_cents FROM org_budget_changes WHERE org_id = $1 ORDER BY created_at DESC LIMIT 1`,
		orgID).Scan(&secondOld); err != nil {
		t.Fatalf("querying second audit row: %v", err)
	}
	if secondOld == nil || *secondOld != 5000 {
		t.Fatalf("second change old_cents = %v, want 5000", secondOld)
	}
}

// TestUpdateOrgMonthlyBudget_UnknownOrg proves the FOR UPDATE SELECT's
// ErrNoRows maps to ErrOrgNotFound AND that no audit row leaks out of a
// rolled-back transaction — the whole point of doing this atomically.
func TestUpdateOrgMonthlyBudget_UnknownOrg(t *testing.T) {
	pool := testutil.SetupTestDB(t)
	db := NewDB(pool)
	ctx := context.Background()

	_, userID := seedBudgetOrgWithUser(t, db)
	bogusOrgID := uuid.NewString()

	err := db.UpdateOrgMonthlyBudget(ctx, bogusOrgID, userID, int64Ptr(100))
	if !errors.Is(err, ErrOrgNotFound) {
		t.Fatalf("UpdateOrgMonthlyBudget(unknown org) err = %v, want ErrOrgNotFound", err)
	}

	var count int
	if err := pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM org_budget_changes WHERE org_id = $1`, bogusOrgID).Scan(&count); err != nil {
		t.Fatalf("counting audit rows: %v", err)
	}
	if count != 0 {
		t.Fatalf("audit rows for unknown org = %d, want 0 (failed update must not leave a trail)", count)
	}
}

// TestUpdateOrgMonthlyBudget_OutOfRange proves the model-layer setter
// rejects the same upper bound the handler does (spec §6 — "enforced in
// the setter as well as the parser", so a caller bypassing the handler
// still cannot persist a value that overflows cap*microsPerCent).
func TestUpdateOrgMonthlyBudget_OutOfRange(t *testing.T) {
	pool := testutil.SetupTestDB(t)
	db := NewDB(pool)
	ctx := context.Background()

	orgID, userID := seedBudgetOrgWithUser(t, db)

	err := db.UpdateOrgMonthlyBudget(ctx, orgID, userID, int64Ptr(100_000_001))
	if !errors.Is(err, ErrBudgetOutOfRange) {
		t.Fatalf("UpdateOrgMonthlyBudget(over max) err = %v, want ErrBudgetOutOfRange", err)
	}

	// The bound itself must be accepted (off-by-one check on the guard).
	if err := db.UpdateOrgMonthlyBudget(ctx, orgID, userID, int64Ptr(100_000_000)); err != nil {
		t.Fatalf("UpdateOrgMonthlyBudget(at max) unexpected error: %v", err)
	}

	var count int
	if err := pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM org_budget_changes WHERE org_id = $1`, orgID).Scan(&count); err != nil {
		t.Fatalf("counting audit rows: %v", err)
	}
	if count != 1 {
		t.Fatalf("audit rows = %d, want 1 (only the successful at-max update)", count)
	}
}

// TestOrgMonthlyBudgetCents_NegativeCheckConstraint proves the DB CHECK
// blocks a negative value even if some future caller bypasses both the
// handler parser and UpdateOrgMonthlyBudget's own range guard entirely.
func TestOrgMonthlyBudgetCents_NegativeCheckConstraint(t *testing.T) {
	pool := testutil.SetupTestDB(t)
	db := NewDB(pool)
	ctx := context.Background()

	orgID, _ := seedBudgetOrgWithUser(t, db)

	_, err := pool.Exec(ctx,
		`UPDATE organizations SET monthly_budget_cents = -1 WHERE id = $1`, orgID)
	if err == nil {
		t.Fatal("raw UPDATE with monthly_budget_cents = -1 succeeded, want CHECK constraint violation")
	}
}
