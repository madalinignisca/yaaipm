package models

import (
	"context"
	"testing"
	"time"

	"github.com/madalin/forgedesk/internal/testutil"
)

// ── Helpers ──────────────────────────────────────────────────────

// setupExtendedTestDB wraps testutil.SetupTestDB and also cleans tables
// that may not be in the base cleanup list (project_costs, ai_usage_entries).
func setupExtendedTestDB(t *testing.T) *DB {
	t.Helper()
	pool := testutil.SetupTestDB(t)

	// Clean additional tables that may not be in base testutil cleanup
	for _, table := range []string{"project_costs", "ai_usage_entries"} {
		if _, err := pool.Exec(context.Background(), "DELETE FROM "+table); err != nil {
			// Table might not exist in older migrations; ignore
			t.Logf("note: cleaning %s: %v (may be OK)", table, err)
		}
	}

	return NewDB(pool)
}

// createTestUser creates a user with a unique email derived from a label.
func createTestUser(t *testing.T, db *DB, label string) *User {
	t.Helper()
	user, err := db.CreateUser(context.Background(), label+"@ext-test.com", "hash", label, "client")
	if err != nil {
		t.Fatalf("createTestUser(%s): %v", label, err)
	}
	return user
}

// createTestOrg creates an org with a unique slug derived from a label.
func createTestOrg(t *testing.T, db *DB, label string) *Organization {
	t.Helper()
	org, err := db.CreateOrg(context.Background(), label+" Org", label+"-org")
	if err != nil {
		t.Fatalf("createTestOrg(%s): %v", label, err)
	}
	return org
}

// createTestProject creates a project under the given org.
func createTestProject(t *testing.T, db *DB, orgID, label string) *Project {
	t.Helper()
	proj, err := db.CreateProject(context.Background(), orgID, label+" Project", label+"-proj")
	if err != nil {
		t.Fatalf("createTestProject(%s): %v", label, err)
	}
	return proj
}

// createTestTicket creates a ticket with reasonable defaults.
func createTestTicket(t *testing.T, db *DB, projectID, userID, ticketType, title string) *Ticket {
	t.Helper()
	ticket := &Ticket{
		ProjectID:           projectID,
		Type:                ticketType,
		Title:               title,
		DescriptionMarkdown: "Description for " + title,
		Status:              "backlog",
		Priority:            "medium",
		CreatedBy:           userID,
	}
	if err := db.CreateTicket(context.Background(), ticket); err != nil {
		t.Fatalf("createTestTicket(%s): %v", title, err)
	}
	return ticket
}

// ── ListBugs ─────────────────────────────────────────────────────

func TestListBugs(t *testing.T) {
	db := setupExtendedTestDB(t)
	ctx := context.Background()

	user := createTestUser(t, db, "listbugs")
	org := createTestOrg(t, db, "listbugs")
	proj := createTestProject(t, db, org.ID, "listbugs")

	// Create tickets of different types
	createTestTicket(t, db, proj.ID, user.ID, "bug", "Bug One")
	createTestTicket(t, db, proj.ID, user.ID, "bug", "Bug Two")
	createTestTicket(t, db, proj.ID, user.ID, "task", "A Task")
	createTestTicket(t, db, proj.ID, user.ID, "feature", "A Feature")

	bugs, err := db.ListBugs(ctx, proj.ID)
	if err != nil {
		t.Fatalf("ListBugs: %v", err)
	}
	if len(bugs) != 2 {
		t.Fatalf("expected 2 bugs, got %d", len(bugs))
	}
	for _, b := range bugs {
		if b.Type != "bug" {
			t.Errorf("expected type 'bug', got %q", b.Type)
		}
	}
}

func TestListBugsEmpty(t *testing.T) {
	db := setupExtendedTestDB(t)
	ctx := context.Background()

	org := createTestOrg(t, db, "listbugsempty")
	proj := createTestProject(t, db, org.ID, "listbugsempty")

	bugs, err := db.ListBugs(ctx, proj.ID)
	if err != nil {
		t.Fatalf("ListBugs: %v", err)
	}
	if len(bugs) != 0 {
		t.Fatalf("expected 0 bugs, got %d", len(bugs))
	}
}

// ── Ticket Archive Lifecycle ─────────────────────────────────────

