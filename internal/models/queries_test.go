package models

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/madalin/forgedesk/internal/testutil"
)

func TestUserCRUD(t *testing.T) {
	pool := testutil.SetupTestDB(t)
	db := NewDB(pool)
	ctx := context.Background()

	// Create
	user, err := db.CreateUser(ctx, "test@example.com", "$argon2id$v=19$m=65536,t=3,p=4$dGVzdHNhbHQ$dGVzdGhhc2g", "Test User", "client")
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	if user.ID == "" {
		t.Fatal("user ID should not be empty")
	}
	if user.Email != "test@example.com" {
		t.Errorf("Email = %q, want test@example.com", user.Email)
	}
	if user.Role != "client" {
		t.Errorf("Role = %q, want client", user.Role)
	}
	if !user.MustSetup2FA {
		t.Error("MustSetup2FA should default to true")
	}

	// Get by email
	found, err := db.GetUserByEmail(ctx, "test@example.com")
	if err != nil {
		t.Fatalf("GetUserByEmail: %v", err)
	}
	if found.ID != user.ID {
		t.Errorf("ID mismatch: %s vs %s", found.ID, user.ID)
	}

	// Get by ID
	found2, err := db.GetUserByID(ctx, user.ID)
	if err != nil {
		t.Fatalf("GetUserByID: %v", err)
	}
	if found2.Email != user.Email {
		t.Error("email mismatch")
	}

	// Update password
	err = db.UpdateUserPassword(ctx, user.ID, "new-hash")
	if err != nil {
		t.Fatalf("UpdateUserPassword: %v", err)
	}
	updated, _ := db.GetUserByID(ctx, user.ID)
	if updated.PasswordHash != "new-hash" {
		t.Errorf("password not updated")
	}

	// List users
	users, err := db.ListUsers(ctx)
	if err != nil {
		t.Fatalf("ListUsers: %v", err)
	}
	if len(users) != 1 {
		t.Fatalf("expected 1 user, got %d", len(users))
	}
}

func TestUserDuplicateEmail(t *testing.T) {
	pool := testutil.SetupTestDB(t)
	db := NewDB(pool)
	ctx := context.Background()

	_, err := db.CreateUser(ctx, "dupe@test.com", "hash", "User1", "client")
	if err != nil {
		t.Fatal(err)
	}

	_, err = db.CreateUser(ctx, "dupe@test.com", "hash", "User2", "client")
	if err == nil {
		t.Fatal("expected error for duplicate email")
	}
}

func TestOrgCRUD(t *testing.T) {
	pool := testutil.SetupTestDB(t)
	db := NewDB(pool)
	ctx := context.Background()

	org, err := db.CreateOrg(ctx, "Test Org", "test-org")
	if err != nil {
		t.Fatalf("CreateOrg: %v", err)
	}
	if org.Slug != "test-org" {
		t.Errorf("Slug = %q, want test-org", org.Slug)
	}

	// Get by slug
	found, err := db.GetOrgBySlug(ctx, "test-org")
	if err != nil {
		t.Fatalf("GetOrgBySlug: %v", err)
	}
	if found.Name != "Test Org" {
		t.Errorf("Name mismatch")
	}

	// Get by ID
	found2, err := db.GetOrgByID(ctx, org.ID)
	if err != nil {
		t.Fatalf("GetOrgByID: %v", err)
	}
	if found2.Slug != "test-org" {
		t.Error("slug mismatch")
	}

	// List all orgs
	orgs, err := db.ListAllOrgs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(orgs) != 1 {
		t.Fatalf("expected 1 org, got %d", len(orgs))
	}
}

