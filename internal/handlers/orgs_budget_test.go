package handlers

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/madalin/forgedesk/internal/models"
)

// TestParseUSDCents covers the accept/reject grammar from the plan's
// test matrix. parseUSDCents deliberately does NOT reuse costs.go's
// parseToCents: that helper uses ParseFloat + math.Round, which silently
// accepts scientific notation / NaN / Inf and rounds instead of
// rejecting extra decimal places — wrong for a money control per spec §6.
func TestParseUSDCents(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    int64
		wantErr error
	}{
		{"zero", "0", 0, nil},
		{"whole dollars", "25", 2500, nil},
		{"one decimal", "25.5", 2550, nil},
		{"two decimals", "25.50", 2550, nil},
		{"leading dollar sign", "$25.00", 2500, nil},
		{"surrounding whitespace", " 25.00 ", 2500, nil},
		{"at the max bound", "1000000", 100_000_000, nil},

		{"empty string", "", 0, errBudgetNotANumber},
		{"negative", "-1", 0, errBudgetNotANumber},
		{"three decimals rejected not rounded", "25.555", 0, errBudgetTooManyDecimals},
		{"scientific notation rejected", "1e3", 0, errBudgetNotANumber},
		{"non-numeric", "abc", 0, errBudgetNotANumber},
		{"thousands separator rejected", "1,000", 0, errBudgetNotANumber},
		{"NaN literal rejected", "NaN", 0, errBudgetNotANumber},
		{"Inf literal rejected", "Inf", 0, errBudgetNotANumber},
		{"over the max bound", "1000000.01", 0, errBudgetOutOfRange},
		{"trailing dot with no digits", "25.", 0, errBudgetNotANumber},
		{"leading dot with no whole part", ".50", 0, errBudgetNotANumber},
		{"leading plus rejected", "+25", 0, errBudgetNotANumber},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseUSDCents(tc.input)
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("parseUSDCents(%q) err = %v, want %v", tc.input, err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseUSDCents(%q) unexpected error: %v", tc.input, err)
			}
			if got != tc.want {
				t.Fatalf("parseUSDCents(%q) = %d, want %d", tc.input, got, tc.want)
			}
		})
	}
}

func TestFormatUSDCents(t *testing.T) {
	tests := []struct {
		cents int64
		want  string
	}{
		{0, "0.00"},
		{5, "0.05"},
		{2500, "25.00"},
		{2550, "25.50"},
		{100_000_000, "1000000.00"},
		// Negative input should never happen (callers only ever format a
		// validated non-negative cap), but %02d of a negative remainder
		// renders "0.-5" rather than "-0.05" if unguarded — this is a
		// bug-catcher, not an expected code path.
		{-5, "-0.05"},
	}
	for _, tc := range tests {
		got := formatUSDCents(tc.cents)
		if got != tc.want {
			t.Errorf("formatUSDCents(%d) = %q, want %q", tc.cents, got, tc.want)
		}
	}
}

// seedBudgetTestOrg creates an org and returns its slug/ID plus a
// membership-role setter so each role-matrix case can attach a fresh
// user at the exact role under test without cross-contaminating others.
func seedBudgetTestOrg(t *testing.T, db *models.DB, name, slug string) *models.Organization {
	t.Helper()
	org, err := db.CreateOrg(context.Background(), name, slug)
	if err != nil {
		t.Fatalf("CreateOrg: %v", err)
	}
	return org
}