func TestTicketArchiveLifecycle(t *testing.T) {
	db := setupExtendedTestDB(t)
	ctx := context.Background()

	user := createTestUser(t, db, "archive")
	org := createTestOrg(t, db, "archive")
	proj := createTestProject(t, db, org.ID, "archive")

	// Create a parent (feature) with a child (task)
	parent := createTestTicket(t, db, proj.ID, user.ID, "feature", "Archivable Feature")
	child := &Ticket{
		ProjectID: proj.ID,
		ParentID:  &parent.ID,
		Type:      "task",
		Title:     "Child Task",
		Status:    "backlog",
		Priority:  "medium",
		CreatedBy: user.ID,
	}
	if err := db.CreateTicket(ctx, child); err != nil {
		t.Fatalf("CreateTicket(child): %v", err)
	}

	// Also create a standalone ticket that should not be affected
	standalone := createTestTicket(t, db, proj.ID, user.ID, "bug", "Standalone Bug")

	// Archive the parent
	if err := db.ArchiveTicket(ctx, parent.ID); err != nil {
		t.Fatalf("ArchiveTicket: %v", err)
	}

	// Both parent and child should be archived
	archivedParent, _ := db.GetTicket(ctx, parent.ID)
	if archivedParent.ArchivedAt == nil {
		t.Error("parent should be archived")
	}
	archivedChild, _ := db.GetTicket(ctx, child.ID)
	if archivedChild.ArchivedAt == nil {
		t.Error("child should be archived when parent is archived")
	}

	// Standalone should not be archived
	standaloneCheck, _ := db.GetTicket(ctx, standalone.ID)
	if standaloneCheck.ArchivedAt != nil {
		t.Error("standalone ticket should not be archived")
	}

	// ListArchivedTickets should return parent only (parent_id IS NULL filter)
	archived, err := db.ListArchivedTickets(ctx, proj.ID)
	if err != nil {
		t.Fatalf("ListArchivedTickets: %v", err)
	}
	if len(archived) != 1 {
		t.Fatalf("expected 1 archived top-level ticket, got %d", len(archived))
	}
	if archived[0].ID != parent.ID {
		t.Error("archived ticket should be the parent")
	}

	// Active list should not contain archived tickets
	active, _ := db.ListTickets(ctx, proj.ID, "")
	for _, a := range active {
		if a.ID == parent.ID || a.ID == child.ID {
			t.Error("archived tickets should not appear in ListTickets")
		}
	}

	// Restore the parent
	if err := db.RestoreTicket(ctx, parent.ID); err != nil {
		t.Fatalf("RestoreTicket: %v", err)
	}

	restoredParent, _ := db.GetTicket(ctx, parent.ID)
	if restoredParent.ArchivedAt != nil {
		t.Error("parent should be restored (archived_at should be nil)")
	}
	restoredChild, _ := db.GetTicket(ctx, child.ID)
	if restoredChild.ArchivedAt != nil {
		t.Error("child should be restored when parent is restored")
	}

	// ListArchivedTickets should now be empty
	archivedAfterRestore, _ := db.ListArchivedTickets(ctx, proj.ID)
	if len(archivedAfterRestore) != 0 {
		t.Fatalf("expected 0 archived tickets after restore, got %d", len(archivedAfterRestore))
	}
}

func TestDeleteTicket(t *testing.T) {
	db := setupExtendedTestDB(t)
	ctx := context.Background()

	user := createTestUser(t, db, "delete")
	org := createTestOrg(t, db, "delete")
	proj := createTestProject(t, db, org.ID, "delete")

	// Create parent with child and a comment
	parent := createTestTicket(t, db, proj.ID, user.ID, "feature", "Deletable Feature")
	child := &Ticket{
		ProjectID: proj.ID,
		ParentID:  &parent.ID,
		Type:      "task",
		Title:     "Child to Delete",
		Status:    "backlog",
		Priority:  "medium",
		CreatedBy: user.ID,
	}
	db.CreateTicket(ctx, child)

	// Add a comment on the parent
	_, err := db.CreateComment(ctx, parent.ID, &user.ID, nil, "A comment")
	if err != nil {
		t.Fatalf("CreateComment: %v", err)
	}

	// Delete the parent (should cascade children + comments)
	err = db.DeleteTicket(ctx, parent.ID)
	if err != nil {
		t.Fatalf("DeleteTicket: %v", err)
	}

	// Parent should be gone
	_, err = db.GetTicket(ctx, parent.ID)
	if err == nil {
		t.Error("parent ticket should be deleted")
	}

	// Child should be gone
	_, err = db.GetTicket(ctx, child.ID)
	if err == nil {
		t.Error("child ticket should be deleted")
	}
}

// ── UpdateTicket ─────────────────────────────────────────────────

