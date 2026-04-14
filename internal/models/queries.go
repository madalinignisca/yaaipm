package models

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

type DB struct {
	Pool *pgxpool.Pool
}

// OrgRoleOwner is the "owner" org-membership role value used in
// org_memberships.role. Centralized here so goconst stops flagging
// the string literal.
const OrgRoleOwner = "owner"

func NewDB(pool *pgxpool.Pool) *DB {
	return &DB{Pool: pool}
}

// ── Users ─────────────────────────────────────────────────────────

func (db *DB) CreateUser(ctx context.Context, email, passwordHash, name, role string) (*User, error) {
	u := &User{}
	err := db.Pool.QueryRow(ctx,
		`INSERT INTO users (email, password_hash, name, role) VALUES ($1, $2, $3, $4)
		 RETURNING id, email, password_hash, name, role, totp_secret, totp_verified, recovery_codes, must_setup_2fa, preferred_2fa_method, created_at, updated_at`,
		email, passwordHash, name, role,
	).Scan(&u.ID, &u.Email, &u.PasswordHash, &u.Name, &u.Role, &u.TOTPSecret, &u.TOTPVerified, &u.RecoveryCodes, &u.MustSetup2FA, &u.Preferred2FAMethod, &u.CreatedAt, &u.UpdatedAt)
	if err != nil {
		return nil, fmt.Errorf("creating user: %w", err)
	}
	return u, nil
}

// ErrInvitationNotAcceptable is returned by AcceptInviteTx when the
// target invitation cannot be consumed — either it never existed,
// was already accepted, or is no longer pending. Keeps this case
// distinct from infrastructure failures so handlers can surface a
// dedicated error without masking 5xxs. (#28 — review feedback)
var ErrInvitationNotAcceptable = errors.New("invitation not acceptable")

