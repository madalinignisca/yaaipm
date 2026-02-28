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

func (db *DB) UpdateUserEmail(ctx context.Context, userID, email string) error {
	_, err := db.Pool.Exec(ctx,
		`UPDATE users SET email = $1, updated_at = now() WHERE id = $2`, email, userID)
	return err
}

func (db *DB) CountUsers(ctx context.Context) (int, error) {
	var count int
	err := db.Pool.QueryRow(ctx, `SELECT COUNT(*) FROM users`).Scan(&count)
	return count, err
}

// ── Invitations ──────────────────────────────────────────────────

func (db *DB) CreateInvitation(ctx context.Context, email, orgID, orgRole, tokenHash, invitedBy string, expiresAt time.Time) (*Invitation, error) {
	inv := &Invitation{}
	err := db.Pool.QueryRow(ctx,
		`INSERT INTO invitations (email, org_id, org_role, token_hash, invited_by, expires_at)
		 VALUES ($1, $2, $3, $4, $5, $6)
		 RETURNING id, email, org_id, org_role, token_hash, status, invited_by, expires_at, created_at, updated_at`,
		email, orgID, orgRole, tokenHash, invitedBy, expiresAt,
	).Scan(&inv.ID, &inv.Email, &inv.OrgID, &inv.OrgRole, &inv.TokenHash, &inv.Status, &inv.InvitedBy, &inv.ExpiresAt, &inv.CreatedAt, &inv.UpdatedAt)
	if err != nil {
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
		return nil, err
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
		return nil, err
	}
	return inv, nil
}

func (db *DB) ListPendingInvitationsForUser(ctx context.Context, email string) ([]InvitationWithOrg, error) {
	rows, err := db.Pool.Query(ctx,
		`SELECT i.id, i.email, i.org_id, i.org_role, i.token_hash, i.status, i.invited_by, i.expires_at, i.created_at, i.updated_at,
		        o.id, o.name, o.slug, o.created_at, o.updated_at,
		        u.name
		 FROM invitations i
		 JOIN organizations o ON o.id = i.org_id
		 JOIN users u ON u.id = i.invited_by
		 WHERE i.email = $1 AND i.status = 'pending' AND i.expires_at > now()
		 ORDER BY i.created_at DESC`, email)
	if err != nil {
		return nil, err
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
			&r.Organization.CreatedAt, &r.Organization.UpdatedAt,
			&r.InviterName,
		); err != nil {
			return nil, err
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
		return nil, err
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
			return nil, err
		}
		results = append(results, r)
	}
	return results, rows.Err()
}

func (db *DB) UpdateInvitationStatus(ctx context.Context, id, status string) error {
	_, err := db.Pool.Exec(ctx,
		`UPDATE invitations SET status = $1, updated_at = now() WHERE id = $2`,
		status, id)
	return err
}

func (db *DB) ExpireOldInvitations(ctx context.Context) error {
	_, err := db.Pool.Exec(ctx,
		`UPDATE invitations SET status = 'expired', updated_at = now()
		 WHERE status = 'pending' AND expires_at <= now()`)
	return err
}

