package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
)

// postAcceptWithEdit submits the accept button with an edited_text field,
// exactly as the Edit tab's textarea does via hx-include.
func postAcceptWithEdit(t *testing.T, r *chi.Mux, cookie *http.Cookie, ticketID, roundID, edited string) *httptest.ResponseRecorder {
	t.Helper()
	form := url.Values{"edited_text": {edited}}
	req := httptest.NewRequest(http.MethodPost,
		"/tickets/"+ticketID+"/debate/rounds/"+roundID+"/accept",
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Hx-Request", "true")
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

// TestAcceptTreatsCRLFAsUnedited pins a trap in the HTML form layer.
//
// The Edit textarea is always in the DOM — the tab only hides it — so its
// content is submitted on EVERY accept, even when the user never opened it.
// Browsers normalise textarea newlines to CRLF while the stored text uses LF,
// so without folding them back, untouched multi-line text would differ from
// output_text and every accept would be recorded as hand-edited. That is wrong
// data that looks perfectly fine until someone queries it.
func TestAcceptTreatsCRLFAsUnedited(t *testing.T) {
	r, db, sessions := setupDebateTestEnv(t)
	ctx := context.Background()
	ticket, cookie := seedAuthedFeatureTicket(t, db, sessions)

	const aiText = "line one\nline two\nline three"
	deb, round := insertInReviewRound(t, db, cookie, r, ticket.ID, aiText)

	// Exactly what a browser sends back for content the user never touched.
	rec := postAcceptWithEdit(t, r, cookie, ticket.ID, round.ID, "line one\r\nline two\r\nline three")
	if rec.Code != http.StatusOK {
		t.Fatalf("accept = %d, want 200: %s", rec.Code, rec.Body.String())
	}

	var edited *string
	if err := db.Pool.QueryRow(ctx,
		`SELECT edited_text FROM feature_debate_rounds WHERE id = $1`, round.ID).Scan(&edited); err != nil {
		t.Fatalf("reading edited_text: %v", err)
	}
	if edited != nil {
		t.Errorf("edited_text = %q; CRLF-normalised untouched text must not count as an edit", *edited)
	}

	var current string
	if err := db.Pool.QueryRow(ctx,
		`SELECT current_text FROM feature_debates WHERE id = $1`, deb.ID).Scan(&current); err != nil {
		t.Fatalf("reading current_text: %v", err)
	}
	if strings.Contains(current, "\r") {
		t.Errorf("current_text kept CRLF line endings: %q", current)
	}
	if current != aiText {
		t.Errorf("current_text = %q, want the untouched AI text", current)
	}
}

// TestAcceptWithRealEditShipsItEndToEnd drives the whole path the Edit tab
// uses: the textarea posts through hx-include, the edit is stored, and the
// document becomes the edited text while output_text keeps the AI's original.
func TestAcceptWithRealEditShipsItEndToEnd(t *testing.T) {
	r, db, sessions := setupDebateTestEnv(t)
	ctx := context.Background()
	ticket, cookie := seedAuthedFeatureTicket(t, db, sessions)

	const aiText = "The AI wrote this with a typoo."
	const userText = "The AI wrote this with a typo, now fixed."
	deb, round := insertInReviewRound(t, db, cookie, r, ticket.ID, aiText)

	rec := postAcceptWithEdit(t, r, cookie, ticket.ID, round.ID, userText)
	if rec.Code != http.StatusOK {
		t.Fatalf("accept = %d, want 200: %s", rec.Code, rec.Body.String())
	}

	var output string
	var edited *string
	if err := db.Pool.QueryRow(ctx,
		`SELECT output_text, edited_text FROM feature_debate_rounds WHERE id = $1`,
		round.ID).Scan(&output, &edited); err != nil {
		t.Fatalf("reading round: %v", err)
	}
	if output != aiText {
		t.Errorf("output_text = %q; the AI's original must survive editing", output)
	}
	if edited == nil || *edited != userText {
		t.Errorf("edited_text = %v, want %q", edited, userText)
	}

	var current string
	if err := db.Pool.QueryRow(ctx,
		`SELECT current_text FROM feature_debates WHERE id = $1`, deb.ID).Scan(&current); err != nil {
		t.Fatalf("reading current_text: %v", err)
	}
	if current != userText {
		t.Errorf("current_text = %q, want the edited text", current)
	}
}

// TestAcceptRefusesEmptyEditOverHTTP: a blank edit must be refused rather than
// silently emptying the brief.
func TestAcceptRefusesEmptyEditOverHTTP(t *testing.T) {
	r, db, sessions := setupDebateTestEnv(t)
	ctx := context.Background()
	ticket, cookie := seedAuthedFeatureTicket(t, db, sessions)

	const aiText = "Text that must survive a refused empty edit."
	deb, round := insertInReviewRound(t, db, cookie, r, ticket.ID, aiText)

	var before string
	if err := db.Pool.QueryRow(ctx,
		`SELECT current_text FROM feature_debates WHERE id = $1`, deb.ID).Scan(&before); err != nil {
		t.Fatalf("reading current_text: %v", err)
	}

	postAcceptWithEdit(t, r, cookie, ticket.ID, round.ID, "   \n  ")

	var after, status string
	if err := db.Pool.QueryRow(ctx,
		`SELECT current_text FROM feature_debates WHERE id = $1`, deb.ID).Scan(&after); err != nil {
		t.Fatalf("reading current_text: %v", err)
	}
	if err := db.Pool.QueryRow(ctx,
		`SELECT status FROM feature_debate_rounds WHERE id = $1`, round.ID).Scan(&status); err != nil {
		t.Fatalf("reading status: %v", err)
	}

	if after != before {
		t.Errorf("current_text changed to %q after a refused empty edit", after)
	}
	if status == "accepted" {
		t.Error("round was accepted despite an empty edit")
	}
}

// TestShowVersionRendersTheEditedText guards the version-history viewer.
//
// Found by sweeping every read of output_text for "does this mean the AI's
// draft, or what shipped?" — this one means shipped, and neither code review
// caught it. Without it, opening a hand-edited version in the history renders
// the AI's draft: the history would quietly disagree with what the document
// actually was at that point.
func TestShowVersionRendersTheEditedText(t *testing.T) {
	r, db, sessions := setupDebateTestEnv(t)
	ticket, cookie := seedAuthedFeatureTicket(t, db, sessions)

	const aiText = "AI version of the brief"
	const userText = "Hand-corrected version of the brief"
	_, round := insertInReviewRound(t, db, cookie, r, ticket.ID, aiText)

	if rec := postAcceptWithEdit(t, r, cookie, ticket.ID, round.ID, userText); rec.Code != http.StatusOK {
		t.Fatalf("accept = %d: %s", rec.Code, rec.Body.String())
	}

	req := httptest.NewRequest(http.MethodGet,
		"/tickets/"+ticket.ID+"/debate/versions/"+round.ID, http.NoBody)
	req.Header.Set("Hx-Request", "true")
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("ShowVersion = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "Hand-corrected") {
		t.Errorf("version history does not show the edited text; body: %s", body)
	}
	if strings.Contains(body, "AI version of the brief") {
		t.Errorf("version history shows the AI draft instead of what shipped")
	}
}