// AcceptInviteTx atomically creates a user from an invitation, marks
// the invitation accepted, and adds the user to the target org with
// the invitation's role. If any step fails the entire operation is
// rolled back, so we never end up with a user account that has no
// membership and a half-consumed invitation. (#28)
//
// The invitation UPDATE is guarded with AND status = 'pending' so
// double-acceptance and stale-invitation acceptance both surface as
// ErrInvitationNotAcceptable via the RowsAffected == 0 branch,
// rather than silently committing a no-op.
func (db *DB) AcceptInviteTx(ctx context.Context, email, passwordHash, name, role, invitationID, orgID, orgRole string) (*User, error) {
	tx, err := db.Pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("beginning transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	u := &User{}
	if scanErr := tx.QueryRow(ctx,
		`INSERT INTO users (email, password_hash, name, role) VALUES ($1, $2, $3, $4)
		 RETURNING id, email, password_hash, name, role, totp_secret, totp_verified, recovery_codes, must_setup_2fa, preferred_2fa_method, created_at, updated_at`,
		email, passwordHash, name, role,
	).Scan(&u.ID, &u.Email, &u.PasswordHash, &u.Name, &u.Role, &u.TOTPSecret, &u.TOTPVerified, &u.RecoveryCodes, &u.MustSetup2FA, &u.Preferred2FAMethod, &u.CreatedAt, &u.UpdatedAt); scanErr != nil {
		return nil, fmt.Errorf("creating user: %w", scanErr)
	}

	tag, err := tx.Exec(ctx,
		`UPDATE invitations SET status = 'accepted', updated_at = now()
		 WHERE id = $1 AND status = 'pending'`,
		invitationID)
	if err != nil {
		return nil, fmt.Errorf("marking invitation accepted: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return nil, ErrInvitationNotAcceptable
	}

	if _, err := tx.Exec(ctx,
		`INSERT INTO org_memberships (user_id, org_id, role) VALUES ($1, $2, $3)
		 ON CONFLICT (user_id, org_id) DO UPDATE SET role = $3`,
		u.ID, orgID, orgRole); err != nil {
		return nil, fmt.Errorf("adding org membership: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("committing accept invite transaction: %w", err)
	}
	return u, nil
}

func (db *DB) GetUserByEmail(ctx context.Context, email string) (*User, error) {
	u := &User{}
	err := db.Pool.QueryRow(ctx,
		`SELECT id, email, password_hash, name, role, totp_secret, totp_verified, recovery_codes, must_setup_2fa, preferred_2fa_method, created_at, updated_at
		 FROM users WHERE email = $1`, email,
	).Scan(&u.ID, &u.Email, &u.PasswordHash, &u.Name, &u.Role, &u.TOTPSecret, &u.TOTPVerified, &u.RecoveryCodes, &u.MustSetup2FA, &u.Preferred2FAMethod, &u.CreatedAt, &u.UpdatedAt)
	if err != nil {
		return nil, fmt.Errorf("getting user by email: %w", err)
	}
	return u, nil
}

func (db *DB) GetUserByID(ctx context.Context, id string) (*User, error) {
	u := &User{}
	err := db.Pool.QueryRow(ctx,
		`SELECT id, email, password_hash, name, role, totp_secret, totp_verified, recovery_codes, must_setup_2fa, preferred_2fa_method, created_at, updated_at
		 FROM users WHERE id = $1`, id,
	).Scan(&u.ID, &u.Email, &u.PasswordHash, &u.Name, &u.Role, &u.TOTPSecret, &u.TOTPVerified, &u.RecoveryCodes, &u.MustSetup2FA, &u.Preferred2FAMethod, &u.CreatedAt, &u.UpdatedAt)
	if err != nil {
		return nil, fmt.Errorf("getting user by id: %w", err)
	}
	return u, nil
}

func (db *DB) UpdateUserTOTP(ctx context.Context, userID string, totpSecret []byte, verified bool, method string) error {
	_, err := db.Pool.Exec(ctx,
		`UPDATE users SET totp_secret = $1, totp_verified = $2, preferred_2fa_method = $3, must_setup_2fa = FALSE, updated_at = now() WHERE id = $4`,
		totpSecret, verified, method, userID)
	if err != nil {
		return fmt.Errorf("updating user totp: %w", err)
	}
	return nil
}

func (db *DB) UpdateUserRecoveryCodes(ctx context.Context, userID string, codes []byte) error {
	_, err := db.Pool.Exec(ctx,
		`UPDATE users SET recovery_codes = $1, updated_at = now() WHERE id = $2`, codes, userID)
	if err != nil {
		return fmt.Errorf("updating user recovery codes: %w", err)
	}
	return nil
}

func (db *DB) ClearUser2FA(ctx context.Context, userID string) error {
	_, err := db.Pool.Exec(ctx,
		`UPDATE users SET totp_secret = NULL, totp_verified = FALSE, recovery_codes = NULL, must_setup_2fa = TRUE, preferred_2fa_method = NULL, updated_at = now() WHERE id = $1`, userID)
	if err != nil {
		return fmt.Errorf("clearing user 2fa: %w", err)
	}
	return nil
}

func (db *DB) ConsumeRecoveryCode(ctx context.Context, userID string, codes []byte) error {
	_, err := db.Pool.Exec(ctx,
		`UPDATE users SET recovery_codes = $1, updated_at = now() WHERE id = $2`, codes, userID)
	if err != nil {
		return fmt.Errorf("consuming recovery code: %w", err)
	}
	return nil
}

func (db *DB) UpdateUserPassword(ctx context.Context, userID, hash string) error {
	_, err := db.Pool.Exec(ctx,
		`UPDATE users SET password_hash = $1, updated_at = now() WHERE id = $2`, hash, userID)
	if err != nil {
		return fmt.Errorf("updating user password: %w", err)
	}
	return nil
}

func (db *DB) UpdateUserEmail(ctx context.Context, userID, email string) error {
	_, err := db.Pool.Exec(ctx,
		`UPDATE users SET email = $1, updated_at = now() WHERE id = $2`, email, userID)
	if err != nil {
		return fmt.Errorf("updating user email: %w", err)
	}
	return nil
}

func (db *DB) CountUsers(ctx context.Context) (int, error) {
	var count int
	err := db.Pool.QueryRow(ctx, `SELECT COUNT(*) FROM users`).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("counting users: %w", err)
	}
	return count, nil
}

// ── Invitations ──────────────────────────────────────────────────

// ErrDuplicatePendingInvitation is returned when CreateInvitation
// hits the partial unique index idx_invitations_email_org_pending
// on (email, org_id) WHERE status = 'pending'. Distinct from a
// generic datastore error so handlers can respond with 409 Conflict
// instead of masking the race as 500. (#32)
var ErrDuplicatePendingInvitation = errors.New("a pending invitation already exists for this email and org")

func (db *DB) CreateInvitation(ctx context.Context, email, orgID, orgRole, tokenHash, invitedBy string, expiresAt time.Time) (*Invitation, error) {
	inv := &Invitation{}
	err := db.Pool.QueryRow(ctx,
		`INSERT INTO invitations (email, org_id, org_role, token_hash, invited_by, expires_at)
		 VALUES ($1, $2, $3, $4, $5, $6)
		 RETURNING id, email, org_id, org_role, token_hash, status, invited_by, expires_at, created_at, updated_at`,
		email, orgID, orgRole, tokenHash, invitedBy, expiresAt,
	).Scan(&inv.ID, &inv.Email, &inv.OrgID, &inv.OrgRole, &inv.TokenHash, &inv.Status, &inv.InvitedBy, &inv.ExpiresAt, &inv.CreatedAt, &inv.UpdatedAt)
	if err != nil {
		// Translate the partial-unique-index violation into a typed
		// sentinel so racing duplicate invites surface as 409 not 500.
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" &&
			pgErr.ConstraintName == "idx_invitations_email_org_pending" {
			return nil, ErrDuplicatePendingInvitation
		}
		return nil, fmt.Errorf("creating invitation: %w", err)
	}
	return inv, nil
}

func (db *DB) GetInvitationByToken(ctx context.Context, tokenHash string) (*Invitation, error) {
	inv := &Invitation{}
	err := db.Pool.QueryRow(ctx,
		`SELECT id, email, org_id, org_role, token_hash, status, invited_by, expires_at, created_at, updated_at
		 FROM invitations WHERE token_hash = $1 AND status = 'pending' AND expires_at > now()`,
		tokenHash,
	).Scan(&inv.ID, &inv.Email, &inv.OrgID, &inv.OrgRole, &inv.TokenHash, &inv.Status, &inv.InvitedBy, &inv.ExpiresAt, &inv.CreatedAt, &inv.UpdatedAt)
	if err != nil {
		return nil, fmt.Errorf("getting invitation by token: %w", err)
	}
	return inv, nil
}

func (db *DB) GetInvitationByID(ctx context.Context, id string) (*Invitation, error) {
	inv := &Invitation{}
	err := db.Pool.QueryRow(ctx,
		`SELECT id, email, org_id, org_role, token_hash, status, invited_by, expires_at, created_at, updated_at
		 FROM invitations WHERE id = $1`,
		id,
	).Scan(&inv.ID, &inv.Email, &inv.OrgID, &inv.OrgRole, &inv.TokenHash, &inv.Status, &inv.InvitedBy, &inv.ExpiresAt, &inv.CreatedAt, &inv.UpdatedAt)
	if err != nil {
		return nil, fmt.Errorf("getting invitation by id: %w", err)
	}
	return inv, nil
}

func (db *DB) ListPendingInvitationsForUser(ctx context.Context, email string) ([]InvitationWithOrg, error) {
	rows, err := db.Pool.Query(ctx,
		`SELECT i.id, i.email, i.org_id, i.org_role, i.token_hash, i.status, i.invited_by, i.expires_at, i.created_at, i.updated_at,
		        o.id, o.name, o.slug, o.ai_margin_percent, o.created_at, o.updated_at,
		        u.name
		 FROM invitations i
		 JOIN organizations o ON o.id = i.org_id
		 JOIN users u ON u.id = i.invited_by
		 WHERE i.email = $1 AND i.status = 'pending' AND i.expires_at > now()
		 ORDER BY i.created_at DESC`, email)
	if err != nil {
		return nil, fmt.Errorf("listing pending invitations for user: %w", err)
	}
	defer rows.Close()

	var results []InvitationWithOrg
	for rows.Next() {
		var r InvitationWithOrg
		if err := rows.Scan(
			&r.Invitation.ID, &r.Invitation.Email, &r.Invitation.OrgID, &r.Invitation.OrgRole,
			&r.Invitation.TokenHash, &r.Invitation.Status, &r.Invitation.InvitedBy,
			&r.Invitation.ExpiresAt, &r.Invitation.CreatedAt, &r.Invitation.UpdatedAt,
			&r.Organization.ID, &r.Organization.Name, &r.Organization.Slug,
			&r.Organization.AIMarginPercent, &r.Organization.CreatedAt, &r.Organization.UpdatedAt,
			&r.InviterName,
		); err != nil {
			return nil, fmt.Errorf("scanning pending invitation for user: %w", err)
		}
		results = append(results, r)
	}
	return results, rows.Err()
}

func (db *DB) ListOrgInvitations(ctx context.Context, orgID string) ([]InvitationWithInviter, error) {
	rows, err := db.Pool.Query(ctx,
		`SELECT i.id, i.email, i.org_id, i.org_role, i.token_hash, i.status, i.invited_by, i.expires_at, i.created_at, i.updated_at,
		        u.name
		 FROM invitations i
		 JOIN users u ON u.id = i.invited_by
		 WHERE i.org_id = $1 AND i.status = 'pending' AND i.expires_at > now()
		 ORDER BY i.created_at DESC`, orgID)
	if err != nil {
		return nil, fmt.Errorf("listing org invitations: %w", err)
	}
	defer rows.Close()

	var results []InvitationWithInviter
	for rows.Next() {
		var r InvitationWithInviter
		if err := rows.Scan(
			&r.Invitation.ID, &r.Invitation.Email, &r.Invitation.OrgID, &r.Invitation.OrgRole,
			&r.Invitation.TokenHash, &r.Invitation.Status, &r.Invitation.InvitedBy,
			&r.Invitation.ExpiresAt, &r.Invitation.CreatedAt, &r.Invitation.UpdatedAt,
			&r.InviterName,
		); err != nil {
			return nil, fmt.Errorf("scanning org invitation: %w", err)
		}
		results = append(results, r)
	}
	return results, rows.Err()
}

func (db *DB) UpdateInvitationStatus(ctx context.Context, id, status string) error {
	_, err := db.Pool.Exec(ctx,
		`UPDATE invitations SET status = $1, updated_at = now() WHERE id = $2`,
		status, id)
	if err != nil {
		return fmt.Errorf("updating invitation status: %w", err)
	}
	return nil
}

func (db *DB) ResetInvitationToken(ctx context.Context, id, newTokenHash string, newExpiry time.Time) error {
	_, err := db.Pool.Exec(ctx,
		`UPDATE invitations SET token_hash = $1, expires_at = $2, updated_at = now() WHERE id = $3`,
		newTokenHash, newExpiry, id)
	if err != nil {
		return fmt.Errorf("resetting invitation token: %w", err)
	}
	return nil
}

func (db *DB) ExpireOldInvitations(ctx context.Context) error {
	_, err := db.Pool.Exec(ctx,
		`UPDATE invitations SET status = 'expired', updated_at = now()
		 WHERE status = 'pending' AND expires_at <= now()`)
	if err != nil {
		return fmt.Errorf("expiring old invitations: %w", err)
	}
	return nil
}

func (db *DB) HasPendingInvitation(ctx context.Context, email, orgID string) (bool, error) {
	var exists bool
	err := db.Pool.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM invitations WHERE email = $1 AND org_id = $2 AND status = 'pending' AND expires_at > now())`,
		email, orgID).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("checking pending invitation: %w", err)
	}
	return exists, nil
}

// ── Organizations ─────────────────────────────────────────────────

// orgColumns is the column list for all organization queries.
const orgColumns = `id, name, slug, ai_margin_percent, currency_code,
	business_name, vat_number, registration_number,
	address_street, address_extra, postal_code, city, country,
	contact_phones, contact_emails,
	created_at, updated_at`

// prefixColumns adds a table alias prefix to each column in a comma-separated list.
// e.g. prefixColumns("o", "id, name") → "o.id, o.name"
func prefixColumns(alias, cols string) string {
	parts := strings.Split(cols, ",")
	for i, p := range parts {
		parts[i] = alias + "." + strings.TrimSpace(p)
	}
	return strings.Join(parts, ", ")
}

// scanOrg scans a row into an Organization struct. Column order must match orgColumns.
func scanOrg(scanner interface{ Scan(dest ...any) error }, o *Organization) error {
	return scanner.Scan(
		&o.ID, &o.Name, &o.Slug, &o.AIMarginPercent, &o.CurrencyCode,
		&o.BusinessName, &o.VATNumber, &o.RegistrationNumber,
		&o.AddressStreet, &o.AddressExtra, &o.PostalCode, &o.City, &o.Country,
		&o.ContactPhones, &o.ContactEmails,
		&o.CreatedAt, &o.UpdatedAt,
	)
}

func (db *DB) CreateOrg(ctx context.Context, name, slug string) (*Organization, error) {
	o := &Organization{}
	err := scanOrg(db.Pool.QueryRow(ctx,
		`INSERT INTO organizations (name, slug) VALUES ($1, $2) RETURNING `+orgColumns,
		name, slug,
	), o)
	if err != nil {
		return nil, fmt.Errorf("creating org: %w", err)
	}
	return o, nil
}

// CreateOrgWithOwnerTx atomically creates an organization and assigns
// the given user as its owner. If either the INSERT into organizations
// or the INSERT into org_memberships fails, the entire operation is
// rolled back. Prevents orgs from existing without an owner. (#29)
func (db *DB) CreateOrgWithOwnerTx(ctx context.Context, ownerUserID, name, slug, ownerRole string) (*Organization, error) {
	tx, err := db.Pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("beginning transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	o := &Organization{}
	if err := scanOrg(tx.QueryRow(ctx,
		`INSERT INTO organizations (name, slug) VALUES ($1, $2) RETURNING `+orgColumns,
		name, slug,
	), o); err != nil {
		return nil, fmt.Errorf("creating org: %w", err)
	}

	if _, err := tx.Exec(ctx,
		`INSERT INTO org_memberships (user_id, org_id, role) VALUES ($1, $2, $3)
		 ON CONFLICT (user_id, org_id) DO UPDATE SET role = $3`,
		ownerUserID, o.ID, ownerRole); err != nil {
		return nil, fmt.Errorf("adding owner membership: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("committing create org transaction: %w", err)
	}
	return o, nil
}

func (db *DB) GetOrgBySlug(ctx context.Context, slug string) (*Organization, error) {
	o := &Organization{}
	err := scanOrg(db.Pool.QueryRow(ctx,
		`SELECT `+orgColumns+` FROM organizations WHERE slug = $1`, slug,
	), o)
	if err != nil {
		return nil, fmt.Errorf("getting org by slug: %w", err)
	}
	return o, nil
}

func (db *DB) GetOrgByID(ctx context.Context, id string) (*Organization, error) {
	o := &Organization{}
	err := scanOrg(db.Pool.QueryRow(ctx,
		`SELECT `+orgColumns+` FROM organizations WHERE id = $1`, id,
	), o)
	if err != nil {
		return nil, fmt.Errorf("getting org by id: %w", err)
	}
	return o, nil
}

func (db *DB) ListUserOrgs(ctx context.Context, userID string) ([]Organization, error) {
	rows, err := db.Pool.Query(ctx,
		`SELECT `+prefixColumns("o", orgColumns)+`
		 FROM organizations o
		 JOIN org_memberships m ON m.org_id = o.id
		 WHERE m.user_id = $1
		 ORDER BY o.name`, userID)
	if err != nil {
		return nil, fmt.Errorf("listing user orgs: %w", err)
	}
	defer rows.Close()

	var orgs []Organization
	for rows.Next() {
		var o Organization
		if err := scanOrg(rows, &o); err != nil {
			return nil, fmt.Errorf("scanning user org: %w", err)
		}
		orgs = append(orgs, o)
	}
	return orgs, rows.Err()
}

func (db *DB) ListAllOrgs(ctx context.Context) ([]Organization, error) {
	rows, err := db.Pool.Query(ctx,
		`SELECT `+orgColumns+` FROM organizations ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("listing all orgs: %w", err)
	}
	defer rows.Close()

	var orgs []Organization
	for rows.Next() {
		var o Organization
		if err := scanOrg(rows, &o); err != nil {
			return nil, fmt.Errorf("scanning org: %w", err)
		}
		orgs = append(orgs, o)
	}
	return orgs, rows.Err()
}

func (db *DB) UpdateOrgBusinessDetails(ctx context.Context, orgID, businessName, vatNumber, registrationNumber, addressStreet, addressExtra, postalCode, city, country, contactPhones, contactEmails string) error {
	_, err := db.Pool.Exec(ctx,
		`UPDATE organizations SET
			business_name = $2, vat_number = $3, registration_number = $4,
			address_street = $5, address_extra = $6, postal_code = $7,
			city = $8, country = $9,
			contact_phones = $10, contact_emails = $11,
			updated_at = now()
		 WHERE id = $1`,
		orgID, businessName, vatNumber, registrationNumber,
		addressStreet, addressExtra, postalCode, city, country,
		contactPhones, contactEmails,
	)
	if err != nil {
		return fmt.Errorf("updating org business details: %w", err)
	}
	return nil
}

// --- Platform Settings ---

func (db *DB) GetPlatformSettings(ctx context.Context) (*PlatformSettings, error) {
	s := &PlatformSettings{}
	err := db.Pool.QueryRow(ctx,
		`SELECT business_name, vat_number, registration_number,
			address_street, address_extra, postal_code, city, country,
			contact_phones, contact_emails,
			created_at, updated_at
		 FROM platform_settings WHERE id = 1`,
	).Scan(
		&s.BusinessName, &s.VATNumber, &s.RegistrationNumber,
		&s.AddressStreet, &s.AddressExtra, &s.PostalCode, &s.City, &s.Country,
		&s.ContactPhones, &s.ContactEmails,
		&s.CreatedAt, &s.UpdatedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("getting platform settings: %w", err)
	}
	return s, nil
}

