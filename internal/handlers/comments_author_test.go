package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/madalin/forgedesk/internal/auth"
	"github.com/madalin/forgedesk/internal/models"
	"github.com/madalin/forgedesk/internal/testutil"
)

// authorSpanRE matches the byline both render paths must produce for a
// comment's author. Matching the `comment-author` landmark rather than the
// bare name keeps these tests from passing on an incidental occurrence of
// "User" or a person's name elsewhere on the ticket page — and matching the
// landmark rather than the full class attribute keeps them from breaking when
// a Tailwind class is added alongside it, which is exactly what happened when
// the E2E landmarks landed (#136).
func authorSpanRE(name string) *regexp.Regexp {
	return regexp.MustCompile(`comment-author[^>]*>\s*` + regexp.QuoteMeta(name) + `\s*<`)
}

func containsAuthor(body, name string) bool {
	return authorSpanRE(name).MatchString(body)
}

// TestTicketDetailShowsCommentAuthorNames locks in #95: the ticket detail
// page rendered every existing comment with a hardcoded generic "User",
// because its inline {{range .Comments}} loop only had a models.Comment
// (which carries user_id, not the user's name). The real name appeared
// only on the comment you had just posted, via the HTMX partial — so the
// same comment was labeled differently before and after a reload.
func TestTicketDetailShowsCommentAuthorNames(t *testing.T) {
	r, db, sessions, _ := setupTestRouter(t)
	cookie := createAuthenticatedUser(t, db, sessions, "viewer95@test.com", "superadmin")
	ctx := context.Background()

	org, _ := db.CreateOrg(ctx, "Author Org", "author-org-95")
	proj, _ := db.CreateProject(ctx, org.ID, "Author Proj", "author-proj-95")
	viewer, _ := db.GetUserByEmail(ctx, "viewer95@test.com")

	// A second, differently-named human so the assertion cannot pass on
	// the viewer's own name leaking in from the navbar.
	hash, _ := auth.HashPassword(context.Background(), "TestPassword123!")
	author, err := db.CreateUser(ctx, "alice95@test.com", hash, "Alice Author", "client")
	if err != nil {
		t.Fatalf("creating comment author: %v", err)
	}

	ticket := &models.Ticket{
		ProjectID: proj.ID, Type: "task", Title: "Author Task",
		Status: "backlog", Priority: "medium", CreatedBy: viewer.ID,
	}
	if err := db.CreateTicket(ctx, ticket); err != nil {
		t.Fatalf("creating ticket: %v", err)
	}

	if _, err := db.CreateComment(ctx, ticket.ID, &author.ID, nil, "human comment"); err != nil {
		t.Fatalf("creating human comment: %v", err)
	}
	agent := "claude"
	if _, err := db.CreateComment(ctx, ticket.ID, nil, &agent, "agent comment"); err != nil {
		t.Fatalf("creating agent comment: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/tickets/"+ticket.ID, http.NoBody)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()

	if !containsAuthor(body, "Alice Author") {
		t.Errorf("ticket page did not show the human comment author's real name")
	}
	// Agent comments stay behind a single generic label on purpose: clients
	// must not see which agent (claude/gemini/codex/mistral) produced a
	// comment. This is the one case where a generic name is correct.
	if !containsAuthor(body, "ForgeDesk Bot") {
		t.Errorf("ticket page did not label the agent comment as ForgeDesk Bot")
	}
	if containsAuthor(body, "User") {
		t.Errorf("ticket page still renders the hardcoded generic \"User\" author")
	}
	if containsAuthor(body, "claude") {
		t.Errorf("ticket page leaked the underlying agent name to the client")
	}
}

// TestCreateCommentPartialShowsAuthorName is the other half of #95: the
// HTMX partial must label the new comment with the same span the full
// page produces, so posting then reloading does not rename the author.
func TestCreateCommentPartialShowsAuthorName(t *testing.T) {
	r, db, sessions, _ := setupTestRouter(t)
	cookie := createAuthenticatedUser(t, db, sessions, "poster95@test.com", "superadmin")
	ctx := context.Background()

	org, _ := db.CreateOrg(ctx, "Poster Org", "poster-org-95")
	proj, _ := db.CreateProject(ctx, org.ID, "Poster Proj", "poster-proj-95")
	user, _ := db.GetUserByEmail(ctx, "poster95@test.com")
	ticket := &models.Ticket{
		ProjectID: proj.ID, Type: "task", Title: "Poster Task",
		Status: "backlog", Priority: "medium", CreatedBy: user.ID,
	}
	if err := db.CreateTicket(ctx, ticket); err != nil {
		t.Fatalf("creating ticket: %v", err)
	}

	form := url.Values{"body": {"hello"}}
	req := httptest.NewRequest(http.MethodPost, "/tickets/"+ticket.ID+"/comments", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if !containsAuthor(rec.Body.String(), user.Name) {
		t.Errorf("HTMX comment partial did not show the author name %q: %s", user.Name, rec.Body.String())
	}
}

// TestTicketDetailDelegatesCommentMarkup guards the structural half of
// #95. The bug existed because comment markup lived in two places that
// had to be edited in lockstep; #37 had already patched one copy and left
// a comment in each saying "keep this matching the other". Behavioral
// assertions above catch today's divergence, but only this one catches
// someone re-inlining the markup and starting the drift over.
func TestTicketDetailDelegatesCommentMarkup(t *testing.T) {
	page := filepath.Join(testutil.ProjectRoot(), "templates", "pages", "ticket_detail.html")
	src, err := os.ReadFile(page)
	if err != nil {
		t.Fatalf("reading ticket_detail.html: %v", err)
	}
	if !strings.Contains(string(src), `{{template "comment.html"`) {
		t.Errorf("ticket_detail.html must render comments via the comment.html partial")
	}
	// Match the class in an actual class attribute, not any mention of the
	// name: the file legitimately refers to comment-body in a comment
	// explaining the shared-landmark convention.
	if regexp.MustCompile(`class="[^"]*comment-body`).MatchString(string(src)) {
		t.Errorf("ticket_detail.html still carries its own copy of the comment markup; it belongs only in components/comment.html")
	}
}
