package handlers

import (
	"context"
	"errors"

	"github.com/madalin/forgedesk/internal/auth"
	"github.com/madalin/forgedesk/internal/models"
)

// errCrossTenant signals that a non-staff user attempted to act on a resource
// outside of their org memberships. Handlers should translate this to 404
// (not 403) so an attacker probing for valid IDs cannot distinguish
// "does not exist" from "belongs to another tenant".
var errCrossTenant = errors.New("cross-tenant access denied")

// authorizeOrgAccess returns nil when the user is allowed to act on resources
// under orgID. Staff and superadmin have global access; client users must
// hold a membership row for the given org.
func authorizeOrgAccess(ctx context.Context, db *models.DB, user *models.User, orgID string) error {
	if auth.IsStaffOrAbove(user.Role) {
		return nil
	}
	if _, err := db.GetOrgMembership(ctx, user.ID, orgID); err != nil {
		return errCrossTenant
	}
	return nil
}

// authorizeProjectAccess loads the project and verifies the user is authorized
// to act on its owning org. Used by write handlers that take a project_id from
// the request body (e.g. CreateTicket).
func authorizeProjectAccess(ctx context.Context, db *models.DB, user *models.User, projectID string) (*models.Project, error) {
	proj, err := db.GetProjectByID(ctx, projectID)
	if err != nil {
		return nil, err
	}
	if err := authorizeOrgAccess(ctx, db, user, proj.OrgID); err != nil {
		return nil, err
	}
	return proj, nil
}

// authorizeTicketAccess loads the ticket and verifies the user is authorized
// to act on its owning org (via the ticket's project).
func authorizeTicketAccess(ctx context.Context, db *models.DB, user *models.User, ticketID string) (*models.Ticket, error) {
	ticket, err := db.GetTicket(ctx, ticketID)
	if err != nil {
		return nil, err
	}
	proj, err := db.GetProjectByID(ctx, ticket.ProjectID)
	if err != nil {
		return nil, err
	}
	if err := authorizeOrgAccess(ctx, db, user, proj.OrgID); err != nil {
		return nil, err
	}
	return ticket, nil
}

// authorizeCommentAccess verifies the user is authorized to act on a comment
// via its ticket → project → org chain. Used by reaction handlers that take
// a comment ID as target.
func authorizeCommentAccess(ctx context.Context, db *models.DB, user *models.User, commentID string) error {
	orgID, err := db.ResolveCommentOrgID(ctx, commentID)
	if err != nil {
		return err
	}
	return authorizeOrgAccess(ctx, db, user, orgID)
}