func TestOrgMembership(t *testing.T) {
	pool := testutil.SetupTestDB(t)
	db := NewDB(pool)
	ctx := context.Background()

	user, _ := db.CreateUser(ctx, "member@test.com", "hash", "Member", "client")
	org, _ := db.CreateOrg(ctx, "Membership Org", "membership-org")

	// Add member
	if err := db.AddOrgMember(ctx, user.ID, org.ID, "owner"); err != nil {
		t.Fatalf("AddOrgMember: %v", err)
	}

	// Get membership
	mem, err := db.GetOrgMembership(ctx, user.ID, org.ID)
	if err != nil {
		t.Fatalf("GetOrgMembership: %v", err)
	}
	if mem.Role != "owner" {
		t.Errorf("Role = %q, want owner", mem.Role)
	}

	// List user orgs
	orgs, err := db.ListUserOrgs(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(orgs) != 1 {
		t.Fatalf("expected 1 org, got %d", len(orgs))
	}

	// Upsert membership (change role)
	err = db.AddOrgMember(ctx, user.ID, org.ID, "admin")
	if err != nil {
		t.Fatal(err)
	}
	mem2, _ := db.GetOrgMembership(ctx, user.ID, org.ID)
	if mem2.Role != "admin" {
		t.Errorf("role not updated: %q", mem2.Role)
	}

	// List org members
	members, err := db.ListOrgMembers(ctx, org.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(members) != 1 {
		t.Fatalf("expected 1 member, got %d", len(members))
	}
}

func TestProjectCRUD(t *testing.T) {
	pool := testutil.SetupTestDB(t)
	db := NewDB(pool)
	ctx := context.Background()

	org, _ := db.CreateOrg(ctx, "Proj Org", "proj-org")

	proj, err := db.CreateProject(ctx, org.ID, "My Project", "my-project")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	if proj.Slug != "my-project" {
		t.Error("slug mismatch")
	}

	// Get project
	found, err := db.GetProject(ctx, org.ID, "my-project")
	if err != nil {
		t.Fatalf("GetProject: %v", err)
	}
	if found.Name != "My Project" {
		t.Error("name mismatch")
	}

	// Get by ID
	found2, err := db.GetProjectByID(ctx, proj.ID)
	if err != nil {
		t.Fatal(err)
	}
	if found2.OrgID != org.ID {
		t.Error("org ID mismatch")
	}

	// Update brief
	err = db.UpdateProjectBrief(ctx, proj.ID, "# Brief\nThis is the brief.")
	if err != nil {
		t.Fatal(err)
	}
	updated, _ := db.GetProjectByID(ctx, proj.ID)
	if updated.BriefMarkdown != "# Brief\nThis is the brief." {
		t.Errorf("brief not updated: %q", updated.BriefMarkdown)
	}

	// List projects
	projects, err := db.ListProjects(ctx, org.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(projects) != 1 {
		t.Fatalf("expected 1 project, got %d", len(projects))
	}
}

func TestTicketCRUD(t *testing.T) {
	pool := testutil.SetupTestDB(t)
	db := NewDB(pool)
	ctx := context.Background()

	org, _ := db.CreateOrg(ctx, "Ticket Org", "ticket-org")
	proj, _ := db.CreateProject(ctx, org.ID, "Ticket Project", "ticket-proj")
	user, _ := db.CreateUser(ctx, "ticketuser@test.com", "hash", "Ticket User", "client")

	// Create ticket
	ticket := &Ticket{
		ProjectID:           proj.ID,
		Type:                "task",
		Title:               "Test Ticket",
		DescriptionMarkdown: "Some description",
		Status:              "backlog",
		Priority:            "medium",
		CreatedBy:           user.ID,
	}
	if err := db.CreateTicket(ctx, ticket); err != nil {
		t.Fatalf("CreateTicket: %v", err)
	}
	if ticket.ID == "" {
		t.Fatal("ticket ID should be set")
	}

	// Get ticket
	found, err := db.GetTicket(ctx, ticket.ID)
	if err != nil {
		t.Fatalf("GetTicket: %v", err)
	}
	if found.Title != "Test Ticket" {
		t.Error("title mismatch")
	}
	if found.Status != "backlog" {
		t.Error("status mismatch")
	}

	// Update status
	err = db.UpdateTicketStatus(ctx, ticket.ID, "ready")
	if err != nil {
		t.Fatal(err)
	}
	found2, _ := db.GetTicket(ctx, ticket.ID)
	if found2.Status != "ready" {
		t.Errorf("status not updated: %q", found2.Status)
	}

	// Update agent mode
	mode := "plan"
	agent := "claude"
	err = db.UpdateTicketAgentMode(ctx, ticket.ID, &mode, &agent)
	if err != nil {
		t.Fatal(err)
	}
	found3, _ := db.GetTicket(ctx, ticket.ID)
	if found3.AgentMode == nil || *found3.AgentMode != "plan" {
		t.Error("agent mode not set")
	}

	// List tickets
	tickets, err := db.ListTickets(ctx, proj.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(tickets) != 1 {
		t.Fatalf("expected 1 ticket, got %d", len(tickets))
	}

	// List by type
	tasks, _ := db.ListTickets(ctx, proj.ID, "task")
	if len(tasks) != 1 {
		t.Error("expected 1 task")
	}
	features, _ := db.ListFeatures(ctx, proj.ID)
	if len(features) != 0 {
		t.Error("expected 0 features")
	}
}

func TestTicketParentChild(t *testing.T) {
	pool := testutil.SetupTestDB(t)
	db := NewDB(pool)
	ctx := context.Background()

	org, _ := db.CreateOrg(ctx, "PC Org", "pc-org")
	proj, _ := db.CreateProject(ctx, org.ID, "PC Project", "pc-proj")
	user, _ := db.CreateUser(ctx, "pc@test.com", "hash", "PC", "client")

	parent := &Ticket{ProjectID: proj.ID, Type: "feature", Title: "Feature", Status: "backlog", Priority: "high", CreatedBy: user.ID}
	db.CreateTicket(ctx, parent)

	child := &Ticket{ProjectID: proj.ID, ParentID: &parent.ID, Type: "task", Title: "Child Task", Status: "backlog", Priority: "medium", CreatedBy: user.ID}
	db.CreateTicket(ctx, child)

	children, err := db.ListTicketsByParent(ctx, parent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(children) != 1 {
		t.Fatalf("expected 1 child, got %d", len(children))
	}
	if children[0].Title != "Child Task" {
		t.Error("child title mismatch")
	}
}

func TestTicketGantt(t *testing.T) {
	pool := testutil.SetupTestDB(t)
	db := NewDB(pool)
	ctx := context.Background()

	org, _ := db.CreateOrg(ctx, "Gantt Org", "gantt-org")
	proj, _ := db.CreateProject(ctx, org.ID, "Gantt Proj", "gantt-proj")
	user, _ := db.CreateUser(ctx, "gantt@test.com", "hash", "Gantt", "client")

	// Ticket with dates
	_, err := pool.Exec(ctx,
		`INSERT INTO tickets (project_id, type, title, status, priority, date_start, date_end, created_by)
		 VALUES ($1, 'task', 'Dated', 'backlog', 'medium', '2026-03-01', '2026-03-15', $2)`,
		proj.ID, user.ID)
	if err != nil {
		t.Fatal(err)
	}

	// Ticket without dates
	noDates := &Ticket{ProjectID: proj.ID, Type: "task", Title: "No Dates", Status: "backlog", Priority: "medium", CreatedBy: user.ID}
	db.CreateTicket(ctx, noDates)

	gantt, err := db.ListGanttTickets(ctx, proj.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(gantt) != 1 {
		t.Fatalf("expected 1 gantt ticket, got %d", len(gantt))
	}
	if gantt[0].Title != "Dated" {
		t.Error("expected ticket with dates")
	}
}

func TestListAgentReady(t *testing.T) {
	pool := testutil.SetupTestDB(t)
	db := NewDB(pool)
	ctx := context.Background()

	org, _ := db.CreateOrg(ctx, "Agent Org", "agent-org")
	proj, _ := db.CreateProject(ctx, org.ID, "Agent Proj", "agent-proj")
	user, _ := db.CreateUser(ctx, "agent@test.com", "hash", "Agent", "client")

	mode := "plan"
	agent := "claude"

	// Ready ticket with agent mode
	t1 := &Ticket{ProjectID: proj.ID, Type: "task", Title: "Ready Agent", Status: "ready", Priority: "high", CreatedBy: user.ID, AgentMode: &mode, AgentName: &agent}
	db.CreateTicket(ctx, t1)

	// Backlog ticket (not agent-ready)
	t2 := &Ticket{ProjectID: proj.ID, Type: "task", Title: "Backlog", Status: "backlog", Priority: "medium", CreatedBy: user.ID, AgentMode: &mode}
	db.CreateTicket(ctx, t2)

	// No agent mode
	t3 := &Ticket{ProjectID: proj.ID, Type: "task", Title: "No Agent", Status: "ready", Priority: "medium", CreatedBy: user.ID}
	db.CreateTicket(ctx, t3)

	ready, err := db.ListAgentReady(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(ready) != 1 {
		t.Fatalf("expected 1 agent-ready ticket, got %d", len(ready))
	}
	if ready[0].Title != "Ready Agent" {
		t.Error("wrong ticket")
	}
}

func TestCommentCRUD(t *testing.T) {
	pool := testutil.SetupTestDB(t)
	db := NewDB(pool)
	ctx := context.Background()

	org, _ := db.CreateOrg(ctx, "Comment Org", "comment-org")
	proj, _ := db.CreateProject(ctx, org.ID, "Comment Proj", "comment-proj")
	user, _ := db.CreateUser(ctx, "commenter@test.com", "hash", "Commenter", "client")

	ticket := &Ticket{ProjectID: proj.ID, Type: "task", Title: "Commented", Status: "backlog", Priority: "medium", CreatedBy: user.ID}
	db.CreateTicket(ctx, ticket)

	// User comment
	c1, err := db.CreateComment(ctx, ticket.ID, &user.ID, nil, "Hello world")
	if err != nil {
		t.Fatalf("CreateComment: %v", err)
	}
	if c1.BodyMarkdown != "Hello world" {
		t.Error("body mismatch")
	}

	// Agent comment
	agentName := "claude"
	c2, err := db.CreateComment(ctx, ticket.ID, nil, &agentName, "Agent response")
	if err != nil {
		t.Fatal(err)
	}
	if c2.AgentName == nil || *c2.AgentName != "claude" {
		t.Error("agent name not set")
	}

	comments, err := db.ListComments(ctx, ticket.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(comments) != 2 {
		t.Fatalf("expected 2 comments, got %d", len(comments))
	}
}

func TestActivityCreate(t *testing.T) {
	pool := testutil.SetupTestDB(t)
	db := NewDB(pool)
	ctx := context.Background()

	org, _ := db.CreateOrg(ctx, "Activity Org", "activity-org")
	proj, _ := db.CreateProject(ctx, org.ID, "Activity Proj", "activity-proj")
	user, _ := db.CreateUser(ctx, "activity@test.com", "hash", "Activity", "client")

	ticket := &Ticket{ProjectID: proj.ID, Type: "task", Title: "Activity Test", Status: "backlog", Priority: "medium", CreatedBy: user.ID}
	db.CreateTicket(ctx, ticket)

	err := db.CreateActivity(ctx, ticket.ID, &user.ID, nil, "status_change", `{"new_status":"ready"}`)
	if err != nil {
		t.Fatalf("CreateActivity: %v", err)
	}
}

func TestWebAuthnCredentialCRUD(t *testing.T) {
	pool := testutil.SetupTestDB(t)
	db := NewDB(pool)
	ctx := context.Background()

	user, _ := db.CreateUser(ctx, "webauthn@test.com", "hash", "WebAuthn", "client")

	cred := &WebAuthnCredential{
		UserID:              user.ID,
		CredentialID:        []byte("cred-id-123"),
		PublicKey:           []byte("pub-key-123"),
		AttestationType:     "direct",
		AuthenticatorAAGUID: []byte("aaguid"),
		SignCount:           0,
		Name:                "YubiKey",
	}
	if err := db.CreateWebAuthnCredential(ctx, cred); err != nil {
		t.Fatalf("CreateWebAuthnCredential: %v", err)
	}

	creds, err := db.ListWebAuthnCredentials(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(creds) != 1 {
		t.Fatalf("expected 1 credential, got %d", len(creds))
	}
	if creds[0].Name != "YubiKey" {
		t.Error("name mismatch")
	}

	// Update sign count
	err = db.UpdateWebAuthnSignCount(ctx, creds[0].ID, 5)
	if err != nil {
		t.Fatal(err)
	}

	// Count
	count, err := db.CountUserWebAuthnCredentials(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Errorf("count = %d, want 1", count)
	}

	// Delete single
	if err := db.DeleteWebAuthnCredential(ctx, creds[0].ID); err != nil {
		t.Fatal(err)
	}
	count2, _ := db.CountUserWebAuthnCredentials(ctx, user.ID)
	if count2 != 0 {
		t.Error("credential should be deleted")
	}
}

func TestClearUser2FA(t *testing.T) {
	pool := testutil.SetupTestDB(t)
	db := NewDB(pool)
	ctx := context.Background()

	user, _ := db.CreateUser(ctx, "clear2fa@test.com", "hash", "Clear2FA", "client")

	// Set up TOTP
	db.UpdateUserTOTP(ctx, user.ID, []byte("encrypted-secret"), true, "totp")
	db.UpdateUserRecoveryCodes(ctx, user.ID, []byte("encrypted-codes"))

	// Clear 2FA
	if err := db.ClearUser2FA(ctx, user.ID); err != nil {
		t.Fatal(err)
	}

	updated, _ := db.GetUserByID(ctx, user.ID)
	if updated.TOTPSecret != nil {
		t.Error("TOTP secret should be nil")
	}
	if updated.TOTPVerified {
		t.Error("TOTP verified should be false")
	}
	if updated.RecoveryCodes != nil {
		t.Error("recovery codes should be nil")
	}
	if !updated.MustSetup2FA {
		t.Error("must_setup_2fa should be true")
	}
}

func TestConsumeRecoveryCode(t *testing.T) {
	pool := testutil.SetupTestDB(t)
	db := NewDB(pool)
	ctx := context.Background()

	user, err := db.CreateUser(ctx, "recovery@test.com", "hash", "Recovery User", "client")
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	// Set initial recovery codes
	initialCodes := []byte(`["code1","code2","code3"]`)
	if err := db.UpdateUserRecoveryCodes(ctx, user.ID, initialCodes); err != nil {
		t.Fatalf("UpdateUserRecoveryCodes: %v", err)
	}

	// Verify codes were set
	found, _ := db.GetUserByID(ctx, user.ID)
	if found.RecoveryCodes == nil {
		t.Fatal("recovery codes should be set")
	}

	// Consume a code (update with fewer codes)
	remainingCodes := []byte(`["code2","code3"]`)
	if err := db.ConsumeRecoveryCode(ctx, user.ID, remainingCodes); err != nil {
		t.Fatalf("ConsumeRecoveryCode: %v", err)
	}

	updated, _ := db.GetUserByID(ctx, user.ID)
	if !bytes.Equal(updated.RecoveryCodes, remainingCodes) {
		t.Errorf("recovery codes = %q, want %q", string(updated.RecoveryCodes), string(remainingCodes))
	}

	// Consume all codes (set to empty array)
	emptyCodes := []byte(`[]`)
	if err := db.ConsumeRecoveryCode(ctx, user.ID, emptyCodes); err != nil {
		t.Fatalf("ConsumeRecoveryCode (empty): %v", err)
	}

	final, _ := db.GetUserByID(ctx, user.ID)
	if !bytes.Equal(final.RecoveryCodes, emptyCodes) {
		t.Errorf("recovery codes = %q, want %q", string(final.RecoveryCodes), string(emptyCodes))
	}
}

func TestUpdateUserEmail(t *testing.T) {
	pool := testutil.SetupTestDB(t)
	db := NewDB(pool)
	ctx := context.Background()

	user, err := db.CreateUser(ctx, "oldemail@test.com", "hash", "Email User", "client")
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	// Update email
	err = db.UpdateUserEmail(ctx, user.ID, "newemail@test.com")
	if err != nil {
		t.Fatalf("UpdateUserEmail: %v", err)
	}

	updated, err := db.GetUserByID(ctx, user.ID)
	if err != nil {
		t.Fatalf("GetUserByID: %v", err)
	}
	if updated.Email != "newemail@test.com" {
		t.Errorf("Email = %q, want newemail@test.com", updated.Email)
	}

	// Verify old email no longer resolves
	_, err = db.GetUserByEmail(ctx, "oldemail@test.com")
	if err == nil {
		t.Error("old email should not resolve anymore")
	}

	// Verify new email resolves
	found, err := db.GetUserByEmail(ctx, "newemail@test.com")
	if err != nil {
		t.Fatalf("GetUserByEmail (new): %v", err)
	}
	if found.ID != user.ID {
		t.Errorf("user ID mismatch: %s vs %s", found.ID, user.ID)
	}

	// Test duplicate email conflict
	_, err = db.CreateUser(ctx, "existing@test.com", "hash", "Existing", "client")
	if err != nil {
		t.Fatal(err)
	}
	err = db.UpdateUserEmail(ctx, user.ID, "existing@test.com")
	if err == nil {
		t.Error("expected error for duplicate email")
	}
}

func TestCountUsers(t *testing.T) {
	pool := testutil.SetupTestDB(t)
	db := NewDB(pool)
	ctx := context.Background()

	// Count with no users
	count, err := db.CountUsers(ctx)
	if err != nil {
		t.Fatalf("CountUsers: %v", err)
	}
	if count != 0 {
		t.Errorf("count = %d, want 0", count)
	}

	// Add users and recount
	db.CreateUser(ctx, "count1@test.com", "hash", "User1", "client")
	db.CreateUser(ctx, "count2@test.com", "hash", "User2", "staff")
	db.CreateUser(ctx, "count3@test.com", "hash", "User3", "superadmin")

	count, err = db.CountUsers(ctx)
	if err != nil {
		t.Fatalf("CountUsers: %v", err)
	}
	if count != 3 {
		t.Errorf("count = %d, want 3", count)
	}
}

func TestInvitationLifecycle(t *testing.T) {
	pool := testutil.SetupTestDB(t)
	db := NewDB(pool)
	ctx := context.Background()

	inviter, _ := db.CreateUser(ctx, "inviter@test.com", "hash", "Inviter", "staff")
	org, _ := db.CreateOrg(ctx, "Invite Org", "invite-org")
	db.AddOrgMember(ctx, inviter.ID, org.ID, "owner")

	expires := time.Now().Add(48 * time.Hour)

	// CreateInvitation
	inv, err := db.CreateInvitation(ctx, "newuser@test.com", org.ID, "member", "token-hash-1", inviter.ID, expires)
	if err != nil {
		t.Fatalf("CreateInvitation: %v", err)
	}
	if inv.ID == "" {
		t.Fatal("invitation ID should not be empty")
	}
	if inv.Email != "newuser@test.com" {
		t.Errorf("Email = %q, want newuser@test.com", inv.Email)
	}
	if inv.OrgRole != "member" {
		t.Errorf("OrgRole = %q, want member", inv.OrgRole)
	}
	if inv.Status != "pending" {
		t.Errorf("Status = %q, want pending", inv.Status)
	}
	if inv.InvitedBy != inviter.ID {
		t.Errorf("InvitedBy = %q, want %q", inv.InvitedBy, inviter.ID)
	}

	// GetInvitationByToken
	found, err := db.GetInvitationByToken(ctx, "token-hash-1")
	if err != nil {
		t.Fatalf("GetInvitationByToken: %v", err)
	}
	if found.ID != inv.ID {
		t.Errorf("ID mismatch: %s vs %s", found.ID, inv.ID)
	}

	// GetInvitationByToken with wrong token
	_, err = db.GetInvitationByToken(ctx, "wrong-token")
	if err == nil {
		t.Error("expected error for wrong token")
	}

	// GetInvitationByID
	found2, err := db.GetInvitationByID(ctx, inv.ID)
	if err != nil {
		t.Fatalf("GetInvitationByID: %v", err)
	}
	if found2.Email != "newuser@test.com" {
		t.Error("email mismatch")
	}

	// HasPendingInvitation - should be true
	hasPending, err := db.HasPendingInvitation(ctx, "newuser@test.com", org.ID)
	if err != nil {
		t.Fatalf("HasPendingInvitation: %v", err)
	}
	if !hasPending {
		t.Error("expected pending invitation to exist")
	}

	// HasPendingInvitation - wrong email
	hasPending2, err := db.HasPendingInvitation(ctx, "nobody@test.com", org.ID)
	if err != nil {
		t.Fatal(err)
	}
	if hasPending2 {
		t.Error("expected no pending invitation for unknown email")
	}

	// ListPendingInvitationsForUser
	pending, err := db.ListPendingInvitationsForUser(ctx, "newuser@test.com")
	if err != nil {
		t.Fatalf("ListPendingInvitationsForUser: %v", err)
	}
	if len(pending) != 1 {
		t.Fatalf("expected 1 pending invitation, got %d", len(pending))
	}
	if pending[0].Organization.Name != "Invite Org" {
		t.Errorf("org name = %q, want Invite Org", pending[0].Organization.Name)
	}
	if pending[0].InviterName != "Inviter" {
		t.Errorf("inviter name = %q, want Inviter", pending[0].InviterName)
	}

	// ListOrgInvitations
	orgInvs, err := db.ListOrgInvitations(ctx, org.ID)
	if err != nil {
		t.Fatalf("ListOrgInvitations: %v", err)
	}
	if len(orgInvs) != 1 {
		t.Fatalf("expected 1 org invitation, got %d", len(orgInvs))
	}
	if orgInvs[0].InviterName != "Inviter" {
		t.Errorf("inviter name = %q, want Inviter", orgInvs[0].InviterName)
	}

	// ResetInvitationToken
	newExpiry := time.Now().Add(72 * time.Hour)
	err = db.ResetInvitationToken(ctx, inv.ID, "new-token-hash", newExpiry)
	if err != nil {
		t.Fatalf("ResetInvitationToken: %v", err)
	}
	// Old token should not work
	_, err = db.GetInvitationByToken(ctx, "token-hash-1")
	if err == nil {
		t.Error("old token should no longer work after reset")
	}
	// New token should work
	foundNew, err := db.GetInvitationByToken(ctx, "new-token-hash")
	if err != nil {
		t.Fatalf("GetInvitationByToken (new): %v", err)
	}
	if foundNew.ID != inv.ID {
		t.Error("invitation ID mismatch after token reset")
	}

	// UpdateInvitationStatus - accept the invitation
	err = db.UpdateInvitationStatus(ctx, inv.ID, "accepted")
	if err != nil {
		t.Fatalf("UpdateInvitationStatus: %v", err)
	}
	accepted, _ := db.GetInvitationByID(ctx, inv.ID)
	if accepted.Status != "accepted" {
		t.Errorf("status = %q, want accepted", accepted.Status)
	}

	// After accepting, GetInvitationByToken should not find it (filters pending only)
	_, err = db.GetInvitationByToken(ctx, "new-token-hash")
	if err == nil {
		t.Error("accepted invitation should not be found by token lookup")
	}

	// HasPendingInvitation should now be false
	hasPending3, _ := db.HasPendingInvitation(ctx, "newuser@test.com", org.ID)
	if hasPending3 {
		t.Error("no pending invitation should exist after acceptance")
	}
}

func TestExpireOldInvitations(t *testing.T) {
	pool := testutil.SetupTestDB(t)
	db := NewDB(pool)
	ctx := context.Background()

	inviter, _ := db.CreateUser(ctx, "expirer@test.com", "hash", "Expirer", "staff")
	org, _ := db.CreateOrg(ctx, "Expire Org", "expire-org")
	db.AddOrgMember(ctx, inviter.ID, org.ID, "owner")

	// Create an already-expired invitation (expires_at in the past)
	pastExpiry := time.Now().Add(-1 * time.Hour)
	_, err := db.CreateInvitation(ctx, "expired@test.com", org.ID, "member", "expired-token", inviter.ID, pastExpiry)
	if err != nil {
		t.Fatalf("CreateInvitation (expired): %v", err)
	}

	// Create a valid invitation (expires in the future)
	futureExpiry := time.Now().Add(48 * time.Hour)
	validInv, err := db.CreateInvitation(ctx, "valid@test.com", org.ID, "member", "valid-token", inviter.ID, futureExpiry)
	if err != nil {
		t.Fatalf("CreateInvitation (valid): %v", err)
	}

	// Run expire
	err = db.ExpireOldInvitations(ctx)
	if err != nil {
		t.Fatalf("ExpireOldInvitations: %v", err)
	}

	// The expired invitation should be marked expired
	// HasPendingInvitation checks status=pending AND expires_at > now()
	hasPending, _ := db.HasPendingInvitation(ctx, "expired@test.com", org.ID)
	if hasPending {
		t.Error("expired invitation should not show as pending")
	}

	// The valid invitation should still be pending
	hasPendingValid, _ := db.HasPendingInvitation(ctx, "valid@test.com", org.ID)
	if !hasPendingValid {
		t.Error("valid invitation should still be pending")
	}

	// Verify the valid invitation is still retrievable
	_, err = db.GetInvitationByID(ctx, validInv.ID)
	if err != nil {
		t.Fatalf("valid invitation should still be retrievable: %v", err)
	}
}

func TestRemoveOrgMember(t *testing.T) {
	pool := testutil.SetupTestDB(t)
	db := NewDB(pool)
	ctx := context.Background()

	user1, _ := db.CreateUser(ctx, "remove1@test.com", "hash", "User1", "client")
	user2, _ := db.CreateUser(ctx, "remove2@test.com", "hash", "User2", "client")
	org, _ := db.CreateOrg(ctx, "Remove Org", "remove-org")

	db.AddOrgMember(ctx, user1.ID, org.ID, "owner")
	db.AddOrgMember(ctx, user2.ID, org.ID, "member")

	// Verify both are members
	members, _ := db.ListOrgMembers(ctx, org.ID)
	if len(members) != 2 {
		t.Fatalf("expected 2 members, got %d", len(members))
	}

	// Remove user2
	if err := db.RemoveOrgMember(ctx, user2.ID, org.ID); err != nil {
		t.Fatalf("RemoveOrgMember: %v", err)
	}

	// Verify only user1 remains
	members2, _ := db.ListOrgMembers(ctx, org.ID)
	if len(members2) != 1 {
		t.Fatalf("expected 1 member after removal, got %d", len(members2))
	}

	// Verify removed user has no membership
	_, err := db.GetOrgMembership(ctx, user2.ID, org.ID)
	if err == nil {
		t.Error("expected error when getting removed membership")
	}

	// Removing non-existent membership should not error
	if err := db.RemoveOrgMember(ctx, user2.ID, org.ID); err != nil {
		t.Fatalf("RemoveOrgMember (idempotent): %v", err)
	}
}

func TestUpdateOrgMemberRole(t *testing.T) {
	pool := testutil.SetupTestDB(t)
	db := NewDB(pool)
	ctx := context.Background()

	user, _ := db.CreateUser(ctx, "roleupdate@test.com", "hash", "RoleUser", "client")
	org, _ := db.CreateOrg(ctx, "Role Org", "role-org")

	db.AddOrgMember(ctx, user.ID, org.ID, "member")

	// Verify initial role
	mem, _ := db.GetOrgMembership(ctx, user.ID, org.ID)
	if mem.Role != "member" {
		t.Errorf("initial role = %q, want member", mem.Role)
	}

	// Update to admin
	if err := db.UpdateOrgMemberRole(ctx, user.ID, org.ID, "admin"); err != nil {
		t.Fatalf("UpdateOrgMemberRole: %v", err)
	}
	mem2, _ := db.GetOrgMembership(ctx, user.ID, org.ID)
	if mem2.Role != "admin" {
		t.Errorf("role = %q, want admin", mem2.Role)
	}

	// Update to owner
	if err := db.UpdateOrgMemberRole(ctx, user.ID, org.ID, "owner"); err != nil {
		t.Fatalf("UpdateOrgMemberRole to owner: %v", err)
	}
	mem3, _ := db.GetOrgMembership(ctx, user.ID, org.ID)
	if mem3.Role != "owner" {
		t.Errorf("role = %q, want owner", mem3.Role)
	}
}

func TestCountOrgOwners(t *testing.T) {
	pool := testutil.SetupTestDB(t)
	db := NewDB(pool)
	ctx := context.Background()

	user1, _ := db.CreateUser(ctx, "owner1@test.com", "hash", "Owner1", "client")
	user2, _ := db.CreateUser(ctx, "owner2@test.com", "hash", "Owner2", "client")
	user3, _ := db.CreateUser(ctx, "member1@test.com", "hash", "Member1", "client")
	org, _ := db.CreateOrg(ctx, "Owners Org", "owners-org")

	// No members yet
	count, err := db.CountOrgOwners(ctx, org.ID)
	if err != nil {
		t.Fatalf("CountOrgOwners: %v", err)
	}
	if count != 0 {
		t.Errorf("count = %d, want 0", count)
	}

	// Add one owner
	db.AddOrgMember(ctx, user1.ID, org.ID, "owner")
	count, _ = db.CountOrgOwners(ctx, org.ID)
	if count != 1 {
		t.Errorf("count = %d, want 1", count)
	}

	// Add second owner
	db.AddOrgMember(ctx, user2.ID, org.ID, "owner")
	count, _ = db.CountOrgOwners(ctx, org.ID)
	if count != 2 {
		t.Errorf("count = %d, want 2", count)
	}

	// Add a member (not owner) - count should stay at 2
	db.AddOrgMember(ctx, user3.ID, org.ID, "member")
	count, _ = db.CountOrgOwners(ctx, org.ID)
	if count != 2 {
		t.Errorf("count = %d, want 2 (member should not be counted)", count)
	}

	// Demote one owner to admin - count should drop to 1
	db.UpdateOrgMemberRole(ctx, user2.ID, org.ID, "admin")
	count, _ = db.CountOrgOwners(ctx, org.ID)
	if count != 1 {
		t.Errorf("count = %d, want 1 after demotion", count)
	}
}

func TestUpdateOrgAIMargin(t *testing.T) {
	pool := testutil.SetupTestDB(t)
	db := NewDB(pool)
	ctx := context.Background()

	org, err := db.CreateOrg(ctx, "Margin Org", "margin-org")
	if err != nil {
		t.Fatalf("CreateOrg: %v", err)
	}

	// Default should be 0
	if org.AIMarginPercent != 0 {
		t.Errorf("default AIMarginPercent = %d, want 0", org.AIMarginPercent)
	}

	// Update to 25%
	if err := db.UpdateOrgAIMargin(ctx, org.ID, 25); err != nil {
		t.Fatalf("UpdateOrgAIMargin: %v", err)
	}
	updated, _ := db.GetOrgByID(ctx, org.ID)
	if updated.AIMarginPercent != 25 {
		t.Errorf("AIMarginPercent = %d, want 25", updated.AIMarginPercent)
	}

	// Update to 100%
	if err := db.UpdateOrgAIMargin(ctx, org.ID, 100); err != nil {
		t.Fatalf("UpdateOrgAIMargin to 100: %v", err)
	}
	updated2, _ := db.GetOrgByID(ctx, org.ID)
	if updated2.AIMarginPercent != 100 {
		t.Errorf("AIMarginPercent = %d, want 100", updated2.AIMarginPercent)
	}

	// Update back to 0
	if err := db.UpdateOrgAIMargin(ctx, org.ID, 0); err != nil {
		t.Fatalf("UpdateOrgAIMargin to 0: %v", err)
	}
	updated3, _ := db.GetOrgByID(ctx, org.ID)
	if updated3.AIMarginPercent != 0 {
		t.Errorf("AIMarginPercent = %d, want 0", updated3.AIMarginPercent)
	}
}
