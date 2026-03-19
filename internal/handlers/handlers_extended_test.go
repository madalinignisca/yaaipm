package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/madalin/forgedesk/internal/auth"
	"github.com/madalin/forgedesk/internal/models"
)

// --- Cost helper unit tests (unexported functions, tested directly since same package) ---

func TestParseDollarsToCents(t *testing.T) {
	tests := []struct {
		input   string
		want    int64
		wantErr bool
	}{
		{"12.50", 1250, false},
		{"$12.50", 1250, false},
		{"0.01", 1, false},
		{"100", 10000, false},
		{"100.00", 10000, false},
		{"0", 0, false},
		{"$0.99", 99, false},
		{"  $25.75  ", 2575, false},
		{"12.345", 1235, false}, // rounds to nearest cent
		{"abc", 0, true},
		{"", 0, true},
	}

	for _, tc := range tests {
		t.Run(tc.input, func(t *testing.T) {
			got, err := parseToCents(tc.input)
			if tc.wantErr {
				if err == nil {
					t.Errorf("parseToCents(%q) expected error, got %d", tc.input, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseToCents(%q) unexpected error: %v", tc.input, err)
			}
			if got != tc.want {
				t.Errorf("parseToCents(%q) = %d, want %d", tc.input, got, tc.want)
			}
		})
	}
}

func TestCurrentMonth(t *testing.T) {
	m := currentMonth()
	// Should be in YYYY-MM format
	if len(m) != 7 {
		t.Errorf("currentMonth() = %q, expected YYYY-MM format (7 chars)", m)
	}
	if m[4] != '-' {
		t.Errorf("currentMonth() = %q, expected dash at position 4", m)
	}
}

func TestParseMonth(t *testing.T) {
	tests := []struct {
		name     string
		query    string
		expected string
	}{
		{"valid month", "2025-06", "2025-06"},
		{"another valid", "2024-12", "2024-12"},
		{"empty defaults to current", "", currentMonth()},
		{"invalid format defaults to current", "2025-13-01", currentMonth()},
		{"garbage defaults to current", "notamonth", currentMonth()},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/costs?month="+tc.query, http.NoBody)
			got := parseMonth(req)
			if got != tc.expected {
				t.Errorf("parseMonth with query %q = %q, want %q", tc.query, got, tc.expected)
			}
		})
	}
}

func TestAdjacentMonths(t *testing.T) {
	tests := []struct {
		input    string
		wantPrev string
		wantNext string
	}{
		{"2025-06", "2025-05", "2025-07"},
		{"2025-01", "2024-12", "2025-02"},
		{"2025-12", "2025-11", "2026-01"},
		{"2024-03", "2024-02", "2024-04"},
	}

	for _, tc := range tests {
		t.Run(tc.input, func(t *testing.T) {
			prev, next := adjacentMonths(tc.input)
			if prev != tc.wantPrev {
				t.Errorf("adjacentMonths(%q) prev = %q, want %q", tc.input, prev, tc.wantPrev)
			}
			if next != tc.wantNext {
				t.Errorf("adjacentMonths(%q) next = %q, want %q", tc.input, next, tc.wantNext)
			}
		})
	}
}

// --- Cost handler integration tests ---

func TestProjectCosts(t *testing.T) {
	r, db, sessions, _ := setupTestRouter(t)
	cookie := createAuthenticatedUser(t, db, sessions, "costview@test.com", "superadmin")
	ctx := context.Background()

	org, _ := db.CreateOrg(ctx, "Cost Org", "cost-org")
	db.CreateProject(ctx, org.ID, "Cost Proj", "cost-proj")

	req := httptest.NewRequest(http.MethodGet, "/orgs/cost-org/projects/cost-proj/costs", http.NoBody)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d; body: %s", rec.Code, rec.Body.String())
	}
}

func TestProjectCostsWithMonth(t *testing.T) {
	r, db, sessions, _ := setupTestRouter(t)
	cookie := createAuthenticatedUser(t, db, sessions, "costmonth@test.com", "superadmin")
	ctx := context.Background()

	org, _ := db.CreateOrg(ctx, "CostM Org", "costm-org")
	db.CreateProject(ctx, org.ID, "CostM Proj", "costm-proj")

	req := httptest.NewRequest(http.MethodGet, "/orgs/costm-org/projects/costm-proj/costs?month=2025-03", http.NoBody)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
}

