package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/madalin/forgedesk/internal/testutil"
)

func TestSessionLifecycle(t *testing.T) {
	pool := testutil.SetupTestDB(t)
	store := NewSessionStore(pool)
	ctx := context.Background()

	// Create a user first
	var userID string
	err := pool.QueryRow(ctx,
		`INSERT INTO users (email, password_hash, name, role) VALUES ('test@test.com', 'hash', 'Test', 'client') RETURNING id`).Scan(&userID)
	if err != nil {
		t.Fatalf("inserting user: %v", err)
	}

	// Create session
	req := httptest.NewRequest(http.MethodPost, "/login", http.NoBody)
	req.Header.Set("User-Agent", "test-agent")
	token, err := store.CreateSession(ctx, userID, true, req)
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if token == "" {
		t.Fatal("token should not be empty")
	}

	// Get session
	sess, err := store.GetSession(ctx, token)
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if sess.UserID != userID {
		t.Errorf("UserID = %q, want %s", sess.UserID, userID)
	}
	if !sess.MustSetup2FA {
		t.Error("MustSetup2FA should be true")
	}
	if sess.TwoFactorVerified {
		t.Error("TwoFactorVerified should be false initially")
	}

	// Mark 2FA verified
	err = store.MarkTwoFactorVerified(ctx, sess.ID)
	if err != nil {
		t.Fatalf("MarkTwoFactorVerified: %v", err)
	}

	sess2, _ := store.GetSession(ctx, token)
	if !sess2.TwoFactorVerified {
		t.Error("TwoFactorVerified should be true after marking")
	}

	// Mark 2FA setup complete
	err = store.Mark2FASetupComplete(ctx, sess.ID)
	if err != nil {
		t.Fatalf("Mark2FASetupComplete: %v", err)
	}
	sess3, _ := store.GetSession(ctx, token)
	if sess3.MustSetup2FA {
		t.Error("MustSetup2FA should be false after setup complete")
	}

	// Extend session
	err = store.ExtendSession(ctx, sess.ID)
	if err != nil {
		t.Fatalf("ExtendSession: %v", err)
	}

	// Delete session
	err = store.DeleteSession(ctx, sess.ID)
	if err != nil {
		t.Fatalf("DeleteSession: %v", err)
	}

	_, err = store.GetSession(ctx, token)
	if err == nil {
		t.Fatal("GetSession should fail after deletion")
	}
}

func TestDeleteAllUserSessions(t *testing.T) {
	pool := testutil.SetupTestDB(t)
	store := NewSessionStore(pool)
	ctx := context.Background()

	var userID string
	err := pool.QueryRow(ctx,
		`INSERT INTO users (email, password_hash, name, role) VALUES ('multi@test.com', 'hash', 'Multi', 'client') RETURNING id`).Scan(&userID)
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodPost, "/", http.NoBody)
	t1, _ := store.CreateSession(ctx, userID, false, req)
	t2, _ := store.CreateSession(ctx, userID, false, req)

	// Both sessions should exist
	if _, err := store.GetSession(ctx, t1); err != nil {
		t.Fatal("session 1 should exist")
	}
	if _, err := store.GetSession(ctx, t2); err != nil {
		t.Fatal("session 2 should exist")
	}

	if err := store.DeleteAllUserSessions(ctx, userID); err != nil {
		t.Fatalf("DeleteAllUserSessions: %v", err)
	}

	if _, err := store.GetSession(ctx, t1); err == nil {
		t.Fatal("session 1 should be deleted")
	}
	if _, err := store.GetSession(ctx, t2); err == nil {
		t.Fatal("session 2 should be deleted")
	}
}

func TestDeleteOtherSessions(t *testing.T) {
	pool := testutil.SetupTestDB(t)
	store := NewSessionStore(pool)
	ctx := context.Background()

	var userID string
	err := pool.QueryRow(ctx,
		`INSERT INTO users (email, password_hash, name, role) VALUES ('other@test.com', 'hash', 'Other', 'client') RETURNING id`).Scan(&userID)
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodPost, "/", http.NoBody)
	t1, _ := store.CreateSession(ctx, userID, false, req)
	t2, _ := store.CreateSession(ctx, userID, false, req)

	sess1, _ := store.GetSession(ctx, t1)

	if err := store.DeleteOtherSessions(ctx, userID, sess1.ID); err != nil {
		t.Fatal(err)
	}

	// Session 1 should still exist
	if _, err := store.GetSession(ctx, t1); err != nil {
		t.Fatal("kept session should still exist")
	}

	// Session 2 should be gone
	if _, err := store.GetSession(ctx, t2); err == nil {
		t.Fatal("other session should be deleted")
	}
}

func TestGetSessionInvalidToken(t *testing.T) {
	pool := testutil.SetupTestDB(t)
	store := NewSessionStore(pool)

	_, err := store.GetSession(context.Background(), "nonexistent-token")
	if err == nil {
		t.Fatal("expected error for invalid token")
	}
}
