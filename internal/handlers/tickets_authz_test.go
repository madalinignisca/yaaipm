package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/madalin/forgedesk/internal/models"
)

// TestCrossTenantWritesBlocked locks in the fix for issue #25.
// A client user belonging only to Org A must not be able to mutate resources
// that live under Org B's project, even by supplying the foreign IDs directly.
// The expected server response is 404 (Not Found) so we do not leak existence
// of the foreign resource — matching the read-side pattern in TicketDetail.
func TestCrossTenantWritesBlocked(t *testing.T) {
	r, db, sessions, _ := setupTestRouter(t)
	ctx := context.Background()

	// Org A with client "alice"
	orgA, err := db.CreateOrg(ctx, "Alpha", "alpha")
	if err != nil {
		t.Fatalf("create orgA: %v", err)
	}
	projA, err := db.CreateProject(ctx, orgA.ID, "Alpha Proj", "alpha-proj")
	if err != nil {
		t.Fatalf("create projA: %v", err)
	}
	aliceCookie := createAuthenticatedUser(t, db, sessions, "alice@test.com", "client")
	alice, _ := db.GetUserByEmail(ctx, "alice@test.com")
	if err = db.AddOrgMember(ctx, alice.ID, orgA.ID, "member"); err != nil {
		t.Fatalf("add alice to orgA: %v", err)
	}
	_ = projA // orgA project only exists to make alice a legitimate tenant

	// Org B — alice is NOT a member. Seed a ticket and a comment owned by Org B.
	orgB, err := db.CreateOrg(ctx, "Bravo", "bravo")
	if err != nil {
		t.Fatalf("create orgB: %v", err)
	}
	projB, err := db.CreateProject(ctx, orgB.ID, "Bravo Proj", "bravo-proj")
	if err != nil {
		t.Fatalf("create projB: %v", err)
	}
	foreignTicket := &models.Ticket{
		ProjectID: projB.ID,
		Type:      "task",
		Title:     "Secret",
		Status:    "backlog",
		Priority:  "medium",
		CreatedBy: alice.ID, // author doesn't matter for the test
	}
	if err = db.CreateTicket(ctx, foreignTicket); err != nil {
		t.Fatalf("create foreign ticket: %v", err)
	}
	foreignComment, err := db.CreateComment(ctx, foreignTicket.ID, &alice.ID, nil, "seeded")
	if err != nil {
		t.Fatalf("create foreign comment: %v", err)
	}

	// Count tickets in projB before tests so we can assert no extras were created.
	countTicketsInProjB := func(t *testing.T) int {
		t.Helper()
		var n int
		if err := db.Pool.QueryRow(ctx,
			`SELECT COUNT(*) FROM tickets WHERE project_id = $1`, projB.ID).Scan(&n); err != nil {
			t.Fatalf("count tickets: %v", err)
		}
		return n
	}
	initialTicketCount := countTicketsInProjB(t)

	t.Run("CreateTicket into foreign project", func(t *testing.T) {
		form := url.Values{
			"project_id": {projB.ID},
			"title":      {"evil"},
			"type":       {"task"},
		}
		req := httptest.NewRequest(http.MethodPost, "/tickets", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.AddCookie(aliceCookie)
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)

		if rec.Code != http.StatusNotFound {
			t.Errorf("expected 404 Not Found, got %d: %s", rec.Code, rec.Body.String())
		}
		if got := countTicketsInProjB(t); got != initialTicketCount {
			t.Errorf("ticket count in foreign project changed: want %d, got %d", initialTicketCount, got)
		}
	})

	t.Run("UpdateTicket on foreign ticket", func(t *testing.T) {
		form := url.Values{"title": {"hijacked"}}
		req := httptest.NewRequest(http.MethodPut, "/tickets/"+foreignTicket.ID, strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.AddCookie(aliceCookie)
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)

		if rec.Code != http.StatusNotFound {
			t.Errorf("expected 404, got %d: %s", rec.Code, rec.Body.String())
		}
		got, err := db.GetTicket(ctx, foreignTicket.ID)
		if err != nil {
			t.Fatalf("reload ticket: %v", err)
		}
		if got.Title != "Secret" {
			t.Errorf("ticket title mutated: want Secret, got %q", got.Title)
		}
	})

	t.Run("UpdateStatus on foreign ticket", func(t *testing.T) {
		form := url.Values{"status": {"done"}}
		req := httptest.NewRequest(http.MethodPatch, "/tickets/"+foreignTicket.ID+"/status", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.AddCookie(aliceCookie)
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)

		if rec.Code != http.StatusNotFound {
			t.Errorf("expected 404, got %d: %s", rec.Code, rec.Body.String())
		}
		got, _ := db.GetTicket(ctx, foreignTicket.ID)
		if got.Status != "backlog" {
			t.Errorf("status mutated: want backlog, got %q", got.Status)
		}
	})

	t.Run("CreateComment on foreign ticket", func(t *testing.T) {
		form := url.Values{"body": {"spam"}}
		req := httptest.NewRequest(http.MethodPost, "/tickets/"+foreignTicket.ID+"/comments", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.AddCookie(aliceCookie)
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)

		if rec.Code != http.StatusNotFound {
			t.Errorf("expected 404, got %d: %s", rec.Code, rec.Body.String())
		}
		comments, _ := db.ListComments(ctx, foreignTicket.ID)
		for _, c := range comments {
			if c.BodyMarkdown == "spam" {
				t.Error("spam comment was written to foreign ticket")
			}
		}
	})

	t.Run("ToggleReaction on foreign ticket", func(t *testing.T) {
		form := url.Values{"emoji": {"\U0001F44D"}}
		req := httptest.NewRequest(http.MethodPost, "/reactions/ticket/"+foreignTicket.ID, strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.AddCookie(aliceCookie)
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)

		if rec.Code != http.StatusNotFound {
			t.Errorf("expected 404, got %d: %s", rec.Code, rec.Body.String())
		}
		var count int
		_ = db.Pool.QueryRow(ctx,
			`SELECT COUNT(*) FROM reactions WHERE target_type = 'ticket' AND target_id = $1`,
			foreignTicket.ID).Scan(&count)
		if count != 0 {
			t.Errorf("reaction was recorded on foreign ticket: count=%d", count)
		}
	})

	t.Run("ToggleReaction on foreign comment", func(t *testing.T) {
		form := url.Values{"emoji": {"\U0001F44D"}}
		req := httptest.NewRequest(http.MethodPost, "/reactions/comment/"+foreignComment.ID, strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.AddCookie(aliceCookie)
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)

		if rec.Code != http.StatusNotFound {
			t.Errorf("expected 404, got %d: %s", rec.Code, rec.Body.String())
		}
		var count int
		_ = db.Pool.QueryRow(ctx,
			`SELECT COUNT(*) FROM reactions WHERE target_type = 'comment' AND target_id = $1`,
			foreignComment.ID).Scan(&count)
		if count != 0 {
			t.Errorf("reaction was recorded on foreign comment: count=%d", count)
		}
	})
}