func (db *DB) UpdatePlatformSettings(ctx context.Context, businessName, vatNumber, registrationNumber, addressStreet, addressExtra, postalCode, city, country, contactPhones, contactEmails string) error {
	_, err := db.Pool.Exec(ctx,
		`UPDATE platform_settings SET
			business_name = $1, vat_number = $2, registration_number = $3,
			address_street = $4, address_extra = $5, postal_code = $6,
			city = $7, country = $8,
			contact_phones = $9, contact_emails = $10,
			updated_at = now()
		 WHERE id = 1`,
		businessName, vatNumber, registrationNumber,
		addressStreet, addressExtra, postalCode, city, country,
		contactPhones, contactEmails,
	)
	if err != nil {
		return fmt.Errorf("updating platform settings: %w", err)
	}
	return nil
}

func (db *DB) AddOrgMember(ctx context.Context, userID, orgID, role string) error {
	_, err := db.Pool.Exec(ctx,
		`INSERT INTO org_memberships (user_id, org_id, role) VALUES ($1, $2, $3)
		 ON CONFLICT (user_id, org_id) DO UPDATE SET role = $3`,
		userID, orgID, role)
	if err != nil {
		return fmt.Errorf("adding org member: %w", err)
	}
	return nil
}

// InsertOrgMembershipIfAbsent inserts a new membership row atomically,
// doing nothing if the user is already a member of the given org. Returns
// (true, nil) if a new row was inserted, (false, nil) if the user was
// already a member, or an error if the datastore operation failed.
//
// Unlike AddOrgMember, this is safe for invitation acceptance where the
// caller wants to refuse any role change when a membership already
// exists. It also eliminates the check-then-insert race between two
// concurrent accept requests. (#31)
func (db *DB) InsertOrgMembershipIfAbsent(ctx context.Context, userID, orgID, role string) (bool, error) {
	tag, err := db.Pool.Exec(ctx,
		`INSERT INTO org_memberships (user_id, org_id, role) VALUES ($1, $2, $3)
		 ON CONFLICT (user_id, org_id) DO NOTHING`,
		userID, orgID, role)
	if err != nil {
		return false, fmt.Errorf("inserting org membership if absent: %w", err)
	}
	return tag.RowsAffected() > 0, nil
}

func (db *DB) GetOrgMembership(ctx context.Context, userID, orgID string) (*OrgMembership, error) {
	m := &OrgMembership{}
	err := db.Pool.QueryRow(ctx,
		`SELECT id, user_id, org_id, role, created_at FROM org_memberships WHERE user_id = $1 AND org_id = $2`,
		userID, orgID,
	).Scan(&m.ID, &m.UserID, &m.OrgID, &m.Role, &m.CreatedAt)
	if err != nil {
		return nil, fmt.Errorf("getting org membership: %w", err)
	}
	return m, nil
}

func (db *DB) ListOrgMembers(ctx context.Context, orgID string) ([]struct {
	User       User
	Membership OrgMembership
}, error) {
	rows, err := db.Pool.Query(ctx,
		`SELECT u.id, u.email, u.name, u.role, u.must_setup_2fa, u.created_at,
		        m.id, m.role, m.created_at
		 FROM users u JOIN org_memberships m ON u.id = m.user_id
		 WHERE m.org_id = $1 ORDER BY u.name`, orgID)
	if err != nil {
		return nil, fmt.Errorf("listing org members: %w", err)
	}
	defer rows.Close()

	type member struct {
		User       User
		Membership OrgMembership
	}
	var members []member
	for rows.Next() {
		var m member
		if err := rows.Scan(&m.User.ID, &m.User.Email, &m.User.Name, &m.User.Role, &m.User.MustSetup2FA, &m.User.CreatedAt,
			&m.Membership.ID, &m.Membership.Role, &m.Membership.CreatedAt); err != nil {
			return nil, fmt.Errorf("scanning org member: %w", err)
		}
		m.Membership.OrgID = orgID
		m.Membership.UserID = m.User.ID
		members = append(members, m)
	}

	// Convert to exported type
	result := make([]struct {
		User       User
		Membership OrgMembership
	}, len(members))
	for i, m := range members {
		result[i].User = m.User
		result[i].Membership = m.Membership
	}
	return result, rows.Err()
}

func (db *DB) RemoveOrgMember(ctx context.Context, userID, orgID string) error {
	_, err := db.Pool.Exec(ctx,
		`DELETE FROM org_memberships WHERE user_id = $1 AND org_id = $2`,
		userID, orgID)
	if err != nil {
		return fmt.Errorf("removing org member: %w", err)
	}
	return nil
}

func (db *DB) UpdateOrgMemberRole(ctx context.Context, userID, orgID, role string) error {
	_, err := db.Pool.Exec(ctx,
		`UPDATE org_memberships SET role = $1 WHERE user_id = $2 AND org_id = $3`,
		role, userID, orgID)
	if err != nil {
		return fmt.Errorf("updating org member role: %w", err)
	}
	return nil
}

// ErrLastOwner is returned by the guarded owner-mutation helpers when
// the requested change would leave an organization with zero owners.
// Handlers should translate this to a 400 response.
var ErrLastOwner = errors.New("cannot remove or demote the last owner")

// ErrMemberNotFound is returned when a guarded mutation targets a
// membership row that does not exist. Distinct from infrastructure
// errors so handlers can translate it to a 404.
var ErrMemberNotFound = errors.New("org membership not found")