func TestUpdateTicket(t *testing.T) {
	db := setupExtendedTestDB(t)
	ctx := context.Background()

	user := createTestUser(t, db, "update")
	org := createTestOrg(t, db, "update")
	proj := createTestProject(t, db, org.ID, "update")

	ticket := createTestTicket(t, db, proj.ID, user.ID, "task", "Original Title")

	// Update fields
	now := time.Now().Truncate(24 * time.Hour)
	later := now.Add(7 * 24 * time.Hour)
	ticket.Title = "Updated Title"
	ticket.DescriptionMarkdown = "Updated description"
	ticket.Priority = "high"
	ticket.DateStart = &now
	ticket.DateEnd = &later
	ticket.AssignedTo = &user.ID

	if err := db.UpdateTicket(ctx, ticket); err != nil {
		t.Fatalf("UpdateTicket: %v", err)
	}

	updated, err := db.GetTicket(ctx, ticket.ID)
	if err != nil {
		t.Fatalf("GetTicket after update: %v", err)
	}
	if updated.Title != "Updated Title" {
		t.Errorf("Title = %q, want 'Updated Title'", updated.Title)
	}
	if updated.DescriptionMarkdown != "Updated description" {
		t.Errorf("Description not updated")
	}
	if updated.Priority != "high" {
		t.Errorf("Priority = %q, want 'high'", updated.Priority)
	}
	if updated.DateStart == nil {
		t.Error("DateStart should be set")
	}
	if updated.DateEnd == nil {
		t.Error("DateEnd should be set")
	}
	if updated.AssignedTo == nil || *updated.AssignedTo != user.ID {
		t.Error("AssignedTo should be set to user ID")
	}

	// Clear optional fields
	ticket.DateStart = nil
	ticket.DateEnd = nil
	ticket.AssignedTo = nil
	if err := db.UpdateTicket(ctx, ticket); err != nil {
		t.Fatalf("UpdateTicket (clear): %v", err)
	}
	cleared, _ := db.GetTicket(ctx, ticket.ID)
	if cleared.DateStart != nil {
		t.Error("DateStart should be nil after clearing")
	}
	if cleared.AssignedTo != nil {
		t.Error("AssignedTo should be nil after clearing")
	}
}

// ── DeleteAllWebAuthnCredentials ─────────────────────────────────

func TestDeleteAllWebAuthnCredentials(t *testing.T) {
	db := setupExtendedTestDB(t)
	ctx := context.Background()

	user := createTestUser(t, db, "webauthnall")

	// Create multiple credentials
	for i, name := range []string{"YubiKey 1", "YubiKey 2", "Touch ID"} {
		cred := &WebAuthnCredential{
			UserID:              user.ID,
			CredentialID:        []byte("cred-" + name),
			PublicKey:           []byte("key-" + name),
			AttestationType:     "direct",
			AuthenticatorAAGUID: []byte{byte(i)},
			SignCount:           0,
			Name:                name,
		}
		if err := db.CreateWebAuthnCredential(ctx, cred); err != nil {
			t.Fatalf("CreateWebAuthnCredential(%s): %v", name, err)
		}
	}

	count, _ := db.CountUserWebAuthnCredentials(ctx, user.ID)
	if count != 3 {
		t.Fatalf("expected 3 credentials, got %d", count)
	}

	// Delete all
	if err := db.DeleteAllWebAuthnCredentials(ctx, user.ID); err != nil {
		t.Fatalf("DeleteAllWebAuthnCredentials: %v", err)
	}

	count, _ = db.CountUserWebAuthnCredentials(ctx, user.ID)
	if count != 0 {
		t.Errorf("expected 0 credentials after delete all, got %d", count)
	}
}

func TestDeleteAllWebAuthnCredentialsNoOp(t *testing.T) {
	db := setupExtendedTestDB(t)
	ctx := context.Background()

	user := createTestUser(t, db, "webauthnallnoop")

	// Delete all when none exist (should not error)
	if err := db.DeleteAllWebAuthnCredentials(ctx, user.ID); err != nil {
		t.Fatalf("DeleteAllWebAuthnCredentials (no-op): %v", err)
	}
}

func TestDeleteAllWebAuthnCredentialsIsolation(t *testing.T) {
	db := setupExtendedTestDB(t)
	ctx := context.Background()

	user1 := createTestUser(t, db, "webauthniso1")
	user2 := createTestUser(t, db, "webauthniso2")

	// Create a credential for each user
	for _, u := range []*User{user1, user2} {
		cred := &WebAuthnCredential{
			UserID:              u.ID,
			CredentialID:        []byte("cred-" + u.Email),
			PublicKey:           []byte("key-" + u.Email),
			AttestationType:     "direct",
			AuthenticatorAAGUID: []byte("aaguid"),
			SignCount:           0,
			Name:                "Key for " + u.Name,
		}
		db.CreateWebAuthnCredential(ctx, cred)
	}

	// Delete all for user1 should not affect user2
	db.DeleteAllWebAuthnCredentials(ctx, user1.ID)

	count1, _ := db.CountUserWebAuthnCredentials(ctx, user1.ID)
	count2, _ := db.CountUserWebAuthnCredentials(ctx, user2.ID)
	if count1 != 0 {
		t.Errorf("user1 should have 0 credentials, got %d", count1)
	}
	if count2 != 1 {
		t.Errorf("user2 should still have 1 credential, got %d", count2)
	}
}

// ── AI Conversation Lifecycle ────────────────────────────────────

