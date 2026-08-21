package models

import (
	"context"
	"testing"

	"github.com/madalin/forgedesk/internal/testutil"
)

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
