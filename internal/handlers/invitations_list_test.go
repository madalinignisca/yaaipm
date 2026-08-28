package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/madalin/forgedesk/internal/auth"
	"github.com/madalin/forgedesk/internal/models"
	"github.com/madalin/forgedesk/internal/testutil"
)

// The invite form fires htmx.trigger('#invitation-list', 'refresh'), but the
// container had no hx-get/hx-trigger, so the event went nowhere and the
// pending list stayed stale until a manual reload (#92). This asserts the
// wiring exists in the template — the trigger and the listener have to
// agree, and nothing else in the test suite would notice if they stopped.
func TestOrgSettingsTemplate_InvitationListIsWiredForRefresh(t *testing.T) {
	body, err := os.ReadFile(testutil.ProjectRoot() + "/templates/pages/org_settings.html")
	if err != nil {
		t.Fatalf("reading template: %v", err)
	}
	tpl := string(body)

	if !strings.Contains(tpl, `htmx.trigger('#invitation-list', 'refresh')`) {
		t.Fatal("invite form no longer triggers a refresh — if that was removed on purpose, remove this test too")
	}

	// The container must listen for exactly the event the form sends.
	idx := strings.Index(tpl, `id="invitation-list"`)
	if idx < 0 {
		t.Fatal(`#invitation-list container not found`)
	}
	// Look at the enclosing tag only, not the whole file.
	start := strings.LastIndex(tpl[:idx], "<")
	end := strings.Index(tpl[idx:], ">") + idx
	tag := tpl[start:end]

	for _, want := range []string{
		`hx-get="/orgs/`,
		`hx-trigger="refresh"`,
		`hx-swap="innerHTML"`,
	} {
		if !strings.Contains(tag, want) {
			t.Errorf("#invitation-list tag missing %s — the refresh trigger has nothing to act on.\ntag: %s", want, tag)
		}
	}
}

func seedOrgWithInvitation(t *testing.T, db *models.DB, slug string) *models.Organization {
	t.Helper()
	ctx := context.Background()
	org, err := db.CreateOrg(ctx, "Inv Org "+slug, slug)
	if err != nil {
		t.Fatalf("CreateOrg: %v", err)
	}
	hash, _ := auth.HashPassword("TestPassword123!")
	inviter, err := db.CreateUser(ctx, "inviter-"+slug+"@test.com", hash, "Inviter", "client")
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	if _, err := db.CreateInvitation(ctx, "invitee-"+slug+"@test.com", org.ID, "member",
		"tokenhash-"+slug, inviter.ID, time.Now().Add(72*time.Hour)); err != nil {
		t.Fatalf("CreateInvitation: %v", err)
	}
	return org
}

// The list is manager-only: pending invitations expose invitee email
// addresses, and the template already renders that section behind
// CanManage. The GET endpoint must enforce the same thing rather than
// relying on the template to hide it.
func TestInvitationList_RoleMatrix(t *testing.T) {
	r, db, sessions, _ := setupTestRouter(t)
	ctx := context.Background()

	org := seedOrgWithInvitation(t, db, "inv-matrix-org")
	createAuthenticatedUser(t, db, sessions, "first-sa-inv@test.com", "superadmin")

	mkUser := func(email, platformRole, orgRole string) *http.Cookie {
		t.Helper()
		// Shared helper across ~40 tests; it manages its own background
		// context and threading this one through would change its signature
		// everywhere.
		cookie := createAuthenticatedUser(t, db, sessions, email, platformRole) //nolint:contextcheck // shared helper manages its own ctx
		if orgRole != "" {
			var uid string
			if err := db.Pool.QueryRow(ctx, `SELECT id FROM users WHERE email = $1`, email).Scan(&uid); err != nil {
				t.Fatalf("look up %s: %v", email, err)
			}
			if _, err := db.Pool.Exec(ctx,
				`INSERT INTO org_memberships (user_id, org_id, role) VALUES ($1,$2,$3)`, uid, org.ID, orgRole); err != nil {
				t.Fatalf("membership: %v", err)
			}
		}
		return cookie
	}

	cases := []struct {
		name     string
		cookie   *http.Cookie
		wantCode int
		wantsPII bool
	}{
		{"owner sees the list", mkUser("owner-inv@test.com", "client", "owner"), http.StatusOK, true},
		{"staff sees any org's list", mkUser("staff-inv@test.com", "staff", ""), http.StatusOK, true},
		{"plain member cannot", mkUser("member-inv@test.com", "client", "member"), http.StatusForbidden, false},
		{"non-member cannot", mkUser("outsider-inv@test.com", "client", ""), http.StatusForbidden, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/orgs/inv-matrix-org/invitations/list", http.NoBody)
			req.AddCookie(tc.cookie)
			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, req)

			if rec.Code != tc.wantCode {
				t.Errorf("got %d, want %d", rec.Code, tc.wantCode)
			}
			leaked := strings.Contains(rec.Body.String(), "invitee-inv-matrix-org@test.com")
			if leaked != tc.wantsPII {
				t.Errorf("invitee email present = %v, want %v", leaked, tc.wantsPII)
			}
		})
	}
}
