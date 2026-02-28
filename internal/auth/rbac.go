package auth

// User roles
const (
	RoleSuperAdmin = "superadmin"
	RoleStaff      = "staff"
	RoleClient     = "client"
)

// Org membership roles
const (
	OrgRoleOwner  = "owner"
	OrgRoleAdmin  = "admin"
	OrgRoleMember = "member"
)

// IsStaffOrAbove returns true for superadmin and staff.
func IsStaffOrAbove(role string) bool {
	return role == RoleSuperAdmin || role == RoleStaff
}

// CanManageOrg returns true if the user can manage org settings and members.
func CanManageOrg(orgRole string) bool {
	return orgRole == OrgRoleOwner || orgRole == OrgRoleAdmin
}

// CanReset2FA checks if the actor can reset the target's 2FA.
func CanReset2FA(actorRole, actorOrgRole, targetRole string, isSelf bool) bool {
	if isSelf {
		return true // self-reset always allowed (with verification)
	}
	switch actorRole {
	case RoleSuperAdmin:
		return true
	case RoleStaff:
		return targetRole == RoleClient
	case RoleClient:
		return CanManageOrg(actorOrgRole) && targetRole == RoleClient
	}
	return false
}

// CanSetAgentMode checks if a user can configure agent mode on tickets.
func CanSetAgentMode(role string) bool {
	return IsStaffOrAbove(role)
}

// CanTriggerDeploy checks if a user can trigger merges and deploys.
func CanTriggerDeploy(role string) bool {
	return IsStaffOrAbove(role)
}