func (db *DB) HasPendingInvitation(ctx context.Context, email, orgID string) (bool, error) {
	var exists bool
	err := db.Pool.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM invitations WHERE email = $1 AND org_id = $2 AND status = 'pending' AND expires_at > now())`,
		email, orgID).Scan(&exists)
	return exists, err
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
		return nil, err
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
		return nil, err
	}
	return c, nil
}

func (db *DB) UpdateAIConversationTitle(ctx context.Context, id, title string) error {
	_, err := db.Pool.Exec(ctx,
		`UPDATE ai_conversations SET title = $1, updated_at = now() WHERE id = $2`, title, id)
	return err
}

func (db *DB) TouchAIConversation(ctx context.Context, id string) error {
	_, err := db.Pool.Exec(ctx,
		`UPDATE ai_conversations SET updated_at = now() WHERE id = $1`, id)
	return err
}

func (db *DB) ListAIConversations(ctx context.Context, userID string, limit int) ([]AIConversation, error) {
	rows, err := db.Pool.Query(ctx,
		`SELECT id, user_id, project_id, title, created_at, updated_at
		 FROM ai_conversations WHERE user_id = $1 ORDER BY updated_at DESC LIMIT $2`,
		userID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var convs []AIConversation
	for rows.Next() {
		var c AIConversation
		if err := rows.Scan(&c.ID, &c.UserID, &c.ProjectID, &c.Title, &c.CreatedAt, &c.UpdatedAt); err != nil {
			return nil, err
		}
		convs = append(convs, c)
	}
	return convs, rows.Err()
}

func (db *DB) CreateAIMessage(ctx context.Context, conversationID, role, content string) (*AIMessage, error) {
	m := &AIMessage{}
	err := db.Pool.QueryRow(ctx,
		`INSERT INTO ai_messages (conversation_id, role, content) VALUES ($1, $2, $3)
		 RETURNING id, conversation_id, role, content, created_at`,
		conversationID, role, content,
	).Scan(&m.ID, &m.ConversationID, &m.Role, &m.Content, &m.CreatedAt)
	if err != nil {
		return nil, fmt.Errorf("creating ai message: %w", err)
	}
	return m, nil
}

func (db *DB) ListAIMessages(ctx context.Context, conversationID string) ([]AIMessage, error) {
	rows, err := db.Pool.Query(ctx,
		`SELECT id, conversation_id, role, content, created_at
		 FROM ai_messages WHERE conversation_id = $1 ORDER BY created_at`,
		conversationID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var msgs []AIMessage
	for rows.Next() {
		var m AIMessage
		if err := rows.Scan(&m.ID, &m.ConversationID, &m.Role, &m.Content, &m.CreatedAt); err != nil {
			return nil, err
		}
		msgs = append(msgs, m)
	}
	return msgs, rows.Err()
}

func (db *DB) DeleteAIConversation(ctx context.Context, id string) error {
	_, err := db.Pool.Exec(ctx, `DELETE FROM ai_conversations WHERE id = $1`, id)
	return err
}

// SearchTickets searches tickets by title matching a query within a project.
func (db *DB) SearchTickets(ctx context.Context, projectID, query string, ticketType, status *string) ([]Ticket, error) {
	sql := `SELECT id, project_id, parent_id, type, title, description_markdown, status, priority, date_start, date_end, agent_mode, agent_name, assigned_to, created_by, created_at, updated_at
		 FROM tickets WHERE project_id = $1 AND (title ILIKE '%' || $2 || '%' OR description_markdown ILIKE '%' || $2 || '%')`
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
		n++
	}
	sql += " ORDER BY created_at DESC LIMIT 20"

	rows, err := db.Pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
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
		return nil, err
	}
	return c, nil
}

func (db *DB) UpdateProjectCost(ctx context.Context, id string, amountCents int64) error {
	_, err := db.Pool.Exec(ctx,
		`UPDATE project_costs SET amount_cents = $1, updated_at = now() WHERE id = $2`,
		amountCents, id)
	return err
}

func (db *DB) DeleteProjectCost(ctx context.Context, id string) error {
	_, err := db.Pool.Exec(ctx, `DELETE FROM project_costs WHERE id = $1`, id)
	return err
}

func (db *DB) ListProjectCosts(ctx context.Context, projectID, month string) ([]ProjectCost, error) {
	rows, err := db.Pool.Query(ctx,
		`SELECT id, project_id, month, category, name, amount_cents, created_at, updated_at
		 FROM project_costs WHERE project_id = $1 AND month = $2
		 ORDER BY category, name`, projectID, month)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var costs []ProjectCost
	for rows.Next() {
		var c ProjectCost
		if err := rows.Scan(&c.ID, &c.ProjectID, &c.Month, &c.Category, &c.Name, &c.AmountCents, &c.CreatedAt, &c.UpdatedAt); err != nil {
			return nil, err
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
		return nil, err
	}
	defer rows.Close()

	var costs []ProjectCost
	for rows.Next() {
		var c ProjectCost
		if err := rows.Scan(&c.ID, &c.ProjectID, &c.Month, &c.Category, &c.Name, &c.AmountCents, &c.CreatedAt, &c.UpdatedAt); err != nil {
			return nil, err
		}
		costs = append(costs, c)
	}
	return costs, rows.Err()
}

// ── AI Usage ──────────────────────────────────────────────────

func (db *DB) CreateAIUsageEntry(ctx context.Context, orgID string, projectID *string, userID, model, label string, inputTokens, outputTokens int, costCents int64) error {
	_, err := db.Pool.Exec(ctx,
		`INSERT INTO ai_usage_entries (org_id, project_id, user_id, model, label, input_tokens, output_tokens, cost_cents)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		orgID, projectID, userID, model, label, inputTokens, outputTokens, costCents)
	return err
}

