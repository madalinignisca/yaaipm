package handlers

import (
	"context"
	"errors"
	"log"
	"net/http"

	"github.com/jackc/pgx/v5"

	"github.com/madalin/forgedesk/internal/auth"
	"github.com/madalin/forgedesk/internal/models"
)

// errCrossTenant signals that a non-staff user attempted to act on a resource
// outside of their org memberships, OR that the resource does not exist at
// all. Handlers translate this to 404 (not 403) so an attacker probing for
// valid IDs cannot distinguish "does not exist" from "belongs to another
// tenant". Any other error returned from the authorize* helpers is a real
// infrastructure failure and should surface as 5xx so ops can see it.
var errCrossTenant = errors.New("cross-tenant access denied")

// isRowMiss reports whether err is a wrapped pgx.ErrNoRows. Used by the
// authorize* helpers to decide whether a DB error means "not found" (map
// to errCrossTenant) or "real failure" (propagate as-is).
func isRowMiss(err error) bool {
	return errors.Is(err, pgx.ErrNoRows)
}

// authorizeOrgAccess returns nil when the user is allowed to act on
// resources under orgID.
//
// Staff and superadmin have global access. Client users must hold a
// membership row for the given org. "No membership" returns
// errCrossTenant; any other error from the datastore is propagated
// unchanged so callers can tell transient DB failures apart from
// legitimate permission denials. (#25 — review feedback)
func authorizeOrgAccess(ctx context.Context, db *models.DB, user *models.User, orgID string) error {
	if auth.IsStaffOrAbove(user.Role) {
		return nil
	}
	if _, err := db.GetOrgMembership(ctx, user.ID, orgID); err != nil {
		if isRowMiss(err) {
			return errCrossTenant
		}
		return err
	}
	return nil
}

// authorizeProjectAccess loads the project and verifies the user is
// authorized to act on its owning org. Used by write handlers that take a
// project_id from the request body (e.g. CreateTicket).
//
// A missing project maps to errCrossTenant (no existence leak); other
// datastore errors propagate unchanged.
func authorizeProjectAccess(ctx context.Context, db *models.DB, user *models.User, projectID string) error {
	proj, err := db.GetProjectByID(ctx, projectID)
	if err != nil {
		if isRowMiss(err) {
			return errCrossTenant
		}
		return err
	}
	return authorizeOrgAccess(ctx, db, user, proj.OrgID)
}

// authorizeTicketAccess loads the ticket and verifies the user is
// authorized to act on its owning org. Returns the ticket so callers
// that actually need to mutate its fields (UpdateTicket) avoid a
// second fetch. Handlers that only need the access check should call
// authorizeTicketOrgAccess instead, which runs a single join query
// and does not materialize the ticket.
func authorizeTicketAccess(ctx context.Context, db *models.DB, user *models.User, ticketID string) (*models.Ticket, error) {
	ticket, err := db.GetTicket(ctx, ticketID)
	if err != nil {
		if isRowMiss(err) {
			return nil, errCrossTenant
		}
		return nil, err
	}
	if err := authorizeProjectAccess(ctx, db, user, ticket.ProjectID); err != nil {
		return nil, err
	}
	return ticket, nil
}

// authorizeTicketOrgAccess is a lighter alternative to authorizeTicketAccess
// for handlers that only need the permission check and do not use the
// ticket body. It runs a single join (tickets JOIN projects) to resolve
// the owning org, then checks org membership. Used by UpdateStatus,
// CreateComment, and the ticket branch of ToggleReaction.
// (#25 — review feedback: Gemini efficiency suggestion)
func authorizeTicketOrgAccess(ctx context.Context, db *models.DB, user *models.User, ticketID string) error {
	orgID, err := db.ResolveTicketOrgID(ctx, ticketID)
	if err != nil {
		if isRowMiss(err) {
			return errCrossTenant
		}
		return err
	}
	return authorizeOrgAccess(ctx, db, user, orgID)
}

// authorizeCommentAccess verifies the user is authorized to act on a
// comment via its ticket → project → org chain in a single join query.
// Used by the comment branch of ToggleReaction.
func authorizeCommentAccess(ctx context.Context, db *models.DB, user *models.User, commentID string) error {
	orgID, err := db.ResolveCommentOrgID(ctx, commentID)
	if err != nil {
		if isRowMiss(err) {
			return errCrossTenant
		}
		return err
	}
	return authorizeOrgAccess(ctx, db, user, orgID)
}

// respondAuthzError translates an authorize* helper error into an HTTP
// response. errCrossTenant (including the "resource not found" case)
// maps to 404 with the caller-supplied message so an attacker probing
// IDs cannot learn which ones exist. Any other error is logged and
// surfaces as 500 so ops can see infrastructure incidents instead of
// having them masked as authorization denials.
func respondAuthzError(w http.ResponseWriter, err error, notFoundMsg string) {
	if errors.Is(err, errCrossTenant) {
		http.Error(w, notFoundMsg, http.StatusNotFound)
		return
	}
	log.Printf("authz check failed: %v", err)
	http.Error(w, "Internal error", http.StatusInternalServerError)
}
