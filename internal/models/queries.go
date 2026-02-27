package models

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type DB struct {
	Pool *pgxpool.Pool
}

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

func (db *DB) GetUserByEmail(ctx context.Context, email string) (*User, error) {
	u := &User{}
	err := db.Pool.QueryRow(ctx,
		`SELECT id, email, password_hash, name, role, totp_secret, totp_verified, recovery_codes, must_setup_2fa, preferred_2fa_method, created_at, updated_at
		 FROM users WHERE email = $1`, email,
	).Scan(&u.ID, &u.Email, &u.PasswordHash, &u.Name, &u.Role, &u.TOTPSecret, &u.TOTPVerified, &u.RecoveryCodes, &u.MustSetup2FA, &u.Preferred2FAMethod, &u.CreatedAt, &u.UpdatedAt)
	if err != nil {
		return nil, err
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
		return nil, err
	}
	return u, nil
}

func (db *DB) UpdateUserTOTP(ctx context.Context, userID string, totpSecret []byte, verified bool, method string) error {
	_, err := db.Pool.Exec(ctx,
		`UPDATE users SET totp_secret = $1, totp_verified = $2, preferred_2fa_method = $3, must_setup_2fa = FALSE, updated_at = now() WHERE id = $4`,
		totpSecret, verified, method, userID)
	return err
}

func (db *DB) UpdateUserRecoveryCodes(ctx context.Context, userID string, codes []byte) error {
	_, err := db.Pool.Exec(ctx,
		`UPDATE users SET recovery_codes = $1, updated_at = now() WHERE id = $2`, codes, userID)
	return err
}

func (db *DB) ClearUser2FA(ctx context.Context, userID string) error {
	_, err := db.Pool.Exec(ctx,
		`UPDATE users SET totp_secret = NULL, totp_verified = FALSE, recovery_codes = NULL, must_setup_2fa = TRUE, preferred_2fa_method = NULL, updated_at = now() WHERE id = $1`, userID)
	return err
}

func (db *DB) ConsumeRecoveryCode(ctx context.Context, userID string, codes []byte) error {
	_, err := db.Pool.Exec(ctx,
		`UPDATE users SET recovery_codes = $1, updated_at = now() WHERE id = $2`, codes, userID)
	return err
}

func (db *DB) UpdateUserPassword(ctx context.Context, userID, hash string) error {
	_, err := db.Pool.Exec(ctx,
		`UPDATE users SET password_hash = $1, updated_at = now() WHERE id = $2`, hash, userID)
	return err
}

// ── Organizations ─────────────────────────────────────────────────

func (db *DB) CreateOrg(ctx context.Context, name, slug string) (*Organization, error) {
	o := &Organization{}
	err := db.Pool.QueryRow(ctx,
		`INSERT INTO organizations (name, slug) VALUES ($1, $2) RETURNING id, name, slug, created_at, updated_at`,
		name, slug,
	).Scan(&o.ID, &o.Name, &o.Slug, &o.CreatedAt, &o.UpdatedAt)
	if err != nil {
		return nil, fmt.Errorf("creating org: %w", err)
	}
	return o, nil
}