// lockOrgOwners holds an exclusive row-level lock on every owner row
// for the given org_id within the current transaction. It is the
// serialization point for the last-owner invariant (#30): two
// concurrent owner-mutation transactions serialize because the
// second one blocks on the SELECT FOR UPDATE until the first commits.
// Returns the user_ids of the currently-locked owners.
//
// ORDER BY user_id ensures concurrent transactions acquire the row
// locks in the same sequence, avoiding lock-ordering deadlocks.
func lockOrgOwners(ctx context.Context, tx pgx.Tx, orgID string) ([]string, error) {
	rows, err := tx.Query(ctx,
		`SELECT user_id FROM org_memberships
		 WHERE org_id = $1 AND role = 'owner'
		 ORDER BY user_id
		 FOR UPDATE`, orgID)
	if err != nil {
		return nil, fmt.Errorf("locking org owners: %w", err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var u string
		if err := rows.Scan(&u); err != nil {
			return nil, fmt.Errorf("scanning owner: %w", err)
		}
		ids = append(ids, u)
	}
	return ids, rows.Err()
}

// RemoveOrgMemberGuarded deletes the given membership atomically and
// returns ErrLastOwner if the deletion would leave the org with zero
// owners. Fixes #30: two concurrent RemoveMember requests on a
// two-owner org can no longer both pass the last-owner check.
func (db *DB) RemoveOrgMemberGuarded(ctx context.Context, userID, orgID string) error {
	tx, err := db.Pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("beginning transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	owners, err := lockOrgOwners(ctx, tx, orgID)
	if err != nil {
		return err
	}

	// Deletion leaves 0 owners iff the target is the sole owner.
	if len(owners) == 1 && owners[0] == userID {
		return ErrLastOwner
	}

	tag, err := tx.Exec(ctx,
		`DELETE FROM org_memberships WHERE user_id = $1 AND org_id = $2`,
		userID, orgID)
	if err != nil {
		return fmt.Errorf("removing org member: %w", err)
	}
	// A 0-row DELETE means the target was never a member of this org;
	// surface ErrMemberNotFound so the handler can return 404 instead
	// of silently reporting success.
	if tag.RowsAffected() == 0 {
		return ErrMemberNotFound
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("committing remove org member transaction: %w", err)
	}
	return nil
}

// UpdateOrgMemberRoleGuarded updates the given membership's role
// atomically and returns ErrLastOwner if demoting the target would
// leave the org with zero owners. Fixes #30 for the demotion path.
func (db *DB) UpdateOrgMemberRoleGuarded(ctx context.Context, userID, orgID, newRole string) error {
	tx, err := db.Pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("beginning transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	owners, err := lockOrgOwners(ctx, tx, orgID)
	if err != nil {
		return err
	}

	// Demoting to non-owner is the only mutation that can violate
	// the invariant. Promoting or keeping role='owner' is safe.
	if newRole != OrgRoleOwner && len(owners) == 1 && owners[0] == userID {
		return ErrLastOwner
	}

	tag, err := tx.Exec(ctx,
		`UPDATE org_memberships SET role = $1 WHERE user_id = $2 AND org_id = $3`,
		newRole, userID, orgID)
	if err != nil {
		return fmt.Errorf("updating org member role: %w", err)
	}
	// A 0-row UPDATE means no matching membership row; surface
	// ErrMemberNotFound so the handler returns 404 instead of
	// silently reporting success.
	if tag.RowsAffected() == 0 {
		return ErrMemberNotFound
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("committing update org member role transaction: %w", err)
	}
	return nil
}

func (db *DB) CountOrgOwners(ctx context.Context, orgID string) (int, error) {
	var count int
	err := db.Pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM org_memberships WHERE org_id = $1 AND role = 'owner'`, orgID).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("counting org owners: %w", err)
	}
	return count, nil
}

func (db *DB) UpdateOrgAIMargin(ctx context.Context, orgID string, marginPercent int) error {
	_, err := db.Pool.Exec(ctx,
		`UPDATE organizations SET ai_margin_percent = $1, updated_at = now() WHERE id = $2`,
		marginPercent, orgID)
	if err != nil {
		return fmt.Errorf("updating org ai margin: %w", err)
	}
	return nil
}

func (db *DB) UpdateOrgCurrency(ctx context.Context, orgID, currencyCode string) error {
	_, err := db.Pool.Exec(ctx,
		`UPDATE organizations SET currency_code = $1, updated_at = now() WHERE id = $2`,
		currencyCode, orgID)
	if err != nil {
		return fmt.Errorf("updating org currency: %w", err)
	}
	return nil
}

// ── Projects ──────────────────────────────────────────────────────

func (db *DB) CreateProject(ctx context.Context, orgID, name, slug string) (*Project, error) {
	p := &Project{}
	err := db.Pool.QueryRow(ctx,
		`INSERT INTO projects (org_id, name, slug) VALUES ($1, $2, $3)
		 RETURNING id, org_id, name, slug, brief_markdown, repo_url, created_at, updated_at`,
		orgID, name, slug,
	).Scan(&p.ID, &p.OrgID, &p.Name, &p.Slug, &p.BriefMarkdown, &p.RepoURL, &p.CreatedAt, &p.UpdatedAt)
	if err != nil {
		return nil, fmt.Errorf("creating project: %w", err)
	}
	return p, nil
}

func (db *DB) GetProject(ctx context.Context, orgID, slug string) (*Project, error) {
	p := &Project{}
	err := db.Pool.QueryRow(ctx,
		`SELECT id, org_id, name, slug, brief_markdown, repo_url, created_at, updated_at
		 FROM projects WHERE org_id = $1 AND slug = $2`, orgID, slug,
	).Scan(&p.ID, &p.OrgID, &p.Name, &p.Slug, &p.BriefMarkdown, &p.RepoURL, &p.CreatedAt, &p.UpdatedAt)
	if err != nil {
		return nil, fmt.Errorf("getting project: %w", err)
	}
	return p, nil
}

func (db *DB) GetProjectByID(ctx context.Context, id string) (*Project, error) {
	p := &Project{}
	err := db.Pool.QueryRow(ctx,
		`SELECT id, org_id, name, slug, brief_markdown, repo_url, created_at, updated_at
		 FROM projects WHERE id = $1`, id,
	).Scan(&p.ID, &p.OrgID, &p.Name, &p.Slug, &p.BriefMarkdown, &p.RepoURL, &p.CreatedAt, &p.UpdatedAt)
	if err != nil {
		return nil, fmt.Errorf("getting project by id: %w", err)
	}
	return p, nil
}

func (db *DB) ListProjects(ctx context.Context, orgID string) ([]Project, error) {
	rows, err := db.Pool.Query(ctx,
		`SELECT id, org_id, name, slug, brief_markdown, repo_url, created_at, updated_at
		 FROM projects WHERE org_id = $1 ORDER BY name`, orgID)
	if err != nil {
		return nil, fmt.Errorf("listing projects: %w", err)
	}
	defer rows.Close()

	var projects []Project
	for rows.Next() {
		var p Project
		if err := rows.Scan(&p.ID, &p.OrgID, &p.Name, &p.Slug, &p.BriefMarkdown, &p.RepoURL, &p.CreatedAt, &p.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scanning project: %w", err)
		}
		projects = append(projects, p)
	}
	return projects, rows.Err()
}

func (db *DB) UpdateProjectBrief(ctx context.Context, projectID, brief string) error {
	_, err := db.Pool.Exec(ctx,
		`UPDATE projects SET brief_markdown = $1, updated_at = now() WHERE id = $2`, brief, projectID)
	if err != nil {
		return fmt.Errorf("updating project brief: %w", err)
	}
	return nil
}

func (db *DB) UpdateProjectRepoURL(ctx context.Context, projectID, repoURL string) error {
	_, err := db.Pool.Exec(ctx,
		`UPDATE projects SET repo_url = $1, updated_at = now() WHERE id = $2`, repoURL, projectID)
	if err != nil {
		return fmt.Errorf("updating project repo url: %w", err)
	}
	return nil
}

func (db *DB) TransferProject(ctx context.Context, projectID, targetOrgID string) error {
	tx, err := db.Pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("beginning transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	_, err = tx.Exec(ctx,
		`UPDATE projects SET org_id = $1, updated_at = now() WHERE id = $2`, targetOrgID, projectID)
	if err != nil {
		return fmt.Errorf("transferring project: %w", err)
	}

	// Keep AI cost attribution consistent
	_, err = tx.Exec(ctx,
		`UPDATE ai_usage_entries SET org_id = $1 WHERE project_id = $2`, targetOrgID, projectID)
	if err != nil {
		return fmt.Errorf("updating ai usage org: %w", err)
	}

	return tx.Commit(ctx)
}

// ── Brief Revisions ──────────────────────────────────────────────

func (db *DB) CreateBriefRevision(ctx context.Context, projectID, userID, action, previousBrief string) error {
	_, err := db.Pool.Exec(ctx,
		`INSERT INTO brief_revisions (project_id, user_id, action, previous_brief) VALUES ($1, $2, $3, $4)`,
		projectID, userID, action, previousBrief)
	if err != nil {
		return fmt.Errorf("creating brief revision: %w", err)
	}
	return nil
}

func (db *DB) ListBriefRevisions(ctx context.Context, projectID string) ([]BriefRevision, error) {
	rows, err := db.Pool.Query(ctx,
		`SELECT r.id, r.project_id, r.user_id, r.action, r.previous_brief, r.created_at,
		        COALESCE(u.name, 'Unknown') AS user_name
		 FROM brief_revisions r
		 LEFT JOIN users u ON u.id = r.user_id
		 WHERE r.project_id = $1
		 ORDER BY r.created_at DESC
		 LIMIT 50`, projectID)
	if err != nil {
		return nil, fmt.Errorf("listing brief revisions: %w", err)
	}
	defer rows.Close()

	var revs []BriefRevision
	for rows.Next() {
		var r BriefRevision
		if err := rows.Scan(&r.ID, &r.ProjectID, &r.UserID, &r.Action, &r.PreviousBrief, &r.CreatedAt, &r.UserName); err != nil {
			return nil, fmt.Errorf("scanning brief revision: %w", err)
		}
		revs = append(revs, r)
	}
	return revs, rows.Err()
}

// ── Tickets ───────────────────────────────────────────────────────

func (db *DB) CreateTicket(ctx context.Context, t *Ticket) error {
	return db.Pool.QueryRow(ctx,
		`INSERT INTO tickets (project_id, parent_id, type, title, description_markdown, status, priority, date_start, date_end, agent_mode, agent_name, assigned_to, created_by)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
		 RETURNING id, archived_at, created_at, updated_at`,
		t.ProjectID, t.ParentID, t.Type, t.Title, t.DescriptionMarkdown, t.Status, t.Priority, t.DateStart, t.DateEnd, t.AgentMode, t.AgentName, t.AssignedTo, t.CreatedBy,
	).Scan(&t.ID, &t.ArchivedAt, &t.CreatedAt, &t.UpdatedAt)
}

func (db *DB) GetTicket(ctx context.Context, id string) (*Ticket, error) {
	t := &Ticket{}
	err := db.Pool.QueryRow(ctx,
		`SELECT id, project_id, parent_id, type, title, description_markdown, status, priority, date_start, date_end, agent_mode, agent_name, assigned_to, created_by, archived_at, created_at, updated_at
		 FROM tickets WHERE id = $1`, id,
	).Scan(&t.ID, &t.ProjectID, &t.ParentID, &t.Type, &t.Title, &t.DescriptionMarkdown, &t.Status, &t.Priority, &t.DateStart, &t.DateEnd, &t.AgentMode, &t.AgentName, &t.AssignedTo, &t.CreatedBy, &t.ArchivedAt, &t.CreatedAt, &t.UpdatedAt)
	if err != nil {
		return nil, fmt.Errorf("getting ticket: %w", err)
	}
	return t, nil
}

func (db *DB) ListTickets(ctx context.Context, projectID, ticketType string) ([]Ticket, error) {
	query := `SELECT id, project_id, parent_id, type, title, description_markdown, status, priority, date_start, date_end, agent_mode, agent_name, assigned_to, created_by, archived_at, created_at, updated_at
		 FROM tickets WHERE project_id = $1 AND archived_at IS NULL`
	args := []any{projectID}

	if ticketType != "" {
		query += " AND type = $2"
		args = append(args, ticketType)
	}
	query += " ORDER BY created_at DESC"

	rows, err := db.Pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("listing tickets: %w", err)
	}
	defer rows.Close()
	return scanTickets(rows)
}

func (db *DB) ListTicketsByParent(ctx context.Context, parentID string) ([]Ticket, error) {
	rows, err := db.Pool.Query(ctx,
		`SELECT id, project_id, parent_id, type, title, description_markdown, status, priority, date_start, date_end, agent_mode, agent_name, assigned_to, created_by, archived_at, created_at, updated_at
		 FROM tickets WHERE parent_id = $1 AND archived_at IS NULL ORDER BY created_at`, parentID)
	if err != nil {
		return nil, fmt.Errorf("listing tickets by parent: %w", err)
	}
	defer rows.Close()
	return scanTickets(rows)
}

func (db *DB) ListFeatures(ctx context.Context, projectID string) ([]Ticket, error) {
	tickets, err := db.ListTickets(ctx, projectID, "feature")
	if err != nil {
		return nil, fmt.Errorf("listing features: %w", err)
	}
	if err := db.populateChildCounts(ctx, tickets); err != nil {
		return tickets, nil //nolint:nilerr // non-fatal: return partial results
	}
	return tickets, nil
}

// populateChildCounts fills ChildCount for each ticket in the slice.
func (db *DB) populateChildCounts(ctx context.Context, tickets []Ticket) error {
	if len(tickets) == 0 {
		return nil
	}
	ids := make([]string, len(tickets))
	for i, t := range tickets {
		ids[i] = t.ID
	}
	rows, err := db.Pool.Query(ctx,
		`SELECT parent_id, COUNT(*) FROM tickets
		 WHERE parent_id = ANY($1) AND archived_at IS NULL
		 GROUP BY parent_id`, ids)
	if err != nil {
		return fmt.Errorf("populating child counts: %w", err)
	}
	defer rows.Close()
	counts := make(map[string]int)
	for rows.Next() {
		var parentID string
		var count int
		if err := rows.Scan(&parentID, &count); err != nil {
			return fmt.Errorf("scanning child count: %w", err)
		}
		counts[parentID] = count
	}
	for i := range tickets {
		tickets[i].ChildCount = counts[tickets[i].ID]
	}
	return nil
}

// ExpandParentDates walks up the parent chain from the given ticket,
// expanding each ancestor's date range to encompass the child's dates.
// If an ancestor has no dates, they are seeded from the child.
// Stops when there's no parent or no expansion is needed.
func (db *DB) ExpandParentDates(ctx context.Context, childStart, childEnd *time.Time, parentID *string) error {
	if parentID == nil || (childStart == nil && childEnd == nil) {
		return nil
	}
	// Walk up at most 5 levels (feature → task → subtask depth + safety margin)
	for i := 0; i < 5 && parentID != nil; i++ {
		parent, err := db.GetTicket(ctx, *parentID)
		if err != nil {
			return nil //nolint:nilerr // parent not found is not an error
		}

		changed := false

		if childStart != nil {
			if parent.DateStart == nil || childStart.Before(*parent.DateStart) {
				parent.DateStart = childStart
				changed = true
			}
		}
		if childEnd != nil {
			if parent.DateEnd == nil || childEnd.After(*parent.DateEnd) {
				parent.DateEnd = childEnd
				changed = true
			}
		}

		if !changed {
			return nil // parent already encompasses child, no need to go higher
		}

		_, err = db.Pool.Exec(ctx,
			`UPDATE tickets SET date_start = $1, date_end = $2, updated_at = now() WHERE id = $3`,
			parent.DateStart, parent.DateEnd, parent.ID)
		if err != nil {
			return fmt.Errorf("expanding parent %s dates: %w", parent.ID, err)
		}

		// Continue up the chain
		parentID = parent.ParentID
	}
	return nil
}

func (db *DB) ListBugs(ctx context.Context, projectID string) ([]Ticket, error) {
	return db.ListTickets(ctx, projectID, "bug")
}

func (db *DB) ListGanttTickets(ctx context.Context, projectID string) ([]Ticket, error) {
	rows, err := db.Pool.Query(ctx,
		`SELECT id, project_id, parent_id, type, title, description_markdown, status, priority, date_start, date_end, agent_mode, agent_name, assigned_to, created_by, archived_at, created_at, updated_at
		 FROM tickets WHERE project_id = $1 AND archived_at IS NULL AND date_start IS NOT NULL AND date_end IS NOT NULL
		 ORDER BY date_start`, projectID)
	if err != nil {
		return nil, fmt.Errorf("listing gantt tickets: %w", err)
	}
	defer rows.Close()
	return scanTickets(rows)
}

func (db *DB) UpdateTicketStatus(ctx context.Context, id, status string) error {
	_, err := db.Pool.Exec(ctx,
		`UPDATE tickets SET status = $1, updated_at = now() WHERE id = $2`, status, id)
	if err != nil {
		return fmt.Errorf("updating ticket status: %w", err)
	}
	return nil
}

func (db *DB) UpdateTicketAgentMode(ctx context.Context, id string, mode, agent *string) error {
	_, err := db.Pool.Exec(ctx,
		`UPDATE tickets SET agent_mode = $1, agent_name = $2, updated_at = now() WHERE id = $3`, mode, agent, id)
	if err != nil {
		return fmt.Errorf("updating ticket agent mode: %w", err)
	}
	return nil
}

func (db *DB) UpdateTicket(ctx context.Context, t *Ticket) error {
	_, err := db.Pool.Exec(ctx,
		`UPDATE tickets SET title = $1, description_markdown = $2, priority = $3, date_start = $4, date_end = $5, assigned_to = $6, updated_at = now() WHERE id = $7`,
		t.Title, t.DescriptionMarkdown, t.Priority, t.DateStart, t.DateEnd, t.AssignedTo, t.ID)
	if err != nil {
		return fmt.Errorf("updating ticket: %w", err)
	}
	return nil
}

// ListAgentReady returns tickets that are ready for agent processing.
func (db *DB) ListAgentReady(ctx context.Context) ([]Ticket, error) {
	rows, err := db.Pool.Query(ctx,
		`SELECT id, project_id, parent_id, type, title, description_markdown, status, priority, date_start, date_end, agent_mode, agent_name, assigned_to, created_by, archived_at, created_at, updated_at
		 FROM tickets WHERE agent_mode IS NOT NULL AND archived_at IS NULL AND status IN ('ready', 'plan_review')
		 ORDER BY priority DESC, created_at`)
	if err != nil {
		return nil, fmt.Errorf("listing agent ready tickets: %w", err)
	}
	defer rows.Close()
	return scanTickets(rows)
}

func scanTickets(rows pgx.Rows) ([]Ticket, error) {
	var tickets []Ticket
	for rows.Next() {
		var t Ticket
		if err := rows.Scan(&t.ID, &t.ProjectID, &t.ParentID, &t.Type, &t.Title, &t.DescriptionMarkdown, &t.Status, &t.Priority, &t.DateStart, &t.DateEnd, &t.AgentMode, &t.AgentName, &t.AssignedTo, &t.CreatedBy, &t.ArchivedAt, &t.CreatedAt, &t.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scanning ticket: %w", err)
		}
		tickets = append(tickets, t)
	}
	return tickets, rows.Err()
}

// ── Ticket Archive / Delete ───────────────────────────────────────

func (db *DB) ArchiveTicket(ctx context.Context, id string) error {
	// Recursively archive the whole subtree (ticket + children + grandchildren...).
	// The old query only reached one level with `parent_id = $1`, leaving
	// grandchildren visible and orphaned-looking.
	//
	// UNION (not UNION ALL) deduplicates against already-visited rows, which
	// makes the recursion terminate even if a cycle were ever introduced via
	// direct DB manipulation or a future bug in the parent_id update path.
	_, err := db.Pool.Exec(ctx,
		`WITH RECURSIVE subtree(id) AS (
			SELECT id FROM tickets WHERE id = $1
			UNION
			SELECT t.id FROM tickets t JOIN subtree s ON t.parent_id = s.id
		)
		UPDATE tickets SET archived_at = now(), updated_at = now()
		WHERE id IN (SELECT id FROM subtree)`, id)
	if err != nil {
		return fmt.Errorf("archiving ticket: %w", err)
	}
	return nil
}

func (db *DB) RestoreTicket(ctx context.Context, id string) error {
	// Recursively restore the whole subtree, mirroring ArchiveTicket.
	// UNION deduplicates to guarantee termination even on cyclic data.
	_, err := db.Pool.Exec(ctx,
		`WITH RECURSIVE subtree(id) AS (
			SELECT id FROM tickets WHERE id = $1
			UNION
			SELECT t.id FROM tickets t JOIN subtree s ON t.parent_id = s.id
		)
		UPDATE tickets SET archived_at = NULL, updated_at = now()
		WHERE id IN (SELECT id FROM subtree)`, id)
	if err != nil {
		return fmt.Errorf("restoring ticket: %w", err)
	}
	return nil
}

func (db *DB) DeleteTicket(ctx context.Context, id string) error {
	// Migration 000031 sets ON DELETE CASCADE on the (project_id, parent_id)
	// composite FK, so Postgres walks the whole subtree for us.
	_, err := db.Pool.Exec(ctx, `DELETE FROM tickets WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("deleting ticket: %w", err)
	}
	return nil
}

func (db *DB) ListArchivedTickets(ctx context.Context, projectID string) ([]Ticket, error) {
	rows, err := db.Pool.Query(ctx,
		`SELECT id, project_id, parent_id, type, title, description_markdown, status, priority, date_start, date_end, agent_mode, agent_name, assigned_to, created_by, archived_at, created_at, updated_at
		 FROM tickets WHERE project_id = $1 AND archived_at IS NOT NULL AND parent_id IS NULL
		 ORDER BY archived_at DESC`, projectID)
	if err != nil {
		return nil, fmt.Errorf("listing archived tickets: %w", err)
	}
	defer rows.Close()
	return scanTickets(rows)
}

// ── Comments ──────────────────────────────────────────────────────

func (db *DB) CreateComment(ctx context.Context, ticketID string, userID, agentName *string, body string) (*Comment, error) {
	c := &Comment{}
	err := db.Pool.QueryRow(ctx,
		`INSERT INTO comments (ticket_id, user_id, agent_name, body_markdown) VALUES ($1, $2, $3, $4)
		 RETURNING id, ticket_id, user_id, agent_name, body_markdown, created_at`,
		ticketID, userID, agentName, body,
	).Scan(&c.ID, &c.TicketID, &c.UserID, &c.AgentName, &c.BodyMarkdown, &c.CreatedAt)
	if err != nil {
		return nil, fmt.Errorf("creating comment: %w", err)
	}
	return c, nil
}

func (db *DB) ListComments(ctx context.Context, ticketID string) ([]Comment, error) {
	rows, err := db.Pool.Query(ctx,
		`SELECT c.id, c.ticket_id, c.user_id, c.agent_name, c.body_markdown, c.created_at
		 FROM comments c WHERE c.ticket_id = $1 ORDER BY c.created_at`, ticketID)
	if err != nil {
		return nil, fmt.Errorf("listing comments: %w", err)
	}
	defer rows.Close()

	var comments []Comment
	for rows.Next() {
		var c Comment
		if err := rows.Scan(&c.ID, &c.TicketID, &c.UserID, &c.AgentName, &c.BodyMarkdown, &c.CreatedAt); err != nil {
			return nil, fmt.Errorf("scanning comment: %w", err)
		}
		comments = append(comments, c)
	}
	return comments, rows.Err()
}

// ── Activities ────────────────────────────────────────────────────

func (db *DB) CreateActivity(ctx context.Context, ticketID string, userID, agentName *string, action, detailsJSON string) error {
	_, err := db.Pool.Exec(ctx,
		`INSERT INTO ticket_activities (ticket_id, user_id, agent_name, action, details_json) VALUES ($1, $2, $3, $4, $5)`,
		ticketID, userID, agentName, action, detailsJSON)
	if err != nil {
		return fmt.Errorf("creating activity: %w", err)
	}
	return nil
}

// ── WebAuthn Credentials ──────────────────────────────────────────

func (db *DB) CreateWebAuthnCredential(ctx context.Context, cred *WebAuthnCredential) error {
	_, err := db.Pool.Exec(ctx,
		`INSERT INTO webauthn_credentials (user_id, credential_id, public_key, attestation_type, authenticator_aaguid, sign_count, name)
		 VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		cred.UserID, cred.CredentialID, cred.PublicKey, cred.AttestationType, cred.AuthenticatorAAGUID, cred.SignCount, cred.Name)
	if err != nil {
		return fmt.Errorf("creating webauthn credential: %w", err)
	}
	return nil
}

func (db *DB) ListWebAuthnCredentials(ctx context.Context, userID string) ([]WebAuthnCredential, error) {
	rows, err := db.Pool.Query(ctx,
		`SELECT id, user_id, credential_id, public_key, attestation_type, authenticator_aaguid, sign_count, name, last_used_at, created_at
		 FROM webauthn_credentials WHERE user_id = $1 ORDER BY created_at`, userID)
	if err != nil {
		return nil, fmt.Errorf("listing webauthn credentials: %w", err)
	}
	defer rows.Close()

	var creds []WebAuthnCredential
	for rows.Next() {
		var c WebAuthnCredential
		if err := rows.Scan(&c.ID, &c.UserID, &c.CredentialID, &c.PublicKey, &c.AttestationType, &c.AuthenticatorAAGUID, &c.SignCount, &c.Name, &c.LastUsedAt, &c.CreatedAt); err != nil {
			return nil, fmt.Errorf("scanning webauthn credential: %w", err)
		}
		creds = append(creds, c)
	}
	return creds, rows.Err()
}

func (db *DB) UpdateWebAuthnSignCount(ctx context.Context, credID string, signCount uint32) error {
	_, err := db.Pool.Exec(ctx,
		`UPDATE webauthn_credentials SET sign_count = $1, last_used_at = $2 WHERE id = $3`,
		signCount, time.Now(), credID)
	if err != nil {
		return fmt.Errorf("updating webauthn sign count: %w", err)
	}
	return nil
}

func (db *DB) DeleteWebAuthnCredential(ctx context.Context, credID string) error {
	_, err := db.Pool.Exec(ctx, `DELETE FROM webauthn_credentials WHERE id = $1`, credID)
	if err != nil {
		return fmt.Errorf("deleting webauthn credential: %w", err)
	}
	return nil
}

func (db *DB) DeleteAllWebAuthnCredentials(ctx context.Context, userID string) error {
	_, err := db.Pool.Exec(ctx, `DELETE FROM webauthn_credentials WHERE user_id = $1`, userID)
	if err != nil {
		return fmt.Errorf("deleting all webauthn credentials: %w", err)
	}
	return nil
}

func (db *DB) CountUserWebAuthnCredentials(ctx context.Context, userID string) (int, error) {
	var count int
	err := db.Pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM webauthn_credentials WHERE user_id = $1`, userID).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("counting user webauthn credentials: %w", err)
	}
	return count, nil
}

// ── Users listing ──────────────────────────────────────────────────

// ── AI Conversations ──────────────────────────────────────────

func (db *DB) CreateAIConversation(ctx context.Context, userID string, projectID *string) (*AIConversation, error) {
	c := &AIConversation{}
	err := db.Pool.QueryRow(ctx,
		`INSERT INTO ai_conversations (user_id, project_id) VALUES ($1, $2)
		 RETURNING id, user_id, project_id, title, created_at, updated_at`,
		userID, projectID,
	).Scan(&c.ID, &c.UserID, &c.ProjectID, &c.Title, &c.CreatedAt, &c.UpdatedAt)
	if err != nil {
		return nil, fmt.Errorf("creating ai conversation: %w", err)
	}
	return c, nil
}

func (db *DB) GetAIConversation(ctx context.Context, id string) (*AIConversation, error) {
	c := &AIConversation{}
	err := db.Pool.QueryRow(ctx,
		`SELECT id, user_id, project_id, title, created_at, updated_at
		 FROM ai_conversations WHERE id = $1`, id,
	).Scan(&c.ID, &c.UserID, &c.ProjectID, &c.Title, &c.CreatedAt, &c.UpdatedAt)
	if err != nil {
		return nil, fmt.Errorf("getting ai conversation: %w", err)
	}
	return c, nil
}

func (db *DB) GetLatestAIConversation(ctx context.Context, userID string, projectID *string) (*AIConversation, error) {
	c := &AIConversation{}
	var err error
	if projectID != nil {
		err = db.Pool.QueryRow(ctx,
			`SELECT id, user_id, project_id, title, created_at, updated_at
			 FROM ai_conversations
			 WHERE user_id = $1 AND project_id = $2 AND updated_at > now() - interval '1 hour'
			 ORDER BY updated_at DESC LIMIT 1`,
			userID, *projectID,
		).Scan(&c.ID, &c.UserID, &c.ProjectID, &c.Title, &c.CreatedAt, &c.UpdatedAt)
	} else {
		err = db.Pool.QueryRow(ctx,
			`SELECT id, user_id, project_id, title, created_at, updated_at
			 FROM ai_conversations
			 WHERE user_id = $1 AND project_id IS NULL AND updated_at > now() - interval '1 hour'
			 ORDER BY updated_at DESC LIMIT 1`,
			userID,
		).Scan(&c.ID, &c.UserID, &c.ProjectID, &c.Title, &c.CreatedAt, &c.UpdatedAt)
	}
	if err != nil {
		return nil, fmt.Errorf("getting latest ai conversation: %w", err)
	}
	return c, nil
}

func (db *DB) UpdateAIConversationTitle(ctx context.Context, id, title string) error {
	_, err := db.Pool.Exec(ctx,
		`UPDATE ai_conversations SET title = $1, updated_at = now() WHERE id = $2`, title, id)
	if err != nil {
		return fmt.Errorf("updating ai conversation title: %w", err)
	}
	return nil
}

func (db *DB) TouchAIConversation(ctx context.Context, id string) error {
	_, err := db.Pool.Exec(ctx,
		`UPDATE ai_conversations SET updated_at = now() WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("touching ai conversation: %w", err)
	}
	return nil
}

func (db *DB) ListAIConversations(ctx context.Context, userID string, limit int) ([]AIConversation, error) {
	rows, err := db.Pool.Query(ctx,
		`SELECT id, user_id, project_id, title, created_at, updated_at
		 FROM ai_conversations WHERE user_id = $1 ORDER BY updated_at DESC LIMIT $2`,
		userID, limit)
	if err != nil {
		return nil, fmt.Errorf("listing ai conversations: %w", err)
	}
	defer rows.Close()

	var convs []AIConversation
	for rows.Next() {
		var c AIConversation
		if err := rows.Scan(&c.ID, &c.UserID, &c.ProjectID, &c.Title, &c.CreatedAt, &c.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scanning ai conversation: %w", err)
		}
		convs = append(convs, c)
	}
	return convs, rows.Err()
}

func (db *DB) CreateAIMessage(ctx context.Context, conversationID, role, content string) (*AIMessage, error) {
	m := &AIMessage{}
	err := db.Pool.QueryRow(ctx,
		`INSERT INTO ai_messages (conversation_id, role, content, user_id, user_name) VALUES ($1, $2, $3, NULL, '')
		 RETURNING id, conversation_id, role, content, user_id, user_name, created_at`,
		conversationID, role, content,
	).Scan(&m.ID, &m.ConversationID, &m.Role, &m.Content, &m.UserID, &m.UserName, &m.CreatedAt)
	if err != nil {
		return nil, fmt.Errorf("creating ai message: %w", err)
	}
	return m, nil
}

func (db *DB) CreateAIMessageWithUser(ctx context.Context, conversationID, role, content string, userID *string, userName string) (*AIMessage, error) {
	m := &AIMessage{}
	err := db.Pool.QueryRow(ctx,
		`INSERT INTO ai_messages (conversation_id, role, content, user_id, user_name) VALUES ($1, $2, $3, $4, $5)
		 RETURNING id, conversation_id, role, content, user_id, user_name, created_at`,
		conversationID, role, content, userID, userName,
	).Scan(&m.ID, &m.ConversationID, &m.Role, &m.Content, &m.UserID, &m.UserName, &m.CreatedAt)
	if err != nil {
		return nil, fmt.Errorf("creating ai message: %w", err)
	}
	return m, nil
}

func (db *DB) ListAIMessages(ctx context.Context, conversationID string) ([]AIMessage, error) {
	rows, err := db.Pool.Query(ctx,
		`SELECT id, conversation_id, role, content, user_id, user_name, created_at
		 FROM ai_messages WHERE conversation_id = $1 ORDER BY created_at`,
		conversationID)
	if err != nil {
		return nil, fmt.Errorf("listing ai messages: %w", err)
	}
	defer rows.Close()

	var msgs []AIMessage
	for rows.Next() {
		var m AIMessage
		if err := rows.Scan(&m.ID, &m.ConversationID, &m.Role, &m.Content, &m.UserID, &m.UserName, &m.CreatedAt); err != nil {
			return nil, fmt.Errorf("scanning ai message: %w", err)
		}
		msgs = append(msgs, m)
	}
	return msgs, rows.Err()
}

func (db *DB) GetOrCreateProjectConversation(ctx context.Context, projectID, createdByUserID string) (*AIConversation, error) {
	c := &AIConversation{}
	err := db.Pool.QueryRow(ctx,
		`INSERT INTO ai_conversations (user_id, project_id)
		 VALUES ($1, $2)
		 ON CONFLICT (project_id) WHERE project_id IS NOT NULL
		 DO UPDATE SET updated_at = now()
		 RETURNING id, user_id, project_id, title, created_at, updated_at`,
		createdByUserID, projectID,
	).Scan(&c.ID, &c.UserID, &c.ProjectID, &c.Title, &c.CreatedAt, &c.UpdatedAt)
	if err != nil {
		return nil, fmt.Errorf("get or create project conversation: %w", err)
	}
	return c, nil
}

func (db *DB) DeleteAIConversation(ctx context.Context, id string) error {
	_, err := db.Pool.Exec(ctx, `DELETE FROM ai_conversations WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("deleting ai conversation: %w", err)
	}
	return nil
}

// SearchTickets searches tickets by title matching a query within a project.
func (db *DB) SearchTickets(ctx context.Context, projectID, query string, ticketType, status *string) ([]Ticket, error) {
	sql := `SELECT id, project_id, parent_id, type, title, description_markdown, status, priority, date_start, date_end, agent_mode, agent_name, assigned_to, created_by, archived_at, created_at, updated_at
		 FROM tickets WHERE project_id = $1 AND archived_at IS NULL AND (title ILIKE '%' || $2 || '%' OR description_markdown ILIKE '%' || $2 || '%')`
	args := []any{projectID, query}
	n := 3

	if ticketType != nil && *ticketType != "" {
		sql += fmt.Sprintf(" AND type = $%d", n)
		args = append(args, *ticketType)
		n++
	}
	if status != nil && *status != "" {
		sql += fmt.Sprintf(" AND status = $%d", n)
		args = append(args, *status)
	}
	sql += " ORDER BY created_at DESC LIMIT 20"

	rows, err := db.Pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, fmt.Errorf("searching tickets: %w", err)
	}
	defer rows.Close()
	return scanTickets(rows)
}