// TestClientMemberCanWriteInOwnOrg ensures the #25 authz fix doesn't over-block:
// a client who is a member of the ticket's owning org must still be able to
// create tickets, post comments, and react within that org.
func TestClientMemberCanWriteInOwnOrg(t *testing.T) {
	r, db, sessions, _ := setupTestRouter(t)
	ctx := context.Background()

	org, err := db.CreateOrg(ctx, "Home Org", "home-org")
	if err != nil {
		t.Fatalf("create org: %v", err)
	}
	proj, err := db.CreateProject(ctx, org.ID, "Home Proj", "home-proj")
	if err != nil {
		t.Fatalf("create proj: %v", err)
	}
	cookie := createAuthenticatedUser(t, db, sessions, "member@test.com", "client")
	member, _ := db.GetUserByEmail(ctx, "member@test.com")
	if err := db.AddOrgMember(ctx, member.ID, org.ID, "member"); err != nil {
		t.Fatalf("add member: %v", err)
	}

	t.Run("CreateTicket in own org", func(t *testing.T) {
		form := url.Values{
			"project_id": {proj.ID},
			"title":      {"Legit"},
			"type":       {"task"},
		}
		req := httptest.NewRequest(http.MethodPost, "/tickets", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.AddCookie(cookie)
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)

		// Handler returns 201 Created when no Referer is set.
		if rec.Code != http.StatusCreated && rec.Code != http.StatusSeeOther {
			t.Fatalf("expected 201 or 303, got %d: %s", rec.Code, rec.Body.String())
		}
	})

	// Seed a ticket so we can exercise comment + reaction paths.
	ownTicket := &models.Ticket{
		ProjectID: proj.ID, Type: "task", Title: "Own Task",
		Status: "backlog", Priority: "medium", CreatedBy: member.ID,
	}
	if err := db.CreateTicket(ctx, ownTicket); err != nil {
		t.Fatalf("seed own ticket: %v", err)
	}

	t.Run("CreateComment in own org", func(t *testing.T) {
		form := url.Values{"body": {"hello"}}
		req := httptest.NewRequest(http.MethodPost, "/tickets/"+ownTicket.ID+"/comments", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.AddCookie(cookie)
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("ToggleReaction on own ticket", func(t *testing.T) {
		form := url.Values{"emoji": {"\U0001F44D"}}
		req := httptest.NewRequest(http.MethodPost, "/reactions/ticket/"+ownTicket.ID, strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.AddCookie(cookie)
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
		}
	})
}

// TestCreateTicketCrossProjectParent verifies that supplying a parent_id
// from another project (even one the user also has access to) does not
// leak existence of the other project's tickets. Per Codex review on #26,
// the handler must return the same 404 "Parent ticket not found" for
// both "nonexistent parent" and "parent in different project".
func TestCreateTicketCrossProjectParent(t *testing.T) {
	r, db, sessions, _ := setupTestRouter(t)
	ctx := context.Background()

	// One org with two projects, one member with access to both.
	org, err := db.CreateOrg(ctx, "DualProj", "dualproj")
	if err != nil {
		t.Fatalf("create org: %v", err)
	}
	projA, err := db.CreateProject(ctx, org.ID, "A", "dualproj-a")
	if err != nil {
		t.Fatalf("create projA: %v", err)
	}
	projB, err := db.CreateProject(ctx, org.ID, "B", "dualproj-b")
	if err != nil {
		t.Fatalf("create projB: %v", err)
	}
	cookie := createAuthenticatedUser(t, db, sessions, "xp@test.com", "client")
	user, _ := db.GetUserByEmail(ctx, "xp@test.com")
	if err = db.AddOrgMember(ctx, user.ID, org.ID, "member"); err != nil {
		t.Fatalf("add member: %v", err)
	}

	parentInA := &models.Ticket{
		ProjectID: projA.ID, Type: "feature", Title: "Parent", Status: "backlog", Priority: "medium", CreatedBy: user.ID,
	}
	if err = db.CreateTicket(ctx, parentInA); err != nil {
		t.Fatalf("create parent: %v", err)
	}

	// Attempt to create a child in project B pointing at the parent in project A.
	form := url.Values{
		"project_id": {projB.ID},
		"parent_id":  {parentInA.ID},
		"title":      {"Child"},
		"type":       {"task"},
	}
	req := httptest.NewRequest(http.MethodPost, "/tickets", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("expected 404 (same as missing parent, no enumeration leak), got %d: %s", rec.Code, rec.Body.String())
	}
	// The body must not reveal that the parent exists in another project.
	if body := rec.Body.String(); strings.Contains(body, "same project") || strings.Contains(body, "different") {
		t.Errorf("body leaked cross-project existence: %q", body)
	}
}

// TestUpdateStatusAcceptsCancelledSpelling locks in #35: the handler,
// AI schema, template dropdown, and DB CHECK constraint must all
// agree on the "cancelled" (two l's) spelling, matching the
// migration 000006 constraint. The old code had "canceled" in the
// handler validation, which rejected the template's "cancelled"
// POST as "Invalid status".
func TestUpdateStatusAcceptsCancelledSpelling(t *testing.T) {
	r, db, sessions, _ := setupTestRouter(t)
	ctx := context.Background()

	org, err := db.CreateOrg(ctx, "Cancel Org", "cancel-org")
	if err != nil {
		t.Fatalf("create org: %v", err)
	}
	proj, err := db.CreateProject(ctx, org.ID, "Cancel Proj", "cancel-proj")
	if err != nil {
		t.Fatalf("create proj: %v", err)
	}
	cookie := createAuthenticatedUser(t, db, sessions, "canceler@test.com", "client")
	user, err := db.GetUserByEmail(ctx, "canceler@test.com")
	if err != nil {
		t.Fatalf("get user: %v", err)
	}
	if err = db.AddOrgMember(ctx, user.ID, org.ID, "member"); err != nil {
		t.Fatalf("add member: %v", err)
	}

	ticket := &models.Ticket{
		ProjectID: proj.ID, Type: "task", Title: "Will Cancel",
		Status: "backlog", Priority: "medium", CreatedBy: user.ID,
	}
	if err = db.CreateTicket(ctx, ticket); err != nil {
		t.Fatalf("create ticket: %v", err)
	}

	form := url.Values{"status": {"cancelled"}}
	req := httptest.NewRequest(http.MethodPatch, "/tickets/"+ticket.ID+"/status", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	got, err := db.GetTicket(ctx, ticket.ID)
	if err != nil {
		t.Fatalf("GetTicket: %v", err)
	}
	if got.Status != "cancelled" {
		t.Errorf("status = %q, want cancelled", got.Status)
	}
}