func TestProjectCostsForbiddenForNonMember(t *testing.T) {
	r, db, sessions, _ := setupTestRouter(t)
	cookie := createAuthenticatedUser(t, db, sessions, "costnon@test.com", "client")
	ctx := context.Background()

	org, _ := db.CreateOrg(ctx, "CostNon Org", "costnon-org")
	db.CreateProject(ctx, org.ID, "CostNon Proj", "costnon-proj")

	req := httptest.NewRequest(http.MethodGet, "/orgs/costnon-org/projects/costnon-proj/costs", http.NoBody)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	// Client not a member of the org should get 403
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", rec.Code)
	}
}

func TestAddCostItem(t *testing.T) {
	r, db, sessions, _ := setupTestRouter(t)
	cookie := createAuthenticatedUser(t, db, sessions, "costadd@test.com", "superadmin")
	ctx := context.Background()

	org, _ := db.CreateOrg(ctx, "AddCost Org", "addcost-org")
	proj, _ := db.CreateProject(ctx, org.ID, "AddCost Proj", "addcost-proj")

	form := url.Values{
		"category": {"base_fee"},
		"name":     {"Monthly Base"},
		"amount":   {"25.50"},
		"month":    {"2025-06"},
	}
	req := httptest.NewRequest(http.MethodPost, "/orgs/addcost-org/projects/addcost-proj/costs", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("expected 303, got %d; body: %s", rec.Code, rec.Body.String())
	}

	loc := rec.Header().Get("Location")
	if !strings.Contains(loc, "/costs") {
		t.Errorf("expected redirect to costs page, got %s", loc)
	}

	// Verify cost was created in DB
	costs, err := db.ListProjectCosts(ctx, proj.ID, "2025-06")
	if err != nil {
		t.Fatalf("listing costs: %v", err)
	}
	if len(costs) != 1 {
		t.Fatalf("expected 1 cost, got %d", len(costs))
	}
	if costs[0].AmountCents != 2550 {
		t.Errorf("amount = %d cents, want 2550", costs[0].AmountCents)
	}
	if costs[0].Category != "base_fee" {
		t.Errorf("category = %q, want base_fee", costs[0].Category)
	}
}

func TestAddCostItemForbiddenForClient(t *testing.T) {
	r, db, sessions, _ := setupTestRouter(t)
	cookie := createAuthenticatedUser(t, db, sessions, "costclient@test.com", "client")
	ctx := context.Background()

	org, _ := db.CreateOrg(ctx, "CostClient Org", "costclient-org")
	db.CreateProject(ctx, org.ID, "CostClient Proj", "costclient-proj")

	form := url.Values{
		"category": {"base_fee"},
		"amount":   {"10.00"},
		"month":    {"2025-06"},
	}
	req := httptest.NewRequest(http.MethodPost, "/orgs/costclient-org/projects/costclient-proj/costs", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", rec.Code)
	}
}