// ── Users listing ──────────────────────────────────────────────────

// ── Project Costs ─────────────────────────────────────────────

func (db *DB) CreateProjectCost(ctx context.Context, projectID, month, category, name string, amountCents int64) (*ProjectCost, error) {
	c := &ProjectCost{}
	err := db.Pool.QueryRow(ctx,
		`INSERT INTO project_costs (project_id, month, category, name, amount_cents)
		 VALUES ($1, $2, $3, $4, $5)
		 RETURNING id, project_id, month, category, name, amount_cents, created_at, updated_at`,
		projectID, month, category, name, amountCents,
	).Scan(&c.ID, &c.ProjectID, &c.Month, &c.Category, &c.Name, &c.AmountCents, &c.CreatedAt, &c.UpdatedAt)
	if err != nil {
		return nil, fmt.Errorf("creating project cost: %w", err)
	}
	return c, nil
}

func (db *DB) GetProjectCost(ctx context.Context, id string) (*ProjectCost, error) {
	c := &ProjectCost{}
	err := db.Pool.QueryRow(ctx,
		`SELECT id, project_id, month, category, name, amount_cents, created_at, updated_at
		 FROM project_costs WHERE id = $1`, id,
	).Scan(&c.ID, &c.ProjectID, &c.Month, &c.Category, &c.Name, &c.AmountCents, &c.CreatedAt, &c.UpdatedAt)
	if err != nil {
		return nil, fmt.Errorf("getting project cost: %w", err)
	}
	return c, nil
}