func TestAIConversationLifecycle(t *testing.T) {
	db := setupExtendedTestDB(t)
	ctx := context.Background()

	user := createTestUser(t, db, "aiconv")
	org := createTestOrg(t, db, "aiconv")
	proj := createTestProject(t, db, org.ID, "aiconv")

	// Create conversation without project
	conv1, err := db.CreateAIConversation(ctx, user.ID, nil)
	if err != nil {
		t.Fatalf("CreateAIConversation(nil project): %v", err)
	}
	if conv1.ID == "" {
		t.Fatal("conversation ID should not be empty")
	}
	if conv1.Title != "New conversation" {
		t.Errorf("Title = %q, want 'New conversation'", conv1.Title)
	}
	if conv1.ProjectID != nil {
		t.Error("ProjectID should be nil")
	}

	// Create conversation with project
	conv2, err := db.CreateAIConversation(ctx, user.ID, &proj.ID)
	if err != nil {
		t.Fatalf("CreateAIConversation(with project): %v", err)
	}
	if conv2.ProjectID == nil || *conv2.ProjectID != proj.ID {
		t.Error("ProjectID should match project")
	}

	// Get by ID
	fetched, err := db.GetAIConversation(ctx, conv1.ID)
	if err != nil {
		t.Fatalf("GetAIConversation: %v", err)
	}
	if fetched.ID != conv1.ID {
		t.Error("ID mismatch")
	}
	if fetched.UserID != user.ID {
		t.Error("UserID mismatch")
	}

	// Update title
	err = db.UpdateAIConversationTitle(ctx, conv1.ID, "My Chat")
	if err != nil {
		t.Fatalf("UpdateAIConversationTitle: %v", err)
	}
	updated, _ := db.GetAIConversation(ctx, conv1.ID)
	if updated.Title != "My Chat" {
		t.Errorf("Title = %q, want 'My Chat'", updated.Title)
	}

	// Touch conversation (updates updated_at)
	beforeTouch := updated.UpdatedAt
	// Small delay to ensure different timestamp
	time.Sleep(10 * time.Millisecond)
	err = db.TouchAIConversation(ctx, conv1.ID)
	if err != nil {
		t.Fatalf("TouchAIConversation: %v", err)
	}
	touched, _ := db.GetAIConversation(ctx, conv1.ID)
	if !touched.UpdatedAt.After(beforeTouch) {
		t.Error("updated_at should be refreshed after touch")
	}

	// List conversations (ordered by updated_at DESC)
	convs, err := db.ListAIConversations(ctx, user.ID, 10)
	if err != nil {
		t.Fatalf("ListAIConversations: %v", err)
	}
	if len(convs) != 2 {
		t.Fatalf("expected 2 conversations, got %d", len(convs))
	}
	// conv1 was touched more recently, should be first
	if convs[0].ID != conv1.ID {
		t.Error("most recently updated conversation should be first")
	}

	// List with limit
	limited, _ := db.ListAIConversations(ctx, user.ID, 1)
	if len(limited) != 1 {
		t.Fatalf("expected 1 conversation with limit=1, got %d", len(limited))
	}

	// Delete conversation
	err = db.DeleteAIConversation(ctx, conv1.ID)
	if err != nil {
		t.Fatalf("DeleteAIConversation: %v", err)
	}
	_, err = db.GetAIConversation(ctx, conv1.ID)
	if err == nil {
		t.Error("conversation should be deleted")
	}

	// Remaining conversations
	remaining, _ := db.ListAIConversations(ctx, user.ID, 10)
	if len(remaining) != 1 {
		t.Fatalf("expected 1 remaining conversation, got %d", len(remaining))
	}
}

func TestGetLatestAIConversation(t *testing.T) {
	db := setupExtendedTestDB(t)
	ctx := context.Background()

	user := createTestUser(t, db, "ailatest")
	org := createTestOrg(t, db, "ailatest")
	proj := createTestProject(t, db, org.ID, "ailatest")

	// No conversation yet -- should return error
	_, err := db.GetLatestAIConversation(ctx, user.ID, nil)
	if err == nil {
		t.Error("expected error when no conversation exists")
	}

	// Create a conversation without project
	conv, _ := db.CreateAIConversation(ctx, user.ID, nil)

	// Should find it (it was just created, so within 1 hour)
	latest, err := db.GetLatestAIConversation(ctx, user.ID, nil)
	if err != nil {
		t.Fatalf("GetLatestAIConversation: %v", err)
	}
	if latest.ID != conv.ID {
		t.Error("should return the created conversation")
	}

	// With project ID should not find it (different scope)
	_, err = db.GetLatestAIConversation(ctx, user.ID, &proj.ID)
	if err == nil {
		t.Error("should not find null-project conversation when searching with project ID")
	}

	// Create a project-scoped conversation
	projConv, _ := db.CreateAIConversation(ctx, user.ID, &proj.ID)

	// Should find the project-scoped one
	latestProj, err := db.GetLatestAIConversation(ctx, user.ID, &proj.ID)
	if err != nil {
		t.Fatalf("GetLatestAIConversation(project): %v", err)
	}
	if latestProj.ID != projConv.ID {
		t.Error("should return the project-scoped conversation")
	}
}

