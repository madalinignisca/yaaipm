package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// TestAcceptInvitationDoesNotOverwriteExistingRole locks in the fix for #31.
// If a user is already a member of an organization (added via another path —
// e.g. directly by an admin, or a prior invitation), accepting a *new*
// invitation with a different role must NOT silently overwrite their
// existing role via AddOrgMember's upsert behavior.
//
// Expected: the handler rejects the request (409 Conflict), the invitation
// is left pending so the admin can see it and decide, and the user's
// original membership role is preserved exactly.
func TestAcceptInvitationDoesNotOverwriteExistingRole(t *testing.T) {
	r, db, sessions, _ := setupTestRouter(t)
	ctx := context.Background()

	// User exists and is already an "owner" of the target org.
	cookie := createAuthenticatedUser(t, db, sessions, "dual@test.com", "client")
	user, _ := db.GetUserByEmail(ctx, "dual@test.com")

	org, err := db.CreateOrg(ctx, "Dual Org", "dual-org")
	if err != nil {
		t.Fatalf("create org: %v", err)
	}
	if err = db.AddOrgMember(ctx, user.ID, org.ID, "owner"); err != nil {
		t.Fatalf("seed existing owner membership: %v", err)
	}

	// Now an admin issues an invitation for the same email at a LOWER role.
	// This is the exploit vector: current code will accept and downgrade.
	inviter, _ := db.CreateUser(ctx,
		"inviter@test.com",
		"$argon2id$v=19$m=65536,t=3,p=2$aaaaaaaaaaaaaaaa$bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", // unused
		"Inviter", "superadmin")
	inv, err := db.CreateInvitation(ctx,
		"dual@test.com", org.ID, "member",
		"sometokenhash31", inviter.ID,
		time.Now().Add(24*time.Hour))
	if err != nil {
		t.Fatalf("seed invitation: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/invitations/"+inv.ID+"/accept", http.NoBody)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusConflict {
		t.Errorf("expected 409 Conflict, got %d: %s", rec.Code, rec.Body.String())
	}

	// The user's original role must still be "owner" — not downgraded to "member".
	m, err := db.GetOrgMembership(ctx, user.ID, org.ID)
	if err != nil {
		t.Fatalf("reload membership: %v", err)
	}
	if m.Role != "owner" {
		t.Errorf("role was overwritten: want owner, got %q", m.Role)
	}

	// The invitation must still be pending so the admin can see/revoke it.
	reloaded, err := db.GetInvitationByID(ctx, inv.ID)
	if err != nil {
		t.Fatalf("reload invitation: %v", err)
	}
	if reloaded.Status != "pending" {
		t.Errorf("invitation status mutated: want pending, got %q", reloaded.Status)
	}
}

// TestAcceptInvitationFreshMemberStillWorks is the non-regression guard:
// a user accepting an invitation for an org they do NOT already belong to
// should still succeed and receive the invited role.
func TestAcceptInvitationFreshMemberStillWorks(t *testing.T) {
	r, db, sessions, _ := setupTestRouter(t)
	ctx := context.Background()

	cookie := createAuthenticatedUser(t, db, sessions, "fresh@test.com", "client")
	user, _ := db.GetUserByEmail(ctx, "fresh@test.com")

	org, err := db.CreateOrg(ctx, "Fresh Org", "fresh-org")
	if err != nil {
		t.Fatalf("create org: %v", err)
	}

	inviter, _ := db.CreateUser(ctx,
		"inviter2@test.com",
		"$argon2id$v=19$m=65536,t=3,p=2$aaaaaaaaaaaaaaaa$bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		"Inviter2", "superadmin")
	inv, err := db.CreateInvitation(ctx,
		"fresh@test.com", org.ID, "admin",
		"sometokenhash31b", inviter.ID,
		time.Now().Add(24*time.Hour))
	if err != nil {
		t.Fatalf("seed invitation: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/invitations/"+inv.ID+"/accept", http.NoBody)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d: %s", rec.Code, rec.Body.String())
	}

	m, err := db.GetOrgMembership(ctx, user.ID, org.ID)
	if err != nil {
		t.Fatalf("reload membership: %v", err)
	}
	if m.Role != "admin" {
		t.Errorf("role = %q, want admin", m.Role)
	}

	reloaded, _ := db.GetInvitationByID(ctx, inv.ID)
	if reloaded.Status != "accepted" {
		t.Errorf("invitation status = %q, want accepted", reloaded.Status)
	}
}