func TestAddCostItemMissingFields(t *testing.T) {
	r, db, sessions, _ := setupTestRouter(t)
	cookie := createAuthenticatedUser(t, db, sessions, "costmiss@test.com", "superadmin")
	ctx := context.Background()

	org, _ := db.CreateOrg(ctx, "CostMiss Org", "costmiss-org")
	db.CreateProject(ctx, org.ID, "CostMiss Proj", "costmiss-proj")

	// Missing category and amount
	form := url.Values{
		"name":  {"Something"},
		"month": {"2025-06"},
	}
	req := httptest.NewRequest(http.MethodPost, "/orgs/costmiss-org/projects/costmiss-proj/costs", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
}

func TestAddCostItemInvalidAmount(t *testing.T) {
	r, db, sessions, _ := setupTestRouter(t)
	cookie := createAuthenticatedUser(t, db, sessions, "costinv@test.com", "superadmin")
	ctx := context.Background()

	org, _ := db.CreateOrg(ctx, "CostInv Org", "costinv-org")
	db.CreateProject(ctx, org.ID, "CostInv Proj", "costinv-proj")

	form := url.Values{
		"category": {"base_fee"},
		"amount":   {"not-a-number"},
		"month":    {"2025-06"},
	}
	req := httptest.NewRequest(http.MethodPost, "/orgs/costinv-org/projects/costinv-proj/costs", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
}

func TestUpdateCostItem(t *testing.T) {
	r, db, sessions, _ := setupTestRouter(t)
	cookie := createAuthenticatedUser(t, db, sessions, "costupd@test.com", "superadmin")
	ctx := context.Background()

	org, _ := db.CreateOrg(ctx, "UpdCost Org", "updcost-org")
	proj, _ := db.CreateProject(ctx, org.ID, "UpdCost Proj", "updcost-proj")

	cost, err := db.CreateProjectCost(ctx, proj.ID, "2025-06", "base_fee", "Original", 1000)
	if err != nil {
		t.Fatalf("creating cost: %v", err)
	}

	form := url.Values{"amount": {"50.00"}}
	req := httptest.NewRequest(http.MethodPatch, "/costs/"+cost.ID, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d; body: %s", rec.Code, rec.Body.String())
	}

	// Verify updated in DB
	updated, err := db.GetProjectCost(ctx, cost.ID)
	if err != nil {
		t.Fatalf("getting cost: %v", err)
	}
	if updated.AmountCents != 5000 {
		t.Errorf("amount = %d cents, want 5000", updated.AmountCents)
	}
}

func TestUpdateCostItemForbiddenForClient(t *testing.T) {
	r, db, sessions, _ := setupTestRouter(t)
	cookie := createAuthenticatedUser(t, db, sessions, "costupdcl@test.com", "client")
	ctx := context.Background()

	org, _ := db.CreateOrg(ctx, "UpdCostCl Org", "updcostcl-org")
	proj, _ := db.CreateProject(ctx, org.ID, "UpdCostCl Proj", "updcostcl-proj")

	cost, _ := db.CreateProjectCost(ctx, proj.ID, "2025-06", "base_fee", "Item", 1000)

	form := url.Values{"amount": {"50.00"}}
	req := httptest.NewRequest(http.MethodPatch, "/costs/"+cost.ID, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", rec.Code)
	}
}

func TestUpdateCostItemInvalidAmount(t *testing.T) {
	r, db, sessions, _ := setupTestRouter(t)
	cookie := createAuthenticatedUser(t, db, sessions, "costupdba@test.com", "superadmin")
	ctx := context.Background()

	org, _ := db.CreateOrg(ctx, "UpdCostBa Org", "updcostba-org")
	proj, _ := db.CreateProject(ctx, org.ID, "UpdCostBa Proj", "updcostba-proj")

	cost, _ := db.CreateProjectCost(ctx, proj.ID, "2025-06", "base_fee", "Item", 1000)

	form := url.Values{"amount": {"garbage"}}
	req := httptest.NewRequest(http.MethodPatch, "/costs/"+cost.ID, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
}

func TestDeleteCostItem(t *testing.T) {
	r, db, sessions, _ := setupTestRouter(t)
	cookie := createAuthenticatedUser(t, db, sessions, "costdel@test.com", "superadmin")
	ctx := context.Background()

	org, _ := db.CreateOrg(ctx, "DelCost Org", "delcost-org")
	proj, _ := db.CreateProject(ctx, org.ID, "DelCost Proj", "delcost-proj")

	cost, err := db.CreateProjectCost(ctx, proj.ID, "2025-06", "base_fee", "To Delete", 2000)
	if err != nil {
		t.Fatalf("creating cost: %v", err)
	}

	req := httptest.NewRequest(http.MethodDelete, "/costs/"+cost.ID, http.NoBody)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d; body: %s", rec.Code, rec.Body.String())
	}

	// Verify HX-Redirect header is set
	hxRedirect := rec.Header().Get("Hx-Redirect")
	if hxRedirect == "" {
		t.Error("expected HX-Redirect header to be set")
	}
	if !strings.Contains(hxRedirect, "/costs") {
		t.Errorf("HX-Redirect = %q, expected to contain /costs", hxRedirect)
	}

	// Verify deleted from DB
	_, err = db.GetProjectCost(ctx, cost.ID)
	if err == nil {
		t.Error("cost should have been deleted")
	}
}

func TestDeleteCostItemForbiddenForClient(t *testing.T) {
	r, db, sessions, _ := setupTestRouter(t)
	cookie := createAuthenticatedUser(t, db, sessions, "costdelcl@test.com", "client")
	ctx := context.Background()

	org, _ := db.CreateOrg(ctx, "DelCostCl Org", "delcostcl-org")
	proj, _ := db.CreateProject(ctx, org.ID, "DelCostCl Proj", "delcostcl-proj")

	cost, _ := db.CreateProjectCost(ctx, proj.ID, "2025-06", "base_fee", "Item", 1000)

	req := httptest.NewRequest(http.MethodDelete, "/costs/"+cost.ID, http.NoBody)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", rec.Code)
	}
}

func TestDeleteCostItemNotFound(t *testing.T) {
	r, db, sessions, _ := setupTestRouter(t)
	cookie := createAuthenticatedUser(t, db, sessions, "costdelnf@test.com", "superadmin")

	req := httptest.NewRequest(http.MethodDelete, "/costs/00000000-0000-0000-0000-000000000000", http.NoBody)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rec.Code)
	}
}