func TestAIMessages(t *testing.T) {
	db := setupExtendedTestDB(t)
	ctx := context.Background()

	user := createTestUser(t, db, "aimsg")
	conv, _ := db.CreateAIConversation(ctx, user.ID, nil)

	// CreateAIMessage (no user attribution)
	msg1, err := db.CreateAIMessage(ctx, conv.ID, "user", "Hello AI")
	if err != nil {
		t.Fatalf("CreateAIMessage: %v", err)
	}
	if msg1.Role != "user" {
		t.Errorf("Role = %q, want 'user'", msg1.Role)
	}
	if msg1.Content != "Hello AI" {
		t.Errorf("Content mismatch")
	}
	if msg1.UserID != nil {
		t.Error("UserID should be nil for basic CreateAIMessage")
	}
	if msg1.UserName != "" {
		t.Errorf("UserName should be empty, got %q", msg1.UserName)
	}

	// CreateAIMessage (assistant)
	msg2, err := db.CreateAIMessage(ctx, conv.ID, "assistant", "Hello human!")
	if err != nil {
		t.Fatalf("CreateAIMessage(assistant): %v", err)
	}
	if msg2.Role != "assistant" {
		t.Errorf("Role = %q, want 'assistant'", msg2.Role)
	}

	// CreateAIMessageWithUser
	msg3, err := db.CreateAIMessageWithUser(ctx, conv.ID, "user", "Message with user", &user.ID, "AI Msg User")
	if err != nil {
		t.Fatalf("CreateAIMessageWithUser: %v", err)
	}
	if msg3.UserID == nil || *msg3.UserID != user.ID {
		t.Error("UserID should be set")
	}
	if msg3.UserName != "AI Msg User" {
		t.Errorf("UserName = %q, want 'AI Msg User'", msg3.UserName)
	}

	// ListAIMessages
	msgs, err := db.ListAIMessages(ctx, conv.ID)
	if err != nil {
		t.Fatalf("ListAIMessages: %v", err)
	}
	if len(msgs) != 3 {
		t.Fatalf("expected 3 messages, got %d", len(msgs))
	}
	// Should be in creation order
	if msgs[0].Content != "Hello AI" {
		t.Error("first message should be 'Hello AI'")
	}
	if msgs[1].Content != "Hello human!" {
		t.Error("second message should be 'Hello human!'")
	}
	if msgs[2].Content != "Message with user" {
		t.Error("third message should be 'Message with user'")
	}
}

func TestAIMessagesDeletedWithConversation(t *testing.T) {
	db := setupExtendedTestDB(t)
	ctx := context.Background()

	user := createTestUser(t, db, "aimsgdel")
	conv, _ := db.CreateAIConversation(ctx, user.ID, nil)

	db.CreateAIMessage(ctx, conv.ID, "user", "Message 1")
	db.CreateAIMessage(ctx, conv.ID, "assistant", "Reply 1")

	// Delete conversation -- messages should cascade
	db.DeleteAIConversation(ctx, conv.ID)

	msgs, err := db.ListAIMessages(ctx, conv.ID)
	if err != nil {
		t.Fatalf("ListAIMessages after delete: %v", err)
	}
	if len(msgs) != 0 {
		t.Errorf("expected 0 messages after conversation delete, got %d", len(msgs))
	}
}

func TestGetOrCreateProjectConversation(t *testing.T) {
	db := setupExtendedTestDB(t)
	ctx := context.Background()

	user := createTestUser(t, db, "projconv")
	org := createTestOrg(t, db, "projconv")
	proj := createTestProject(t, db, org.ID, "projconv")

	// First call creates a new conversation
	conv1, err := db.GetOrCreateProjectConversation(ctx, proj.ID, user.ID)
	if err != nil {
		t.Fatalf("GetOrCreateProjectConversation: %v", err)
	}
	if conv1.ID == "" {
		t.Fatal("conversation ID should not be empty")
	}
	if conv1.ProjectID == nil || *conv1.ProjectID != proj.ID {
		t.Error("ProjectID should match")
	}

	// Second call with same project should return the same conversation (upsert)
	conv2, err := db.GetOrCreateProjectConversation(ctx, proj.ID, user.ID)
	if err != nil {
		t.Fatalf("GetOrCreateProjectConversation (second): %v", err)
	}
	if conv2.ID != conv1.ID {
		t.Errorf("expected same conversation ID, got %q vs %q", conv2.ID, conv1.ID)
	}

	// Different project should create a new one
	proj2 := createTestProject(t, db, org.ID, "projconv2")
	conv3, err := db.GetOrCreateProjectConversation(ctx, proj2.ID, user.ID)
	if err != nil {
		t.Fatalf("GetOrCreateProjectConversation (proj2): %v", err)
	}
	if conv3.ID == conv1.ID {
		t.Error("different project should create a different conversation")
	}
}