func (db *DB) ListAIUsageByProjectMonth(ctx context.Context, projectID, month string) ([]AIUsageSummary, error) {
	rows, err := db.Pool.Query(ctx,
		`SELECT model, label, SUM(input_tokens)::BIGINT, SUM(output_tokens)::BIGINT, SUM(cost_cents)::BIGINT, COUNT(*)::INT
		 FROM ai_usage_entries
		 WHERE project_id = $1 AND to_char(created_at, 'YYYY-MM') = $2
		 GROUP BY model, label ORDER BY model`, projectID, month)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var summaries []AIUsageSummary
	for rows.Next() {
		var s AIUsageSummary
		if err := rows.Scan(&s.Model, &s.Label, &s.InputTokens, &s.OutputTokens, &s.TotalCents, &s.EntryCount); err != nil {
			return nil, err
		}
		summaries = append(summaries, s)
	}
	return summaries, rows.Err()
}

func (db *DB) ListAIUsageByOrgMonth(ctx context.Context, orgID, month string) ([]AIUsageSummary, error) {
	rows, err := db.Pool.Query(ctx,
		`SELECT model, label, SUM(input_tokens)::BIGINT, SUM(output_tokens)::BIGINT, SUM(cost_cents)::BIGINT, COUNT(*)::INT
		 FROM ai_usage_entries
		 WHERE org_id = $1 AND to_char(created_at, 'YYYY-MM') = $2
		 GROUP BY model, label ORDER BY model`, orgID, month)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var summaries []AIUsageSummary
	for rows.Next() {
		var s AIUsageSummary
		if err := rows.Scan(&s.Model, &s.Label, &s.InputTokens, &s.OutputTokens, &s.TotalCents, &s.EntryCount); err != nil {
			return nil, err
		}
		summaries = append(summaries, s)
	}
	return summaries, rows.Err()
}

func (db *DB) GetTotalAIUsageCentsForOrgMonth(ctx context.Context, orgID, month string) (int64, error) {
	var total int64
	err := db.Pool.QueryRow(ctx,
		`SELECT COALESCE(SUM(cost_cents), 0)::BIGINT FROM ai_usage_entries
		 WHERE org_id = $1 AND to_char(created_at, 'YYYY-MM') = $2`, orgID, month).Scan(&total)
	return total, err
}

// ── AI Model Pricing ──────────────────────────────────────────

func (db *DB) GetModelPricing(ctx context.Context, modelName string) (*AIModelPricing, error) {
	p := &AIModelPricing{}
	err := db.Pool.QueryRow(ctx,
		`SELECT id, model_name, input_price_per_million_cents, output_price_per_million_cents, effective_from, created_at
		 FROM ai_model_pricing WHERE model_name = $1`, modelName,
	).Scan(&p.ID, &p.ModelName, &p.InputPricePerMillionCents, &p.OutputPricePerMillionCents, &p.EffectiveFrom, &p.CreatedAt)
	if err != nil {
		return nil, err
	}
	return p, nil
}

func (db *DB) ListModelPricing(ctx context.Context) ([]AIModelPricing, error) {
	rows, err := db.Pool.Query(ctx,
		`SELECT id, model_name, input_price_per_million_cents, output_price_per_million_cents, effective_from, created_at
		 FROM ai_model_pricing ORDER BY model_name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var pricing []AIModelPricing
	for rows.Next() {
		var p AIModelPricing
		if err := rows.Scan(&p.ID, &p.ModelName, &p.InputPricePerMillionCents, &p.OutputPricePerMillionCents, &p.EffectiveFrom, &p.CreatedAt); err != nil {
			return nil, err
		}
		pricing = append(pricing, p)
	}
	return pricing, rows.Err()
}

func (db *DB) UpsertModelPricing(ctx context.Context, modelName string, inputPrice, outputPrice int64) error {
	_, err := db.Pool.Exec(ctx,
		`INSERT INTO ai_model_pricing (model_name, input_price_per_million_cents, output_price_per_million_cents)
		 VALUES ($1, $2, $3)
		 ON CONFLICT (model_name) DO UPDATE SET
		   input_price_per_million_cents = $2,
		   output_price_per_million_cents = $3,
		   effective_from = now()`,
		modelName, inputPrice, outputPrice)
	return err
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
		return nil, err
	}
	defer rows.Close()

	var months []string
	for rows.Next() {
		var m string
		if err := rows.Scan(&m); err != nil {
			return nil, err
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