func (db *DB) UpdateProjectCost(ctx context.Context, id string, amountCents int64) error {
	_, err := db.Pool.Exec(ctx,
		`UPDATE project_costs SET amount_cents = $1, updated_at = now() WHERE id = $2`,
		amountCents, id)
	if err != nil {
		return fmt.Errorf("updating project cost: %w", err)
	}
	return nil
}

func (db *DB) DeleteProjectCost(ctx context.Context, id string) error {
	_, err := db.Pool.Exec(ctx, `DELETE FROM project_costs WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("deleting project cost: %w", err)
	}
	return nil
}

func (db *DB) ListProjectCosts(ctx context.Context, projectID, month string) ([]ProjectCost, error) {
	rows, err := db.Pool.Query(ctx,
		`SELECT id, project_id, month, category, name, amount_cents, created_at, updated_at
		 FROM project_costs WHERE project_id = $1 AND month = $2
		 ORDER BY category, name`, projectID, month)
	if err != nil {
		return nil, fmt.Errorf("listing project costs: %w", err)
	}
	defer rows.Close()

	var costs []ProjectCost
	for rows.Next() {
		var c ProjectCost
		if err := rows.Scan(&c.ID, &c.ProjectID, &c.Month, &c.Category, &c.Name, &c.AmountCents, &c.CreatedAt, &c.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scanning project cost: %w", err)
		}
		costs = append(costs, c)
	}
	return costs, rows.Err()
}

func (db *DB) ListOrgCostsByMonth(ctx context.Context, orgID, month string) ([]ProjectCost, error) {
	rows, err := db.Pool.Query(ctx,
		`SELECT pc.id, pc.project_id, pc.month, pc.category, pc.name, pc.amount_cents, pc.created_at, pc.updated_at
		 FROM project_costs pc
		 JOIN projects p ON p.id = pc.project_id
		 WHERE p.org_id = $1 AND pc.month = $2
		 ORDER BY p.name, pc.category, pc.name`, orgID, month)
	if err != nil {
		return nil, fmt.Errorf("listing org costs by month: %w", err)
	}
	defer rows.Close()

	var costs []ProjectCost
	for rows.Next() {
		var c ProjectCost
		if err := rows.Scan(&c.ID, &c.ProjectID, &c.Month, &c.Category, &c.Name, &c.AmountCents, &c.CreatedAt, &c.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scanning org cost: %w", err)
		}
		costs = append(costs, c)
	}
	return costs, rows.Err()
}

// ── AI Usage ──────────────────────────────────────────────────

func (db *DB) CreateAIUsageEntry(ctx context.Context, orgID string, projectID, userID *string, model, label string, inputTokens, outputTokens int, costCents int64) error {
	_, err := db.Pool.Exec(ctx,
		`INSERT INTO ai_usage_entries (org_id, project_id, user_id, model, label, input_tokens, output_tokens, cost_cents)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		orgID, projectID, userID, model, label, inputTokens, outputTokens, costCents)
	if err != nil {
		return fmt.Errorf("creating ai usage entry: %w", err)
	}
	return nil
}

