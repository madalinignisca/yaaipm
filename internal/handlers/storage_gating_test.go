package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/madalin/forgedesk/internal/models"
)

// TestUploadControlsHiddenWhenStorageDisabled pins #153.
//
// With no object store configured, cmd/server/main.go does not register the
// upload routes at all — so a page that still offers Upload, Insert Image or
// the attachments card is offering controls that 404. Production ran in exactly
// that state, and nothing said so.
//
// The template must therefore gate on StorageEnabled, and this asserts both
// directions: a bare {{if}} that is never true would pass a one-sided test.
func TestUploadControlsHiddenWhenStorageDisabled(t *testing.T) {
	r, db, sessions, engine := setupTestRouter(t)
	ctx := context.Background()
	cookie := createAuthenticatedUser(t, db, sessions, "storagegate@test.com", "superadmin")

	user, err := db.GetUserByEmail(ctx, "storagegate@test.com")
	if err != nil {
		t.Fatalf("GetUserByEmail: %v", err)
	}
	org, err := db.CreateOrg(ctx, "Storage Org", "storage-org")
	if err != nil {
		t.Fatalf("CreateOrg: %v", err)
	}
	proj, err := db.CreateProject(ctx, org.ID, "Storage Proj", "storage-proj")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	tk := &models.Ticket{
		ProjectID: proj.ID, Type: "task", Title: "Storage gate task",
		Status: "backlog", Priority: "medium", CreatedBy: user.ID,
	}
	if err := db.CreateTicket(ctx, tk); err != nil {
		t.Fatalf("CreateTicket: %v", err)
	}
	ticket := tk.ID

	get := func() string {
		req := httptest.NewRequest(http.MethodGet, "/tickets/"+ticket, http.NoBody)
		req.AddCookie(cookie)
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("ticket page = %d: %s", rec.Code, rec.Body.String())
		}
		return rec.Body.String()
	}

	engine.StorageEnabled = false
	off := get()
	for _, marker := range []string{"Upload File", "insert-image", "Attachments"} {
		if strings.Contains(off, marker) {
			t.Errorf("with storage disabled the page still offers %q, whose route is not registered", marker)
		}
	}

	engine.StorageEnabled = true
	on := get()
	for _, marker := range []string{"Upload File", "insert-image", "Attachments"} {
		if !strings.Contains(on, marker) {
			t.Errorf("with storage enabled the page is missing %q — the gate hides it unconditionally", marker)
		}
	}
}

// TestTicketPageRendersToCompletion guards the failure that hid the bug above.
//
// A template execution error — a missing field on the wrong dot, say — makes
// html/template stop writing mid-page. Handlers discard the error
// (`_ = h.engine.Render(...)`), so the response is still 200 with plausible
// content and the tail is simply gone. That is indistinguishable from a
// complete page unless something asserts on what should be at the END.
//
// This repo has hit it before (PR #80, a conditional `selected` attribute), and
// again in #153, where everything after line 353 vanished while the page still
// returned 32KB and a 200.
func TestTicketPageRendersToCompletion(t *testing.T) {
	r, db, sessions, engine := setupTestRouter(t)
	ctx := context.Background()
	cookie := createAuthenticatedUser(t, db, sessions, "completion@test.com", "superadmin")

	user, err := db.GetUserByEmail(ctx, "completion@test.com")
	if err != nil {
		t.Fatalf("GetUserByEmail: %v", err)
	}
	org, err := db.CreateOrg(ctx, "Completion Org", "completion-org")
	if err != nil {
		t.Fatalf("CreateOrg: %v", err)
	}
	proj, err := db.CreateProject(ctx, org.ID, "Completion", "completion-proj")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	tk := &models.Ticket{
		ProjectID: proj.ID, Type: "task", Title: "Completion task",
		Status: "backlog", Priority: "medium", CreatedBy: user.ID,
	}
	if err := db.CreateTicket(ctx, tk); err != nil {
		t.Fatalf("CreateTicket: %v", err)
	}
	engine.StorageEnabled = true

	req := httptest.NewRequest(http.MethodGet, "/tickets/"+tk.ID, http.NoBody)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	body := rec.Body.String()
	// Markers from progressively later in the template. The last ones are what
	// a mid-page abort silently removes.
	for _, marker := range []string{
		"Attachments",      // early
		"imageInsertModal", // the modal include, near the end
		"ticketEditor",     // the editor script
		"toolbar",          // inside that script
		"</html>",          // the very end of the layout
	} {
		if !strings.Contains(body, marker) {
			t.Errorf("page is missing %q — rendering aborted before the end (status was %d, %d bytes, so it looks fine)",
				marker, rec.Code, len(body))
		}
	}
}
