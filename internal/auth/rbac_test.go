package auth

import "testing"

func TestIsStaffOrAbove(t *testing.T) {
	tests := []struct {
		role string
		want bool
	}{
		{RoleSuperAdmin, true},
		{RoleStaff, true},
		{RoleClient, false},
		{"unknown", false},
		{"", false},
	}

	for _, tc := range tests {
		t.Run(tc.role, func(t *testing.T) {
			if got := IsStaffOrAbove(tc.role); got != tc.want {
				t.Errorf("IsStaffOrAbove(%q) = %v, want %v", tc.role, got, tc.want)
			}
		})
	}
}

func TestCanManageOrg(t *testing.T) {
	tests := []struct {
		role string
		want bool
	}{
		{OrgRoleOwner, true},
		{OrgRoleAdmin, true},
		{OrgRoleMember, false},
		{"", false},
	}

	for _, tc := range tests {
		t.Run(tc.role, func(t *testing.T) {
			if got := CanManageOrg(tc.role); got != tc.want {
				t.Errorf("CanManageOrg(%q) = %v, want %v", tc.role, got, tc.want)
			}
		})
	}
}

func TestCanReset2FA(t *testing.T) {
	tests := []struct {
		name       string
		actorRole  string
		actorOrg   string
		targetRole string
		isSelf     bool
		want       bool
	}{
		{"self reset always allowed", RoleClient, OrgRoleMember, RoleClient, true, true},
		{"superadmin can reset anyone", RoleSuperAdmin, "", RoleSuperAdmin, false, true},
		{"superadmin resets staff", RoleSuperAdmin, "", RoleStaff, false, true},
		{"superadmin resets client", RoleSuperAdmin, "", RoleClient, false, true},
		{"staff resets client", RoleStaff, "", RoleClient, false, true},
		{"staff cannot reset staff", RoleStaff, "", RoleStaff, false, false},
		{"staff cannot reset superadmin", RoleStaff, "", RoleSuperAdmin, false, false},
		{"client org owner resets client", RoleClient, OrgRoleOwner, RoleClient, false, true},
		{"client org admin resets client", RoleClient, OrgRoleAdmin, RoleClient, false, true},
		{"client member cannot reset", RoleClient, OrgRoleMember, RoleClient, false, false},
		{"client cannot reset staff", RoleClient, OrgRoleOwner, RoleStaff, false, false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := CanReset2FA(tc.actorRole, tc.actorOrg, tc.targetRole, tc.isSelf)
			if got != tc.want {
				t.Errorf("CanReset2FA(%q, %q, %q, %v) = %v, want %v",
					tc.actorRole, tc.actorOrg, tc.targetRole, tc.isSelf, got, tc.want)
			}
		})
	}
}

func TestCanSetAgentMode(t *testing.T) {
	if !CanSetAgentMode(RoleSuperAdmin) {
		t.Error("superadmin should be able to set agent mode")
	}
	if !CanSetAgentMode(RoleStaff) {
		t.Error("staff should be able to set agent mode")
	}
	if CanSetAgentMode(RoleClient) {
		t.Error("client should not be able to set agent mode")
	}
}

func TestCanTriggerDeploy(t *testing.T) {
	if !CanTriggerDeploy(RoleSuperAdmin) {
		t.Error("superadmin should be able to trigger deploy")
	}
	if !CanTriggerDeploy(RoleStaff) {
		t.Error("staff should be able to trigger deploy")
	}
	if CanTriggerDeploy(RoleClient) {
		t.Error("client should not be able to trigger deploy")
	}
}
