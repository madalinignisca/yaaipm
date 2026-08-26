package handlers

import (
	"context"
	"errors"
	"testing"

	"github.com/madalin/forgedesk/internal/auth"
	"github.com/madalin/forgedesk/internal/models"
	"github.com/madalin/forgedesk/internal/testutil"
)

// authorizeOrgManage must distinguish "this user may not manage" from
// "we could not find out". The predecessors (canManageOrgMembers and
// canManageOrg, byte-identical duplicates) returned a bare bool and
// collapsed both into false, so a Postgres blip surfaced to users as
// 403 Forbidden and never reached the logs as an infra fault (#128).
func TestAuthorizeOrgManage(t *testing.T) {
	pool := testutil.SetupTestDB(t)
	db := models.NewDB(pool)
	ctx := context.Background()

	org, err := db.CreateOrg(ctx, "Manage Org", "manage-org")
	if err != nil {
		t.Fatalf("CreateOrg: %v", err)
	}

	hash, _ := auth.HashPassword("TestPassword123!")
	mk := func(email, platformRole, orgRole string) *models.User {
		t.Helper()
		u, uErr := db.CreateUser(ctx, email, hash, "U", platformRole)
		if uErr != nil {
			t.Fatalf("CreateUser: %v", uErr)
		}
		if orgRole != "" {
			if _, exErr := db.Pool.Exec(ctx,
				`INSERT INTO org_memberships (user_id, org_id, role) VALUES ($1,$2,$3)`,
				u.ID, org.ID, orgRole); exErr != nil {
				t.Fatalf("add membership: %v", exErr)
			}
		}
		return u
	}

	owner := mk("owner-mg@test.com", "client", "owner")
	member := mk("member-mg@test.com", "client", "member")
	outsider := mk("outsider-mg@test.com", "client", "")
	staff := mk("staff-mg@test.com", "staff", "")

	cases := []struct {
		name    string
		user    *models.User
		wantOK  bool
		wantErr bool
	}{
		{"owner may manage", owner, true, false},
		{"plain member may not", member, false, false},
		{"non-member may not", outsider, false, false},
		{"staff may manage any org", staff, true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ok, mgErr := authorizeOrgManage(ctx, db, tc.user, org.ID)
			if ok != tc.wantOK {
				t.Errorf("ok = %v, want %v", ok, tc.wantOK)
			}
			if (mgErr != nil) != tc.wantErr {
				t.Errorf("err = %v, wantErr %v", mgErr, tc.wantErr)
			}
		})
	}

	// The point of the change: an infrastructure failure must come back as
	// an ERROR, not as a silent false that the caller renders as 403.
	t.Run("db failure returns an error, not a bare false", func(t *testing.T) {
		cancelled, cancel := context.WithCancel(ctx)
		cancel()

		ok, mgErr := authorizeOrgManage(cancelled, db, owner, org.ID)
		if ok {
			t.Error("ok = true on a failed lookup, want false")
		}
		if mgErr == nil {
			t.Fatal("err = nil on a failed lookup — the caller cannot tell this from a genuine denial")
		}
		if errors.Is(mgErr, errCrossTenant) {
			t.Error("a DB failure must not be reported as cross-tenant denial")
		}
	})
}