func (db *DB) ListAIUsageByProjectMonth(ctx context.Context, projectID, month string) ([]AIUsageSummary, error) {
	rows, err := db.Pool.Query(ctx,
		`SELECT model, label,
		        SUM(input_tokens)::BIGINT,
		        SUM(output_tokens)::BIGINT,
		        COALESCE(SUM(cost_cents), 0)::BIGINT,
		        COUNT(*)::INT
		 FROM ai_usage_entries
		 WHERE project_id = $1 AND to_char(created_at, 'YYYY-MM') = $2
		 GROUP BY model, label ORDER BY model`, projectID, month)
	if err != nil {
		return nil, fmt.Errorf("listing ai usage by project month: %w", err)
	}
	defer rows.Close()

	var summaries []AIUsageSummary
	for rows.Next() {
		var s AIUsageSummary
		if err := rows.Scan(&s.Model, &s.Label, &s.InputTokens, &s.OutputTokens, &s.TotalCents, &s.EntryCount); err != nil {
			return nil, fmt.Errorf("scanning ai usage summary: %w", err)
		}
		summaries = append(summaries, s)
	}
	return summaries, rows.Err()
}

func (db *DB) ListAIUsageByOrgMonth(ctx context.Context, orgID, month string) ([]AIUsageSummary, error) {
	rows, err := db.Pool.Query(ctx,
		`SELECT model, label,
		        SUM(input_tokens)::BIGINT,
		        SUM(output_tokens)::BIGINT,
		        COALESCE(SUM(cost_cents), 0)::BIGINT,
		        COUNT(*)::INT
		 FROM ai_usage_entries
		 WHERE org_id = $1 AND to_char(created_at, 'YYYY-MM') = $2
		 GROUP BY model, label ORDER BY model`, orgID, month)
	if err != nil {
		return nil, fmt.Errorf("listing ai usage by org month: %w", err)
	}
	defer rows.Close()

	var summaries []AIUsageSummary
	for rows.Next() {
		var s AIUsageSummary
		if err := rows.Scan(&s.Model, &s.Label, &s.InputTokens, &s.OutputTokens, &s.TotalCents, &s.EntryCount); err != nil {
			return nil, fmt.Errorf("scanning ai usage summary: %w", err)
		}
		summaries = append(summaries, s)
	}
	return summaries, rows.Err()
}

func (db *DB) GetTotalAIUsageCentsForOrgMonth(ctx context.Context, orgID, month string) (int64, error) {
	var total int64
	err := db.Pool.QueryRow(ctx,
		`SELECT COALESCE(SUM(cost_cents), 0)::BIGINT
		 FROM ai_usage_entries
		 WHERE org_id = $1 AND to_char(created_at, 'YYYY-MM') = $2`,
		orgID, month).Scan(&total)
	if err != nil {
		return 0, fmt.Errorf("getting total ai usage cents for org month: %w", err)
	}
	return total, nil
}

// ── Cost Months ───────────────────────────────────────────────

func (db *DB) ListCostMonths(ctx context.Context, orgID string) ([]string, error) {
	rows, err := db.Pool.Query(ctx,
		`SELECT DISTINCT month FROM (
		   SELECT pc.month FROM project_costs pc JOIN projects p ON p.id = pc.project_id WHERE p.org_id = $1
		   UNION
		   SELECT to_char(a.created_at, 'YYYY-MM') AS month FROM ai_usage_entries a WHERE a.org_id = $1
		 ) AS months ORDER BY month DESC`, orgID)
	if err != nil {
		return nil, fmt.Errorf("listing cost months: %w", err)
	}
	defer rows.Close()

	var months []string
	for rows.Next() {
		var m string
		if err := rows.Scan(&m); err != nil {
			return nil, fmt.Errorf("scanning cost month: %w", err)
		}
		months = append(months, m)
	}
	return months, rows.Err()
}

func (db *DB) ListUsers(ctx context.Context) ([]User, error) {
	rows, err := db.Pool.Query(ctx,
		`SELECT id, email, '', name, role, NULL, totp_verified, NULL, must_setup_2fa, preferred_2fa_method, created_at, updated_at
		 FROM users ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("listing users: %w", err)
	}
	defer rows.Close()

	var users []User
	for rows.Next() {
		var u User
		if err := rows.Scan(&u.ID, &u.Email, &u.PasswordHash, &u.Name, &u.Role, &u.TOTPSecret, &u.TOTPVerified, &u.RecoveryCodes, &u.MustSetup2FA, &u.Preferred2FAMethod, &u.CreatedAt, &u.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scanning user: %w", err)
		}
		users = append(users, u)
	}
	return users, rows.Err()
}

// ── Ticket Attachments ───────────────────────────────────────────

func (db *DB) CreateAttachment(ctx context.Context, ticketID, fileName, filePath, contentType string, sizeBytes int64, uploadedBy string) (*TicketAttachment, error) {
	var a TicketAttachment
	err := db.Pool.QueryRow(ctx,
		`INSERT INTO ticket_attachments (ticket_id, file_name, file_path, content_type, size_bytes, uploaded_by)
		 VALUES ($1, $2, $3, $4, $5, $6)
		 RETURNING id, ticket_id, file_name, file_path, content_type, size_bytes, uploaded_by, created_at`,
		ticketID, fileName, filePath, contentType, sizeBytes, uploadedBy,
	).Scan(&a.ID, &a.TicketID, &a.FileName, &a.FilePath, &a.ContentType, &a.SizeBytes, &a.UploadedBy, &a.CreatedAt)
	if err != nil {
		return nil, fmt.Errorf("creating attachment: %w", err)
	}
	return &a, nil
}

func (db *DB) ListAttachmentsByTicket(ctx context.Context, ticketID string) ([]TicketAttachment, error) {
	rows, err := db.Pool.Query(ctx,
		`SELECT id, ticket_id, file_name, file_path, content_type, size_bytes, uploaded_by, created_at
		 FROM ticket_attachments WHERE ticket_id = $1 ORDER BY created_at`, ticketID)
	if err != nil {
		return nil, fmt.Errorf("listing attachments by ticket: %w", err)
	}
	defer rows.Close()

	var attachments []TicketAttachment
	for rows.Next() {
		var a TicketAttachment
		if err := rows.Scan(&a.ID, &a.TicketID, &a.FileName, &a.FilePath, &a.ContentType, &a.SizeBytes, &a.UploadedBy, &a.CreatedAt); err != nil {
			return nil, fmt.Errorf("scanning attachment: %w", err)
		}
		attachments = append(attachments, a)
	}
	return attachments, rows.Err()
}

func (db *DB) DeleteAttachment(ctx context.Context, id string) error {
	_, err := db.Pool.Exec(ctx, `DELETE FROM ticket_attachments WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("deleting attachment: %w", err)
	}
	return nil
}

// ── Reactions ─────────────────────────────────────────────────────

// ToggleReaction adds or removes an emoji reaction. Returns true if added, false if removed.
func (db *DB) ToggleReaction(ctx context.Context, targetType, targetID, userID, emoji string) (bool, error) {
	// Try to delete first
	tag, err := db.Pool.Exec(ctx,
		`DELETE FROM reactions WHERE target_type = $1 AND target_id = $2 AND user_id = $3 AND emoji = $4`,
		targetType, targetID, userID, emoji)
	if err != nil {
		return false, fmt.Errorf("toggling reaction: %w", err)
	}
	if tag.RowsAffected() > 0 {
		return false, nil // removed
	}
	// Not found — insert
	_, err = db.Pool.Exec(ctx,
		`INSERT INTO reactions (target_type, target_id, user_id, emoji) VALUES ($1, $2, $3, $4)`,
		targetType, targetID, userID, emoji)
	if err != nil {
		return false, fmt.Errorf("adding reaction: %w", err)
	}
	return true, nil
}

// ListReactionGroups returns grouped emoji counts for a target, including whether the current user reacted.
func (db *DB) ListReactionGroups(ctx context.Context, targetType, targetID, currentUserID string) ([]ReactionGroup, error) {
	rows, err := db.Pool.Query(ctx,
		`SELECT emoji, COUNT(*) AS cnt,
		        bool_or(user_id = $3) AS user_reacted
		 FROM reactions
		 WHERE target_type = $1 AND target_id = $2
		 GROUP BY emoji
		 ORDER BY MIN(created_at)`,
		targetType, targetID, currentUserID)
	if err != nil {
		return nil, fmt.Errorf("listing reactions: %w", err)
	}
	defer rows.Close()

	var groups []ReactionGroup
	for rows.Next() {
		var g ReactionGroup
		if err := rows.Scan(&g.Emoji, &g.Count, &g.UserReacted); err != nil {
			return nil, fmt.Errorf("scanning reaction group: %w", err)
		}
		groups = append(groups, g)
	}
	return groups, rows.Err()
}