// ── SearchTickets ────────────────────────────────────────────────

func TestSearchTickets(t *testing.T) {
	db := setupExtendedTestDB(t)
	ctx := context.Background()

	user := createTestUser(t, db, "search")
	org := createTestOrg(t, db, "search")
	proj := createTestProject(t, db, org.ID, "search")

	// Create tickets with varied titles and descriptions
	t1 := createTestTicket(t, db, proj.ID, user.ID, "feature", "User Authentication")
	t1.DescriptionMarkdown = "Implement login and signup"
	db.UpdateTicket(ctx, t1)

	createTestTicket(t, db, proj.ID, user.ID, "bug", "Login page crash")
	createTestTicket(t, db, proj.ID, user.ID, "task", "Write documentation")
	createTestTicket(t, db, proj.ID, user.ID, "bug", "Dashboard timeout")

	// Search by title
	results, err := db.SearchTickets(ctx, proj.ID, "login", nil, nil)
	if err != nil {
		t.Fatalf("SearchTickets: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 results for 'login', got %d", len(results))
	}

	// Search by description
	results, err = db.SearchTickets(ctx, proj.ID, "signup", nil, nil)
	if err != nil {
		t.Fatalf("SearchTickets(signup): %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result for 'signup', got %d", len(results))
	}

	// Search with type filter
	bugType := "bug"
	results, err = db.SearchTickets(ctx, proj.ID, "login", &bugType, nil)
	if err != nil {
		t.Fatalf("SearchTickets(type=bug): %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 bug matching 'login', got %d", len(results))
	}
	if results[0].Type != "bug" {
		t.Errorf("expected type 'bug', got %q", results[0].Type)
	}

	// Search with status filter
	backlogStatus := "backlog"
	results, err = db.SearchTickets(ctx, proj.ID, "login", nil, &backlogStatus)
	if err != nil {
		t.Fatalf("SearchTickets(status=backlog): %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 backlog results for 'login', got %d", len(results))
	}

	// Search with both type and status filter
	results, err = db.SearchTickets(ctx, proj.ID, "login", &bugType, &backlogStatus)
	if err != nil {
		t.Fatalf("SearchTickets(type+status): %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result for type=bug + status=backlog, got %d", len(results))
	}

	// Search with no matches
	results, err = db.SearchTickets(ctx, proj.ID, "nonexistent-query-xyz", nil, nil)
	if err != nil {
		t.Fatalf("SearchTickets(no match): %v", err)
	}
	if len(results) != 0 {
		t.Fatalf("expected 0 results, got %d", len(results))
	}

	// Case insensitive search
	results, err = db.SearchTickets(ctx, proj.ID, "LOGIN", nil, nil)
	if err != nil {
		t.Fatalf("SearchTickets(case): %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 results for case-insensitive 'LOGIN', got %d", len(results))
	}
}

func TestSearchTicketsExcludesArchived(t *testing.T) {
	db := setupExtendedTestDB(t)
	ctx := context.Background()

	user := createTestUser(t, db, "searcharch")
	org := createTestOrg(t, db, "searcharch")
	proj := createTestProject(t, db, org.ID, "searcharch")

	ticket := createTestTicket(t, db, proj.ID, user.ID, "task", "Searchable Task")

	// Archive it
	db.ArchiveTicket(ctx, ticket.ID)

	// Search should not find it
	results, _ := db.SearchTickets(ctx, proj.ID, "Searchable", nil, nil)
	if len(results) != 0 {
		t.Errorf("archived tickets should be excluded from search, got %d results", len(results))
	}
}

// ── Project Costs ────────────────────────────────────────────────