// --- Reaction handler integration tests ---

func TestToggleReaction(t *testing.T) {
	r, db, sessions, _ := setupTestRouter(t)
	cookie := createAuthenticatedUser(t, db, sessions, "react@test.com", "superadmin")
	ctx := context.Background()

	org, _ := db.CreateOrg(ctx, "React Org", "react-org")
	proj, _ := db.CreateProject(ctx, org.ID, "React Proj", "react-proj")

	user, _ := db.GetUserByEmail(ctx, "react@test.com")
	ticket := &models.Ticket{
		ProjectID: proj.ID, Type: "task", Title: "React Task",
		Status: "backlog", Priority: "medium", CreatedBy: user.ID,
	}
	db.CreateTicket(ctx, ticket)

	// Toggle reaction ON
	form := url.Values{"emoji": {"\U0001F44D"}} // thumbs up
	req := httptest.NewRequest(http.MethodPost, "/reactions/ticket/"+ticket.ID, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d; body: %s", rec.Code, rec.Body.String())
	}

	// Verify the reaction was created
	groups, err := db.ListReactionGroups(ctx, "ticket", ticket.ID, user.ID)
	if err != nil {
		t.Fatalf("listing reactions: %v", err)
	}
	if len(groups) != 1 {
		t.Fatalf("expected 1 reaction group, got %d", len(groups))
	}
	if groups[0].Count != 1 {
		t.Errorf("reaction count = %d, want 1", groups[0].Count)
	}
	if !groups[0].UserReacted {
		t.Error("expected UserReacted to be true")
	}
}