// ListReactionGroupsBatch returns reaction groups for multiple targets of the same type.
func (db *DB) ListReactionGroupsBatch(ctx context.Context, targetType string, targetIDs []string, currentUserID string) (map[string][]ReactionGroup, error) {
	result := make(map[string][]ReactionGroup)
	if len(targetIDs) == 0 {
		return result, nil
	}

	rows, err := db.Pool.Query(ctx,
		`SELECT target_id, emoji, COUNT(*) AS cnt,
		        bool_or(user_id = $3) AS user_reacted
		 FROM reactions
		 WHERE target_type = $1 AND target_id = ANY($2)
		 GROUP BY target_id, emoji
		 ORDER BY target_id, MIN(created_at)`,
		targetType, targetIDs, currentUserID)
	if err != nil {
		return nil, fmt.Errorf("batch listing reactions: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var targetID string
		var g ReactionGroup
		if err := rows.Scan(&targetID, &g.Emoji, &g.Count, &g.UserReacted); err != nil {
			return nil, fmt.Errorf("scanning reaction group batch: %w", err)
		}
		result[targetID] = append(result[targetID], g)
	}
	return result, rows.Err()
}

// ── Org-Scoped Queries (Data Isolation) ──────────────────────

// ResolveTicketOrgID returns the org ID that owns a ticket by walking
// the ticket → project → org chain in a single join. Used by cross-tenant
// authorization helpers that only need the access check, not the ticket
// body itself. Returns pgx.ErrNoRows (wrapped) when the ticket does not exist.
func (db *DB) ResolveTicketOrgID(ctx context.Context, ticketID string) (string, error) {
	var orgID string
	err := db.Pool.QueryRow(ctx,
		`SELECT p.org_id
		   FROM tickets t
		   JOIN projects p ON p.id = t.project_id
		  WHERE t.id = $1`,
		ticketID,
	).Scan(&orgID)
	if err != nil {
		return "", fmt.Errorf("resolving ticket org: %w", err)
	}
	return orgID, nil
}

// ResolveCommentOrgID returns the org ID that owns a comment by walking
// the comment → ticket → project → org chain. Used by cross-tenant
// authorization checks on comment-targeted reactions.
func (db *DB) ResolveCommentOrgID(ctx context.Context, commentID string) (string, error) {
	var orgID string
	err := db.Pool.QueryRow(ctx,
		`SELECT p.org_id
		   FROM comments c
		   JOIN tickets t ON t.id = c.ticket_id
		   JOIN projects p ON p.id = t.project_id
		  WHERE c.id = $1`,
		commentID,
	).Scan(&orgID)
	if err != nil {
		return "", fmt.Errorf("resolving comment org: %w", err)
	}
	return orgID, nil
}

// GetTicketScoped fetches a ticket only if it belongs to a project within the given org.
func (db *DB) GetTicketScoped(ctx context.Context, ticketID, orgID string) (*Ticket, error) {
	t := &Ticket{}
	err := db.Pool.QueryRow(ctx,
		`SELECT t.id, t.project_id, t.parent_id, t.type, t.title, t.description_markdown,
		        t.status, t.priority, t.date_start, t.date_end, t.agent_mode, t.agent_name,
		        t.assigned_to, t.created_by, t.archived_at, t.created_at, t.updated_at
		 FROM tickets t
		 JOIN projects p ON p.id = t.project_id
		 WHERE t.id = $1 AND p.org_id = $2`,
		ticketID, orgID,
	).Scan(&t.ID, &t.ProjectID, &t.ParentID, &t.Type, &t.Title, &t.DescriptionMarkdown,
		&t.Status, &t.Priority, &t.DateStart, &t.DateEnd, &t.AgentMode, &t.AgentName,
		&t.AssignedTo, &t.CreatedBy, &t.ArchivedAt, &t.CreatedAt, &t.UpdatedAt)
	if err != nil {
		return nil, fmt.Errorf("getting org-scoped ticket: %w", err)
	}
	return t, nil
}

// GetAttachmentByID fetches an attachment by ID for authorization checks.
func (db *DB) GetAttachmentByID(ctx context.Context, id string) (*TicketAttachment, error) {
	a := &TicketAttachment{}
	err := db.Pool.QueryRow(ctx,
		`SELECT id, ticket_id, file_name, file_path, content_type, size_bytes, uploaded_by, created_at
		 FROM ticket_attachments WHERE id = $1`, id,
	).Scan(&a.ID, &a.TicketID, &a.FileName, &a.FilePath, &a.ContentType, &a.SizeBytes, &a.UploadedBy, &a.CreatedAt)
	if err != nil {
		return nil, fmt.Errorf("getting attachment: %w", err)
	}
	return a, nil
}

// DeleteTicketTx wraps ticket deletion in a transaction.
// Descendants cascade automatically via the composite FK (#26).
func (db *DB) DeleteTicketTx(ctx context.Context, ticketID string) error {
	tx, err := db.Pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("beginning transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, `DELETE FROM tickets WHERE id = $1`, ticketID); err != nil {
		return fmt.Errorf("deleting ticket: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("committing delete ticket transaction: %w", err)
	}
	return nil
}

// UpdateTOTPLastUsed records when a TOTP code was last used (replay prevention).
func (db *DB) UpdateTOTPLastUsed(ctx context.Context, userID string, usedAt time.Time) error {
	_, err := db.Pool.Exec(ctx,
		`UPDATE users SET totp_last_used_at = $1 WHERE id = $2`, usedAt, userID)
	if err != nil {
		return fmt.Errorf("updating totp last used: %w", err)
	}
	return nil
}

// GetTOTPLastUsed returns the last time a TOTP code was used for a user.
func (db *DB) GetTOTPLastUsed(ctx context.Context, userID string) (*time.Time, error) {
	var lastUsed *time.Time
	err := db.Pool.QueryRow(ctx,
		`SELECT totp_last_used_at FROM users WHERE id = $1`, userID).Scan(&lastUsed)
	if err != nil {
		return nil, fmt.Errorf("getting totp last used: %w", err)
	}
	return lastUsed, nil
}

// ── Feature Debates ───────────────────────────────────────────────

// featureDebateColumns lists the column order used by every SELECT / RETURNING
// clause in the debate queries. Keeping it in one place prevents drift across
// the many scan targets below.
const featureDebateColumns = `id, ticket_id, project_id, org_id, started_by, status,
	seed_description, current_text, original_ticket_description,
	in_flight_request_id, in_flight_started_at, total_cost_micros,
	effort_score, effort_hours, effort_reasoning, effort_scored_at,
	last_scored_round_id, approved_text, created_at, updated_at`

// scanFeatureDebate reads one row from a pgx.Row or pgx.Rows into a *FeatureDebate.
type featureDebateScanner interface {
	Scan(dest ...any) error
}

func scanFeatureDebate(row featureDebateScanner, deb *FeatureDebate) error {
	return row.Scan(
		&deb.ID, &deb.TicketID, &deb.ProjectID, &deb.OrgID, &deb.StartedBy, &deb.Status,
		&deb.SeedDescription, &deb.CurrentText, &deb.OriginalTicketDescription,
		&deb.InFlightRequestID, &deb.InFlightStartedAt, &deb.TotalCostMicros,
		&deb.EffortScore, &deb.EffortHours, &deb.EffortReasoning, &deb.EffortScoredAt,
		&deb.LastScoredRoundID, &deb.ApprovedText, &deb.CreatedAt, &deb.UpdatedAt,
	)
}

// StartDebate creates an active debate for the given ticket, or returns the
// existing one if another request already created it. Idempotent under
// concurrent calls (ON CONFLICT DO NOTHING + fallback SELECT). Spec §4.1.
func (db *DB) StartDebate(ctx context.Context, ticketID, projectID, orgID, userID string) (*FeatureDebate, error) {
	var desc string
	if err := db.Pool.QueryRow(ctx,
		`SELECT description_markdown FROM tickets WHERE id = $1`, ticketID,
	).Scan(&desc); err != nil {
		return nil, fmt.Errorf("loading ticket description: %w", err)
	}

	deb := &FeatureDebate{}
	err := scanFeatureDebate(db.Pool.QueryRow(ctx,
		`INSERT INTO feature_debates (
			ticket_id, project_id, org_id, started_by, status,
			seed_description, current_text, original_ticket_description,
			total_cost_micros
		) VALUES ($1, $2, $3, $4, 'active', $5, $5, $5, 0)
		ON CONFLICT (ticket_id) WHERE status = 'active' DO NOTHING
		RETURNING `+featureDebateColumns,
		ticketID, projectID, orgID, userID, desc,
	), deb)
	if errors.Is(err, pgx.ErrNoRows) {
		// Concurrent INSERT lost the race; return the existing active row.
		return db.GetActiveDebate(ctx, ticketID)
	}
	if err != nil {
		return nil, fmt.Errorf("inserting feature_debate: %w", err)
	}
	return deb, nil
}

// GetActiveDebate returns the single active debate for a ticket, or pgx.ErrNoRows.
func (db *DB) GetActiveDebate(ctx context.Context, ticketID string) (*FeatureDebate, error) {
	deb := &FeatureDebate{}
	err := scanFeatureDebate(db.Pool.QueryRow(ctx,
		`SELECT `+featureDebateColumns+` FROM feature_debates
		  WHERE ticket_id = $1 AND status = 'active' LIMIT 1`,
		ticketID,
	), deb)
	if err != nil {
		return nil, err
	}
	return deb, nil
}

// GetDebateByID returns a debate by id regardless of status.
func (db *DB) GetDebateByID(ctx context.Context, debateID string) (*FeatureDebate, error) {
	deb := &FeatureDebate{}
	err := scanFeatureDebate(db.Pool.QueryRow(ctx,
		`SELECT `+featureDebateColumns+` FROM feature_debates WHERE id = $1`,
		debateID,
	), deb)
	if err != nil {
		return nil, err
	}
	return deb, nil
}

// IsDebateActive returns true if any debate is active for the given ticket.
// Used by UpdateTicketDescription guard (Task 9).
func (db *DB) IsDebateActive(ctx context.Context, ticketID string) (bool, error) {
	var exists bool
	err := db.Pool.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM feature_debates
		  WHERE ticket_id = $1 AND status = 'active')`,
		ticketID,
	).Scan(&exists)
	return exists, err
}

// GetDebateRounds returns rounds for a debate in round_number ASC order.
func (db *DB) GetDebateRounds(ctx context.Context, debateID string) ([]DebateRound, error) {
	rows, err := db.Pool.Query(ctx, `
		SELECT id, debate_id, round_number, provider, model, triggered_by,
		       feedback, input_text, output_text, diff_unified, status,
		       input_tokens, output_tokens, cost_micros, scorer_cost_micros,
		       created_at, decided_at
		  FROM feature_debate_rounds
		 WHERE debate_id = $1
		 ORDER BY round_number ASC`, debateID)
	if err != nil {
		return nil, fmt.Errorf("listing debate rounds: %w", err)
	}
	defer rows.Close()
	var out []DebateRound
	for rows.Next() {
		var r DebateRound
		if err := rows.Scan(
			&r.ID, &r.DebateID, &r.RoundNumber, &r.Provider, &r.Model, &r.TriggeredBy,
			&r.Feedback, &r.InputText, &r.OutputText, &r.DiffUnified, &r.Status,
			&r.InputTokens, &r.OutputTokens, &r.CostMicros, &r.ScorerCostMicros,
			&r.CreatedAt, &r.DecidedAt,
		); err != nil {
			return nil, fmt.Errorf("scanning debate round: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// CountUserRoundsLast24h returns the count of rounds triggered by the given
// user within the last 24 hours. Backs the per-user daily safety fuse (spec §6).
func (db *DB) CountUserRoundsLast24h(ctx context.Context, userID string) (int, error) {
	var n int
	err := db.Pool.QueryRow(ctx,
		`SELECT count(*) FROM feature_debate_rounds
		  WHERE triggered_by = $1 AND created_at >= now() - INTERVAL '24 hours'`,
		userID,
	).Scan(&n)
	return n, err
}

// CountActiveRoundsForDebate returns the count of in_review + accepted rounds
// for a debate. Rejected rounds don't count toward the per-feature cap.
func (db *DB) CountActiveRoundsForDebate(ctx context.Context, debateID string) (int, error) {
	var n int
	err := db.Pool.QueryRow(ctx,
		`SELECT count(*) FROM feature_debate_rounds
		  WHERE debate_id = $1 AND status IN ('in_review','accepted')`,
		debateID,
	).Scan(&n)
	return n, err
}