func TestProjectCosts(t *testing.T) {
	db := setupExtendedTestDB(t)
	ctx := context.Background()

	org := createTestOrg(t, db, "costs")
	proj := createTestProject(t, db, org.ID, "costs")

	// Create a cost
	cost, err := db.CreateProjectCost(ctx, proj.ID, "2026-02", "base_fee", "Monthly base", 5000)
	if err != nil {
		t.Fatalf("CreateProjectCost: %v", err)
	}
	if cost.ID == "" {
		t.Fatal("cost ID should not be empty")
	}
	if cost.ProjectID != proj.ID {
		t.Error("ProjectID mismatch")
	}
	if cost.Month != "2026-02" {
		t.Errorf("Month = %q, want '2026-02'", cost.Month)
	}
	if cost.Category != "base_fee" {
		t.Errorf("Category = %q, want 'base_fee'", cost.Category)
	}
	if cost.Name != "Monthly base" {
		t.Errorf("Name = %q, want 'Monthly base'", cost.Name)
	}
	if cost.AmountCents != 5000 {
		t.Errorf("AmountCents = %d, want 5000", cost.AmountCents)
	}

	// Get by ID
	fetched, err := db.GetProjectCost(ctx, cost.ID)
	if err != nil {
		t.Fatalf("GetProjectCost: %v", err)
	}
	if fetched.AmountCents != 5000 {
		t.Errorf("fetched AmountCents = %d, want 5000", fetched.AmountCents)
	}

	// Create more costs in the same month
	db.CreateProjectCost(ctx, proj.ID, "2026-02", "dev_environment", "Dev server", 2000)
	db.CreateProjectCost(ctx, proj.ID, "2026-03", "base_fee", "March base", 5000)

	// List by month
	febCosts, err := db.ListProjectCosts(ctx, proj.ID, "2026-02")
	if err != nil {
		t.Fatalf("ListProjectCosts: %v", err)
	}
	if len(febCosts) != 2 {
		t.Fatalf("expected 2 costs for 2026-02, got %d", len(febCosts))
	}

	marCosts, _ := db.ListProjectCosts(ctx, proj.ID, "2026-03")
	if len(marCosts) != 1 {
		t.Fatalf("expected 1 cost for 2026-03, got %d", len(marCosts))
	}

	// Update amount
	err = db.UpdateProjectCost(ctx, cost.ID, 7500)
	if err != nil {
		t.Fatalf("UpdateProjectCost: %v", err)
	}
	updated, _ := db.GetProjectCost(ctx, cost.ID)
	if updated.AmountCents != 7500 {
		t.Errorf("AmountCents = %d, want 7500", updated.AmountCents)
	}

	// Delete
	err = db.DeleteProjectCost(ctx, cost.ID)
	if err != nil {
		t.Fatalf("DeleteProjectCost: %v", err)
	}
	_, err = db.GetProjectCost(ctx, cost.ID)
	if err == nil {
		t.Error("cost should be deleted")
	}

	// Remaining costs for that month
	afterDelete, _ := db.ListProjectCosts(ctx, proj.ID, "2026-02")
	if len(afterDelete) != 1 {
		t.Fatalf("expected 1 cost remaining for 2026-02, got %d", len(afterDelete))
	}
}

func TestProjectCostsEmpty(t *testing.T) {
	db := setupExtendedTestDB(t)
	ctx := context.Background()

	org := createTestOrg(t, db, "costsempty")
	proj := createTestProject(t, db, org.ID, "costsempty")

	costs, err := db.ListProjectCosts(ctx, proj.ID, "2026-01")
	if err != nil {
		t.Fatalf("ListProjectCosts: %v", err)
	}
	if len(costs) != 0 {
		t.Errorf("expected 0 costs, got %d", len(costs))
	}
}

// ── Reactions ────────────────────────────────────────────────────

func TestReactions(t *testing.T) {
	db := setupExtendedTestDB(t)
	ctx := context.Background()

	user1 := createTestUser(t, db, "react1")
	user2 := createTestUser(t, db, "react2")
	org := createTestOrg(t, db, "react")
	proj := createTestProject(t, db, org.ID, "react")
	ticket := createTestTicket(t, db, proj.ID, user1.ID, "task", "Reacted Ticket")

	// Toggle reaction on (add)
	added, err := db.ToggleReaction(ctx, "ticket", ticket.ID, user1.ID, "thumbsup")
	if err != nil {
		t.Fatalf("ToggleReaction(add): %v", err)
	}
	if !added {
		t.Error("first toggle should add (return true)")
	}

	// Toggle same reaction again (remove)
	added, err = db.ToggleReaction(ctx, "ticket", ticket.ID, user1.ID, "thumbsup")
	if err != nil {
		t.Fatalf("ToggleReaction(remove): %v", err)
	}
	if added {
		t.Error("second toggle should remove (return false)")
	}

	// Add reactions from multiple users with different emojis
	db.ToggleReaction(ctx, "ticket", ticket.ID, user1.ID, "thumbsup")
	db.ToggleReaction(ctx, "ticket", ticket.ID, user2.ID, "thumbsup")
	db.ToggleReaction(ctx, "ticket", ticket.ID, user1.ID, "heart")

	// ListReactionGroups
	groups, err := db.ListReactionGroups(ctx, "ticket", ticket.ID, user1.ID)
	if err != nil {
		t.Fatalf("ListReactionGroups: %v", err)
	}
	if len(groups) != 2 {
		t.Fatalf("expected 2 reaction groups, got %d", len(groups))
	}

	// Find the thumbsup group
	var thumbsup, heart *ReactionGroup
	for i := range groups {
		switch groups[i].Emoji {
		case "thumbsup":
			thumbsup = &groups[i]
		case "heart":
			heart = &groups[i]
		}
	}

	if thumbsup == nil {
		t.Fatal("thumbsup group not found")
	}
	if thumbsup.Count != 2 {
		t.Errorf("thumbsup count = %d, want 2", thumbsup.Count)
	}
	if !thumbsup.UserReacted {
		t.Error("user1 should be marked as having reacted with thumbsup")
	}

	if heart == nil {
		t.Fatal("heart group not found")
	}
	if heart.Count != 1 {
		t.Errorf("heart count = %d, want 1", heart.Count)
	}
	if !heart.UserReacted {
		t.Error("user1 should be marked as having reacted with heart")
	}

	// ListReactionGroups from user2's perspective
	groups2, _ := db.ListReactionGroups(ctx, "ticket", ticket.ID, user2.ID)
	for _, g := range groups2 {
		if g.Emoji == "thumbsup" && !g.UserReacted {
			t.Error("user2 should be marked as having reacted with thumbsup")
		}
		if g.Emoji == "heart" && g.UserReacted {
			t.Error("user2 should NOT be marked as having reacted with heart")
		}
	}
}