func TestToggleReactionOff(t *testing.T) {
	r, db, sessions, _ := setupTestRouter(t)
	cookie := createAuthenticatedUser(t, db, sessions, "reactoff@test.com", "superadmin")
	ctx := context.Background()

	org, _ := db.CreateOrg(ctx, "ReactOff Org", "reactoff-org")
	proj, _ := db.CreateProject(ctx, org.ID, "ReactOff Proj", "reactoff-proj")

	user, _ := db.GetUserByEmail(ctx, "reactoff@test.com")
	ticket := &models.Ticket{
		ProjectID: proj.ID, Type: "task", Title: "ReactOff Task",
		Status: "backlog", Priority: "medium", CreatedBy: user.ID,
	}
	db.CreateTicket(ctx, ticket)

	emoji := "\U0001F680" // rocket

	// Toggle ON
	db.ToggleReaction(ctx, "ticket", ticket.ID, user.ID, emoji)

	// Toggle OFF via handler
	form := url.Values{"emoji": {emoji}}
	req := httptest.NewRequest(http.MethodPost, "/reactions/ticket/"+ticket.ID, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	// Verify reaction was removed
	groups, _ := db.ListReactionGroups(ctx, "ticket", ticket.ID, user.ID)
	for _, g := range groups {
		if g.Emoji == emoji {
			t.Errorf("reaction with emoji %q should have been toggled off", emoji)
		}
	}
}

func TestToggleReactionInvalidTargetType(t *testing.T) {
	r, db, sessions, _ := setupTestRouter(t)
	cookie := createAuthenticatedUser(t, db, sessions, "reactinv@test.com", "superadmin")

	form := url.Values{"emoji": {"\U0001F44D"}}
	req := httptest.NewRequest(http.MethodPost, "/reactions/invalid/some-id", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
}

func TestToggleReactionInvalidEmoji(t *testing.T) {
	r, db, sessions, _ := setupTestRouter(t)
	cookie := createAuthenticatedUser(t, db, sessions, "reactbad@test.com", "superadmin")
	ctx := context.Background()

	org, _ := db.CreateOrg(ctx, "ReactBad Org", "reactbad-org")
	proj, _ := db.CreateProject(ctx, org.ID, "ReactBad Proj", "reactbad-proj")

	user, _ := db.GetUserByEmail(ctx, "reactbad@test.com")
	ticket := &models.Ticket{
		ProjectID: proj.ID, Type: "task", Title: "ReactBad Task",
		Status: "backlog", Priority: "medium", CreatedBy: user.ID,
	}
	db.CreateTicket(ctx, ticket)

	form := url.Values{"emoji": {"X"}} // not in allowed list
	req := httptest.NewRequest(http.MethodPost, "/reactions/ticket/"+ticket.ID, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid emoji, got %d", rec.Code)
	}
}

func TestToggleReactionOnComment(t *testing.T) {
	r, db, sessions, _ := setupTestRouter(t)
	cookie := createAuthenticatedUser(t, db, sessions, "reactcmt@test.com", "superadmin")
	ctx := context.Background()

	org, _ := db.CreateOrg(ctx, "ReactCmt Org", "reactcmt-org")
	proj, _ := db.CreateProject(ctx, org.ID, "ReactCmt Proj", "reactcmt-proj")

	user, _ := db.GetUserByEmail(ctx, "reactcmt@test.com")
	ticket := &models.Ticket{
		ProjectID: proj.ID, Type: "task", Title: "ReactCmt Task",
		Status: "backlog", Priority: "medium", CreatedBy: user.ID,
	}
	db.CreateTicket(ctx, ticket)

	comment, err := db.CreateComment(ctx, ticket.ID, &user.ID, nil, "Test comment for reaction")
	if err != nil {
		t.Fatalf("creating comment: %v", err)
	}

	form := url.Values{"emoji": {"\U0001F389"}} // party popper
	req := httptest.NewRequest(http.MethodPost, "/reactions/comment/"+comment.ID, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d; body: %s", rec.Code, rec.Body.String())
	}

	groups, _ := db.ListReactionGroups(ctx, "comment", comment.ID, user.ID)
	if len(groups) != 1 {
		t.Fatalf("expected 1 reaction group on comment, got %d", len(groups))
	}
}

// --- Account handler integration tests ---

func TestAccountSettingsPage(t *testing.T) {
	r, db, sessions, _ := setupTestRouter(t)
	cookie := createAuthenticatedUser(t, db, sessions, "acctpage@test.com", "superadmin")

	req := httptest.NewRequest(http.MethodGet, "/account/settings", http.NoBody)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "Account Settings") && !strings.Contains(body, "account") {
		t.Error("should render account settings page")
	}
}

func TestChangePasswordSuccess(t *testing.T) {
	r, db, sessions, _ := setupTestRouter(t)
	cookie := createAuthenticatedUser(t, db, sessions, "chgpwd@test.com", "superadmin")

	form := url.Values{
		"current_password": {"TestPassword123!"},
		"new_password":     {"NewSecurePass123!"},
		"confirm_password": {"NewSecurePass123!"},
	}
	req := httptest.NewRequest(http.MethodPost, "/account/password", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "Password updated successfully") {
		t.Error("should show success message")
	}

	// Verify new password works
	ctx := context.Background()
	user, _ := db.GetUserByEmail(ctx, "chgpwd@test.com")
	ok, err := auth.VerifyPassword("NewSecurePass123!", user.PasswordHash)
	if err != nil || !ok {
		t.Error("new password should verify successfully")
	}
}

func TestChangePasswordWrongOldPassword(t *testing.T) {
	r, db, sessions, _ := setupTestRouter(t)
	cookie := createAuthenticatedUser(t, db, sessions, "chgpwdwr@test.com", "superadmin")

	form := url.Values{
		"current_password": {"WrongOldPassword1"},
		"new_password":     {"NewSecurePass123!"},
		"confirm_password": {"NewSecurePass123!"},
	}
	req := httptest.NewRequest(http.MethodPost, "/account/password", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "Current password is incorrect") {
		t.Errorf("should show incorrect password error, got body containing: %s", body)
	}
}

func TestChangePasswordTooShort(t *testing.T) {
	r, db, sessions, _ := setupTestRouter(t)
	cookie := createAuthenticatedUser(t, db, sessions, "chgpwdsh@test.com", "superadmin")

	form := url.Values{
		"current_password": {"TestPassword123!"},
		"new_password":     {"short"},
		"confirm_password": {"short"},
	}
	req := httptest.NewRequest(http.MethodPost, "/account/password", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "at least 12 characters") {
		t.Errorf("should show min length error, got body containing: %s", body)
	}
}

func TestChangePasswordMismatch(t *testing.T) {
	r, db, sessions, _ := setupTestRouter(t)
	cookie := createAuthenticatedUser(t, db, sessions, "chgpwdmm@test.com", "superadmin")

	form := url.Values{
		"current_password": {"TestPassword123!"},
		"new_password":     {"NewSecurePass123!"},
		"confirm_password": {"DifferentPass123!"},
	}
	req := httptest.NewRequest(http.MethodPost, "/account/password", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "do not match") {
		t.Errorf("should show mismatch error, got body containing: %s", body)
	}
}

func TestChangePasswordMissingFields(t *testing.T) {
	r, db, sessions, _ := setupTestRouter(t)
	cookie := createAuthenticatedUser(t, db, sessions, "chgpwdmf@test.com", "superadmin")

	form := url.Values{
		"current_password": {""},
		"new_password":     {""},
		"confirm_password": {""},
	}
	req := httptest.NewRequest(http.MethodPost, "/account/password", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "All fields are required") {
		t.Errorf("should show required fields error, got body containing: %s", body)
	}
}

func TestChangeEmailSuccess(t *testing.T) {
	r, db, sessions, _ := setupTestRouter(t)
	cookie := createAuthenticatedUser(t, db, sessions, "chgemail@test.com", "superadmin")

	form := url.Values{
		"new_email": {"newemail@test.com"},
		"password":  {"TestPassword123!"},
	}
	req := httptest.NewRequest(http.MethodPost, "/account/email", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "Email updated successfully") {
		t.Errorf("should show success message, got body containing: %s", body)
	}

	// Verify email was updated in DB
	ctx := context.Background()
	user, err := db.GetUserByEmail(ctx, "newemail@test.com")
	if err != nil {
		t.Fatalf("should find user with new email: %v", err)
	}
	if user.Email != "newemail@test.com" {
		t.Errorf("email = %q, want newemail@test.com", user.Email)
	}
}

func TestChangeEmailDuplicate(t *testing.T) {
	r, db, sessions, _ := setupTestRouter(t)
	ctx := context.Background()

	// Create two users
	cookie := createAuthenticatedUser(t, db, sessions, "chgemaildup@test.com", "superadmin")
	hash, _ := auth.HashPassword("AnotherPassword1")
	db.CreateUser(ctx, "existing@test.com", hash, "Existing User", "client")

	form := url.Values{
		"new_email": {"existing@test.com"},
		"password":  {"TestPassword123!"},
	}
	req := httptest.NewRequest(http.MethodPost, "/account/email", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "already in use") {
		t.Errorf("should show duplicate email error, got body containing: %s", body)
	}
}

func TestChangeEmailWrongPassword(t *testing.T) {
	r, db, sessions, _ := setupTestRouter(t)
	cookie := createAuthenticatedUser(t, db, sessions, "chgemailwp@test.com", "superadmin")

	form := url.Values{
		"new_email": {"updated@test.com"},
		"password":  {"WrongPassword123!"},
	}
	req := httptest.NewRequest(http.MethodPost, "/account/email", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "Password is incorrect") {
		t.Errorf("should show incorrect password error, got body containing: %s", body)
	}
}

func TestChangeEmailMissingFields(t *testing.T) {
	r, db, sessions, _ := setupTestRouter(t)
	cookie := createAuthenticatedUser(t, db, sessions, "chgemailmf@test.com", "superadmin")

	form := url.Values{
		"new_email": {""},
		"password":  {""},
	}
	req := httptest.NewRequest(http.MethodPost, "/account/email", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "Email and password are required") {
		t.Errorf("should show required fields error, got body containing: %s", body)
	}
}
