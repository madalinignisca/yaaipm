package models

import (
	"context"
	"testing"

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
	if err := db.AddOrgMember(ctx, user.ID, org.ID, "admin"); err != nil {
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
	if err := db.UpdateProjectBrief(ctx, proj.ID, "# Brief\nThis is the brief."); err != nil {
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
	if err := db.UpdateTicketStatus(ctx, ticket.ID, "ready"); err != nil {
		t.Fatal(err)
	}
	found2, _ := db.GetTicket(ctx, ticket.ID)
	if found2.Status != "ready" {
		t.Errorf("status not updated: %q", found2.Status)
	}

	// Update agent mode
	mode := "plan"
	agent := "claude"
	if err := db.UpdateTicketAgentMode(ctx, ticket.ID, &mode, &agent); err != nil {
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
	epics, _ := db.ListEpics(ctx, proj.ID)
	if len(epics) != 0 {
		t.Error("expected 0 epics")
	}
}

func TestTicketParentChild(t *testing.T) {
	pool := testutil.SetupTestDB(t)
	db := NewDB(pool)
	ctx := context.Background()

	org, _ := db.CreateOrg(ctx, "PC Org", "pc-org")
	proj, _ := db.CreateProject(ctx, org.ID, "PC Project", "pc-proj")
	user, _ := db.CreateUser(ctx, "pc@test.com", "hash", "PC", "client")

	parent := &Ticket{ProjectID: proj.ID, Type: "epic", Title: "Epic", Status: "backlog", Priority: "high", CreatedBy: user.ID}
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
	if err := db.UpdateWebAuthnSignCount(ctx, creds[0].ID, 5); err != nil {
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