func postBudget(t *testing.T, r *chi.Mux, cookie *http.Cookie, slug, amount string) *httptest.ResponseRecorder {
	t.Helper()
	form := url.Values{"monthly_budget_cents": {amount}}
	req := httptest.NewRequest(http.MethodPost, "/orgs/"+slug+"/settings/budget", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if cookie != nil {
		req.AddCookie(cookie)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

// TestUpdateMonthlyBudget_RoleMatrix exercises every role this endpoint
// treats differently, through the real router (not a direct handler
// call) so routing/middleware wiring is also under test. Unlike
// UpdateAIMargin, this endpoint is NOT staff-only (spec §6) — it is the
// client's own spend control.
func TestUpdateMonthlyBudget_RoleMatrix(t *testing.T) {
	r, db, sessions, _ := setupTestRouter(t)
	ctx := context.Background()

	// First registered user is auto-promoted to superadmin — burn that
	// slot on a throwaway account so later role assertions are exact.
	createAuthenticatedUser(t, db, sessions, "first-sa@test.com", "superadmin")

	org := seedBudgetTestOrg(t, db, "Role Matrix Org", "role-matrix-org")

	tests := []struct {
		name       string
		email      string
		platform   string // platform (global) role
		orgRole    string // "" = not a member at all
		wantStatus int
	}{
		{"member cannot set", "member@test.com", "client", "member", http.StatusForbidden},
		{"owner can set", "owner@test.com", "client", "owner", http.StatusSeeOther},
		{"admin can set", "admin@test.com", "client", "admin", http.StatusSeeOther},
		{"staff on foreign org can set", "staff@test.com", "staff", "", http.StatusSeeOther},
		{"non-member cannot set", "outsider@test.com", "client", "", http.StatusForbidden},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cookie := createAuthenticatedUser(t, db, sessions, tc.email, tc.platform)
			if tc.orgRole != "" {
				var userID string
				if err := db.Pool.QueryRow(ctx, `SELECT id FROM users WHERE email = $1`, tc.email).Scan(&userID); err != nil {
					t.Fatalf("look up user: %v", err)
				}
				if err := db.AddOrgMember(ctx, userID, org.ID, tc.orgRole); err != nil {
					t.Fatalf("AddOrgMember: %v", err)
				}
			}

			rec := postBudget(t, r, cookie, org.Slug, "10.00")
			if rec.Code != tc.wantStatus {
				t.Errorf("%s: status = %d, want %d (body: %s)", tc.name, rec.Code, tc.wantStatus, rec.Body.String())
			}
		})
	}
}

// TestUpdateMonthlyBudget_CrossOrgSubstitution proves the org is derived
// ONLY from the trusted route slug: a member of org A must not be able
// to set org B's budget by any means (there is no org_id form field to
// even attempt this with, but the test pins the outcome regardless).
func TestUpdateMonthlyBudget_CrossOrgSubstitution(t *testing.T) {
	r, db, sessions, _ := setupTestRouter(t)
	ctx := context.Background()

	createAuthenticatedUser(t, db, sessions, "first-sa2@test.com", "superadmin")

	orgA := seedBudgetTestOrg(t, db, "Org A", "cross-org-a")
	orgB := seedBudgetTestOrg(t, db, "Org B", "cross-org-b")

	cookie := createAuthenticatedUser(t, db, sessions, "member-a@test.com", "client")
	var userID string
	if err := db.Pool.QueryRow(ctx, `SELECT id FROM users WHERE email = 'member-a@test.com'`).Scan(&userID); err != nil {
		t.Fatalf("look up user: %v", err)
	}
	if err := db.AddOrgMember(ctx, userID, orgA.ID, "owner"); err != nil {
		t.Fatalf("AddOrgMember: %v", err)
	}

	rec := postBudget(t, r, cookie, orgB.Slug, "10.00")
	if rec.Code != http.StatusForbidden {
		t.Errorf("member of org A posting to org B's budget: status = %d, want 403", rec.Code)
	}

	orgBAfter, err := db.GetOrgByID(ctx, orgB.ID)
	if err != nil {
		t.Fatalf("GetOrgByID: %v", err)
	}
	if orgBAfter.MonthlyBudgetCents != nil {
		t.Errorf("org B's budget was set to %v despite the 403 — cross-tenant write succeeded", orgBAfter.MonthlyBudgetCents)
	}
}

// TestUpdateMonthlyBudget_EmptyClears proves an empty field maps to
// NULL (unlimited), not to a parse error or a stored 0.
func TestUpdateMonthlyBudget_EmptyClears(t *testing.T) {
	r, db, sessions, _ := setupTestRouter(t)
	ctx := context.Background()

	createAuthenticatedUser(t, db, sessions, "first-sa3@test.com", "superadmin")
	org := seedBudgetTestOrg(t, db, "Empty Clears Org", "empty-clears-org")

	cookie := createAuthenticatedUser(t, db, sessions, "owner-clear@test.com", "client")
	var userID string
	if err := db.Pool.QueryRow(ctx, `SELECT id FROM users WHERE email = 'owner-clear@test.com'`).Scan(&userID); err != nil {
		t.Fatalf("look up user: %v", err)
	}
	if err := db.AddOrgMember(ctx, userID, org.ID, "owner"); err != nil {
		t.Fatalf("AddOrgMember: %v", err)
	}

	// Set first, so clearing is a real transition rather than a no-op.
	if rec := postBudget(t, r, cookie, org.Slug, "50.00"); rec.Code != http.StatusSeeOther {
		t.Fatalf("initial set: status = %d, want 303", rec.Code)
	}
	if rec := postBudget(t, r, cookie, org.Slug, ""); rec.Code != http.StatusSeeOther {
		t.Fatalf("clear: status = %d, want 303, body: %s", rec.Code, rec.Body.String())
	}

	org2, err := db.GetOrgByID(ctx, org.ID)
	if err != nil {
		t.Fatalf("GetOrgByID: %v", err)
	}
	if org2.MonthlyBudgetCents != nil {
		t.Errorf("MonthlyBudgetCents after empty submit = %v, want nil", org2.MonthlyBudgetCents)
	}
}

// TestUpdateMonthlyBudget_InvalidAmountRejected proves a malformed
// amount is a 400, not silently coerced or a 500.
func TestUpdateMonthlyBudget_InvalidAmountRejected(t *testing.T) {
	r, db, sessions, _ := setupTestRouter(t)
	ctx := context.Background()

	createAuthenticatedUser(t, db, sessions, "first-sa4@test.com", "superadmin")
	org := seedBudgetTestOrg(t, db, "Invalid Amount Org", "invalid-amount-org")

	cookie := createAuthenticatedUser(t, db, sessions, "owner-invalid@test.com", "client")
	var userID string
	if err := db.Pool.QueryRow(ctx, `SELECT id FROM users WHERE email = 'owner-invalid@test.com'`).Scan(&userID); err != nil {
		t.Fatalf("look up user: %v", err)
	}
	if err := db.AddOrgMember(ctx, userID, org.ID, "owner"); err != nil {
		t.Fatalf("AddOrgMember: %v", err)
	}

	rec := postBudget(t, r, cookie, org.Slug, "not-a-number")
	if rec.Code != http.StatusBadRequest {
		t.Errorf("invalid amount: status = %d, want 400", rec.Code)
	}
}

// TestUpdateMonthlyBudget_UnknownOrg404sDistinctFromDBError pins that
// UpdateAIMargin's known bug (collapsing not-found and infra-error into
// one branch) is NOT copied here (plan step 5).
func TestUpdateMonthlyBudget_UnknownOrg(t *testing.T) {
	r, db, sessions, _ := setupTestRouter(t)

	cookie := createAuthenticatedUser(t, db, sessions, "first-sa5@test.com", "superadmin")

	rec := postBudget(t, r, cookie, "no-such-org-at-all", "10.00")
	if rec.Code != http.StatusNotFound {
		t.Errorf("unknown org slug: status = %d, want 404", rec.Code)
	}
}