func (db *DB) GetOrgBySlug(ctx context.Context, slug string) (*Organization, error) {
	o := &Organization{}
	err := db.Pool.QueryRow(ctx,
		`SELECT id, name, slug, created_at, updated_at FROM organizations WHERE slug = $1`, slug,
	).Scan(&o.ID, &o.Name, &o.Slug, &o.CreatedAt, &o.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return o, nil
}

func (db *DB) GetOrgByID(ctx context.Context, id string) (*Organization, error) {
	o := &Organization{}
	err := db.Pool.QueryRow(ctx,
		`SELECT id, name, slug, created_at, updated_at FROM organizations WHERE id = $1`, id,
	).Scan(&o.ID, &o.Name, &o.Slug, &o.CreatedAt, &o.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return o, nil
}

func (db *DB) ListUserOrgs(ctx context.Context, userID string) ([]Organization, error) {
	rows, err := db.Pool.Query(ctx,
		`SELECT o.id, o.name, o.slug, o.created_at, o.updated_at
		 FROM organizations o
		 JOIN org_memberships m ON m.org_id = o.id
		 WHERE m.user_id = $1
		 ORDER BY o.name`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var orgs []Organization
	for rows.Next() {
		var o Organization
		if err := rows.Scan(&o.ID, &o.Name, &o.Slug, &o.CreatedAt, &o.UpdatedAt); err != nil {
			return nil, err
		}
		orgs = append(orgs, o)
	}
	return orgs, rows.Err()
}

func (db *DB) ListAllOrgs(ctx context.Context) ([]Organization, error) {
	rows, err := db.Pool.Query(ctx,
		`SELECT id, name, slug, created_at, updated_at FROM organizations ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var orgs []Organization
	for rows.Next() {
		var o Organization
		if err := rows.Scan(&o.ID, &o.Name, &o.Slug, &o.CreatedAt, &o.UpdatedAt); err != nil {
			return nil, err
		}
		orgs = append(orgs, o)
	}
	return orgs, rows.Err()
}

func (db *DB) AddOrgMember(ctx context.Context, userID, orgID, role string) error {
	_, err := db.Pool.Exec(ctx,
		`INSERT INTO org_memberships (user_id, org_id, role) VALUES ($1, $2, $3)
		 ON CONFLICT (user_id, org_id) DO UPDATE SET role = $3`,
		userID, orgID, role)
	return err
}

func (db *DB) GetOrgMembership(ctx context.Context, userID, orgID string) (*OrgMembership, error) {
	m := &OrgMembership{}
	err := db.Pool.QueryRow(ctx,
		`SELECT id, user_id, org_id, role, created_at FROM org_memberships WHERE user_id = $1 AND org_id = $2`,
		userID, orgID,
	).Scan(&m.ID, &m.UserID, &m.OrgID, &m.Role, &m.CreatedAt)
	if err != nil {
		return nil, err
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
		return nil, err
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
			return nil, err
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
	return err
}

func (db *DB) UpdateOrgMemberRole(ctx context.Context, userID, orgID, role string) error {
	_, err := db.Pool.Exec(ctx,
		`UPDATE org_memberships SET role = $1 WHERE user_id = $2 AND org_id = $3`,
		role, userID, orgID)
	return err
}

func (db *DB) CountOrgOwners(ctx context.Context, orgID string) (int, error) {
	var count int
	err := db.Pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM org_memberships WHERE org_id = $1 AND role = 'owner'`, orgID).Scan(&count)
	return count, err
}

// ── Projects ──────────────────────────────────────────────────────

func (db *DB) CreateProject(ctx context.Context, orgID, name, slug string) (*Project, error) {
	p := &Project{}
	err := db.Pool.QueryRow(ctx,
		`INSERT INTO projects (org_id, name, slug) VALUES ($1, $2, $3)
		 RETURNING id, org_id, name, slug, brief_markdown, created_at, updated_at`,
		orgID, name, slug,
	).Scan(&p.ID, &p.OrgID, &p.Name, &p.Slug, &p.BriefMarkdown, &p.CreatedAt, &p.UpdatedAt)
	if err != nil {
		return nil, fmt.Errorf("creating project: %w", err)
	}
	return p, nil
}

func (db *DB) GetProject(ctx context.Context, orgID, slug string) (*Project, error) {
	p := &Project{}
	err := db.Pool.QueryRow(ctx,
		`SELECT id, org_id, name, slug, brief_markdown, created_at, updated_at
		 FROM projects WHERE org_id = $1 AND slug = $2`, orgID, slug,
	).Scan(&p.ID, &p.OrgID, &p.Name, &p.Slug, &p.BriefMarkdown, &p.CreatedAt, &p.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return p, nil
}

func (db *DB) GetProjectByID(ctx context.Context, id string) (*Project, error) {
	p := &Project{}
	err := db.Pool.QueryRow(ctx,
		`SELECT id, org_id, name, slug, brief_markdown, created_at, updated_at
		 FROM projects WHERE id = $1`, id,
	).Scan(&p.ID, &p.OrgID, &p.Name, &p.Slug, &p.BriefMarkdown, &p.CreatedAt, &p.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return p, nil
}

func (db *DB) ListProjects(ctx context.Context, orgID string) ([]Project, error) {
	rows, err := db.Pool.Query(ctx,
		`SELECT id, org_id, name, slug, brief_markdown, created_at, updated_at
		 FROM projects WHERE org_id = $1 ORDER BY name`, orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var projects []Project
	for rows.Next() {
		var p Project
		if err := rows.Scan(&p.ID, &p.OrgID, &p.Name, &p.Slug, &p.BriefMarkdown, &p.CreatedAt, &p.UpdatedAt); err != nil {
			return nil, err
		}
		projects = append(projects, p)
	}
	return projects, rows.Err()
}

func (db *DB) UpdateProjectBrief(ctx context.Context, projectID, brief string) error {
	_, err := db.Pool.Exec(ctx,
		`UPDATE projects SET brief_markdown = $1, updated_at = now() WHERE id = $2`, brief, projectID)
	return err
}

// ── Tickets ───────────────────────────────────────────────────────

func (db *DB) CreateTicket(ctx context.Context, t *Ticket) error {
	return db.Pool.QueryRow(ctx,
		`INSERT INTO tickets (project_id, parent_id, type, title, description_markdown, status, priority, date_start, date_end, agent_mode, agent_name, assigned_to, created_by)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
		 RETURNING id, created_at, updated_at`,
		t.ProjectID, t.ParentID, t.Type, t.Title, t.DescriptionMarkdown, t.Status, t.Priority, t.DateStart, t.DateEnd, t.AgentMode, t.AgentName, t.AssignedTo, t.CreatedBy,
	).Scan(&t.ID, &t.CreatedAt, &t.UpdatedAt)
}

func (db *DB) GetTicket(ctx context.Context, id string) (*Ticket, error) {
	t := &Ticket{}
	err := db.Pool.QueryRow(ctx,
		`SELECT id, project_id, parent_id, type, title, description_markdown, status, priority, date_start, date_end, agent_mode, agent_name, assigned_to, created_by, created_at, updated_at
		 FROM tickets WHERE id = $1`, id,
	).Scan(&t.ID, &t.ProjectID, &t.ParentID, &t.Type, &t.Title, &t.DescriptionMarkdown, &t.Status, &t.Priority, &t.DateStart, &t.DateEnd, &t.AgentMode, &t.AgentName, &t.AssignedTo, &t.CreatedBy, &t.CreatedAt, &t.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return t, nil
}

func (db *DB) ListTickets(ctx context.Context, projectID, ticketType string) ([]Ticket, error) {
	query := `SELECT id, project_id, parent_id, type, title, description_markdown, status, priority, date_start, date_end, agent_mode, agent_name, assigned_to, created_by, created_at, updated_at
		 FROM tickets WHERE project_id = $1`
	args := []any{projectID}

	if ticketType != "" {
		query += " AND type = $2"
		args = append(args, ticketType)
	}
	query += " ORDER BY created_at DESC"

	rows, err := db.Pool.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanTickets(rows)
}

func (db *DB) ListTicketsByParent(ctx context.Context, parentID string) ([]Ticket, error) {
	rows, err := db.Pool.Query(ctx,
		`SELECT id, project_id, parent_id, type, title, description_markdown, status, priority, date_start, date_end, agent_mode, agent_name, assigned_to, created_by, created_at, updated_at
		 FROM tickets WHERE parent_id = $1 ORDER BY created_at`, parentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanTickets(rows)
}

func (db *DB) ListEpics(ctx context.Context, projectID string) ([]Ticket, error) {
	return db.ListTickets(ctx, projectID, "epic")
}

func (db *DB) ListBugs(ctx context.Context, projectID string) ([]Ticket, error) {
	return db.ListTickets(ctx, projectID, "bug")
}

func (db *DB) ListGanttTickets(ctx context.Context, projectID string) ([]Ticket, error) {
	rows, err := db.Pool.Query(ctx,
		`SELECT id, project_id, parent_id, type, title, description_markdown, status, priority, date_start, date_end, agent_mode, agent_name, assigned_to, created_by, created_at, updated_at
		 FROM tickets WHERE project_id = $1 AND date_start IS NOT NULL AND date_end IS NOT NULL
		 ORDER BY date_start`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanTickets(rows)
}

func (db *DB) UpdateTicketStatus(ctx context.Context, id, status string) error {
	_, err := db.Pool.Exec(ctx,
		`UPDATE tickets SET status = $1, updated_at = now() WHERE id = $2`, status, id)
	return err
}

func (db *DB) UpdateTicketAgentMode(ctx context.Context, id string, mode, agent *string) error {
	_, err := db.Pool.Exec(ctx,
		`UPDATE tickets SET agent_mode = $1, agent_name = $2, updated_at = now() WHERE id = $3`, mode, agent, id)
	return err
}

func (db *DB) UpdateTicket(ctx context.Context, t *Ticket) error {
	_, err := db.Pool.Exec(ctx,
		`UPDATE tickets SET title = $1, description_markdown = $2, priority = $3, date_start = $4, date_end = $5, assigned_to = $6, updated_at = now() WHERE id = $7`,
		t.Title, t.DescriptionMarkdown, t.Priority, t.DateStart, t.DateEnd, t.AssignedTo, t.ID)
	return err
}

// ListAgentReady returns tickets that are ready for agent processing.
func (db *DB) ListAgentReady(ctx context.Context) ([]Ticket, error) {
	rows, err := db.Pool.Query(ctx,
		`SELECT id, project_id, parent_id, type, title, description_markdown, status, priority, date_start, date_end, agent_mode, agent_name, assigned_to, created_by, created_at, updated_at
		 FROM tickets WHERE agent_mode IS NOT NULL AND status IN ('ready', 'plan_review')
		 ORDER BY priority DESC, created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanTickets(rows)
}

func scanTickets(rows pgx.Rows) ([]Ticket, error) {
	var tickets []Ticket
	for rows.Next() {
		var t Ticket
		if err := rows.Scan(&t.ID, &t.ProjectID, &t.ParentID, &t.Type, &t.Title, &t.DescriptionMarkdown, &t.Status, &t.Priority, &t.DateStart, &t.DateEnd, &t.AgentMode, &t.AgentName, &t.AssignedTo, &t.CreatedBy, &t.CreatedAt, &t.UpdatedAt); err != nil {
			return nil, err
		}
		tickets = append(tickets, t)
	}
	return tickets, rows.Err()
}

// ── Comments ──────────────────────────────────────────────────────

func (db *DB) CreateComment(ctx context.Context, ticketID string, userID *string, agentName *string, body string) (*Comment, error) {
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
		return nil, err
	}
	defer rows.Close()

	var comments []Comment
	for rows.Next() {
		var c Comment
		if err := rows.Scan(&c.ID, &c.TicketID, &c.UserID, &c.AgentName, &c.BodyMarkdown, &c.CreatedAt); err != nil {
			return nil, err
		}
		comments = append(comments, c)
	}
	return comments, rows.Err()
}

// ── Activities ────────────────────────────────────────────────────

func (db *DB) CreateActivity(ctx context.Context, ticketID string, userID *string, agentName *string, action, detailsJSON string) error {
	_, err := db.Pool.Exec(ctx,
		`INSERT INTO ticket_activities (ticket_id, user_id, agent_name, action, details_json) VALUES ($1, $2, $3, $4, $5)`,
		ticketID, userID, agentName, action, detailsJSON)
	return err
}

// ── WebAuthn Credentials ──────────────────────────────────────────

func (db *DB) CreateWebAuthnCredential(ctx context.Context, cred *WebAuthnCredential) error {
	_, err := db.Pool.Exec(ctx,
		`INSERT INTO webauthn_credentials (user_id, credential_id, public_key, attestation_type, authenticator_aaguid, sign_count, name)
		 VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		cred.UserID, cred.CredentialID, cred.PublicKey, cred.AttestationType, cred.AuthenticatorAAGUID, cred.SignCount, cred.Name)
	return err
}

func (db *DB) ListWebAuthnCredentials(ctx context.Context, userID string) ([]WebAuthnCredential, error) {
	rows, err := db.Pool.Query(ctx,
		`SELECT id, user_id, credential_id, public_key, attestation_type, authenticator_aaguid, sign_count, name, last_used_at, created_at
		 FROM webauthn_credentials WHERE user_id = $1 ORDER BY created_at`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var creds []WebAuthnCredential
	for rows.Next() {
		var c WebAuthnCredential
		if err := rows.Scan(&c.ID, &c.UserID, &c.CredentialID, &c.PublicKey, &c.AttestationType, &c.AuthenticatorAAGUID, &c.SignCount, &c.Name, &c.LastUsedAt, &c.CreatedAt); err != nil {
			return nil, err
		}
		creds = append(creds, c)
	}
	return creds, rows.Err()
}

func (db *DB) UpdateWebAuthnSignCount(ctx context.Context, credID string, signCount uint32) error {
	_, err := db.Pool.Exec(ctx,
		`UPDATE webauthn_credentials SET sign_count = $1, last_used_at = $2 WHERE id = $3`,
		signCount, time.Now(), credID)
	return err
}

func (db *DB) DeleteWebAuthnCredential(ctx context.Context, credID string) error {
	_, err := db.Pool.Exec(ctx, `DELETE FROM webauthn_credentials WHERE id = $1`, credID)
	return err
}

func (db *DB) DeleteAllWebAuthnCredentials(ctx context.Context, userID string) error {
	_, err := db.Pool.Exec(ctx, `DELETE FROM webauthn_credentials WHERE user_id = $1`, userID)
	return err
}

func (db *DB) CountUserWebAuthnCredentials(ctx context.Context, userID string) (int, error) {
	var count int
	err := db.Pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM webauthn_credentials WHERE user_id = $1`, userID).Scan(&count)
	return count, err
}

// ── Users listing ──────────────────────────────────────────────────

func (db *DB) ListUsers(ctx context.Context) ([]User, error) {
	rows, err := db.Pool.Query(ctx,
		`SELECT id, email, '', name, role, NULL, totp_verified, NULL, must_setup_2fa, preferred_2fa_method, created_at, updated_at
		 FROM users ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var users []User
	for rows.Next() {
		var u User
		if err := rows.Scan(&u.ID, &u.Email, &u.PasswordHash, &u.Name, &u.Role, &u.TOTPSecret, &u.TOTPVerified, &u.RecoveryCodes, &u.MustSetup2FA, &u.Preferred2FAMethod, &u.CreatedAt, &u.UpdatedAt); err != nil {
			return nil, err
		}
		users = append(users, u)
	}
	return users, rows.Err()
}