func TestReactionsOnComment(t *testing.T) {
	db := setupExtendedTestDB(t)
	ctx := context.Background()

	user := createTestUser(t, db, "reactcomm")
	org := createTestOrg(t, db, "reactcomm")
	proj := createTestProject(t, db, org.ID, "reactcomm")
	ticket := createTestTicket(t, db, proj.ID, user.ID, "task", "Commented Ticket")
	comment, _ := db.CreateComment(ctx, ticket.ID, &user.ID, nil, "A comment")

	// React to comment
	added, err := db.ToggleReaction(ctx, "comment", comment.ID, user.ID, "fire")
	if err != nil {
		t.Fatalf("ToggleReaction(comment): %v", err)
	}
	if !added {
		t.Error("should add reaction")
	}

	groups, _ := db.ListReactionGroups(ctx, "comment", comment.ID, user.ID)
	if len(groups) != 1 {
		t.Fatalf("expected 1 reaction group on comment, got %d", len(groups))
	}
	if groups[0].Emoji != "fire" {
		t.Errorf("Emoji = %q, want 'fire'", groups[0].Emoji)
	}
}

func TestReactionGroupsBatch(t *testing.T) {
	db := setupExtendedTestDB(t)
	ctx := context.Background()

	user := createTestUser(t, db, "reactbatch")
	org := createTestOrg(t, db, "reactbatch")
	proj := createTestProject(t, db, org.ID, "reactbatch")

	ticket1 := createTestTicket(t, db, proj.ID, user.ID, "task", "Batch Ticket 1")
	ticket2 := createTestTicket(t, db, proj.ID, user.ID, "task", "Batch Ticket 2")
	ticket3 := createTestTicket(t, db, proj.ID, user.ID, "task", "Batch Ticket 3")

	// Add reactions to ticket1 and ticket2 only
	db.ToggleReaction(ctx, "ticket", ticket1.ID, user.ID, "thumbsup")
	db.ToggleReaction(ctx, "ticket", ticket1.ID, user.ID, "heart")
	db.ToggleReaction(ctx, "ticket", ticket2.ID, user.ID, "fire")

	// Batch query
	result, err := db.ListReactionGroupsBatch(ctx, "ticket", []string{ticket1.ID, ticket2.ID, ticket3.ID}, user.ID)
	if err != nil {
		t.Fatalf("ListReactionGroupsBatch: %v", err)
	}

	// ticket1 should have 2 reaction groups
	if len(result[ticket1.ID]) != 2 {
		t.Errorf("ticket1: expected 2 reaction groups, got %d", len(result[ticket1.ID]))
	}

	// ticket2 should have 1 reaction group
	if len(result[ticket2.ID]) != 1 {
		t.Errorf("ticket2: expected 1 reaction group, got %d", len(result[ticket2.ID]))
	}

	// ticket3 should have 0 reaction groups (not in result map)
	if len(result[ticket3.ID]) != 0 {
		t.Errorf("ticket3: expected 0 reaction groups, got %d", len(result[ticket3.ID]))
	}
}

func TestReactionGroupsBatchEmpty(t *testing.T) {
	db := setupExtendedTestDB(t)
	ctx := context.Background()

	user := createTestUser(t, db, "reactbatchmt")

	// Empty target IDs
	result, err := db.ListReactionGroupsBatch(ctx, "ticket", []string{}, user.ID)
	if err != nil {
		t.Fatalf("ListReactionGroupsBatch(empty): %v", err)
	}
	if len(result) != 0 {
		t.Errorf("expected empty result map, got %d entries", len(result))
	}
}
