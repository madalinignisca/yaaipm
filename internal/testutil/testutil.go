// Package testutil provides shared test helpers for integration tests.
package testutil

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	TestDBURL  = "postgres://testuser:testpass@localhost:5433/forgedesk_test?sslmode=disable"
	TestAESKey = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	TestSecret = "test-session-secret-32-chars-long!"
)

// SetupTestDB connects to the test database and cleans all data.
// Skips the test if the test database is not reachable.
func SetupTestDB(t *testing.T) *pgxpool.Pool {
	t.Helper()

	dbURL := os.Getenv("TEST_DATABASE_URL")
	if dbURL == "" {
		dbURL = TestDBURL
	}

	pool, err := pgxpool.New(context.Background(), dbURL)
	if err != nil {
		t.Skipf("skipping integration test: cannot connect to test DB: %v", err)
	}

	if err := pool.Ping(context.Background()); err != nil {
		pool.Close()
		t.Skipf("skipping integration test: cannot ping test DB: %v", err)
	}

	// Clean all tables (order matters due to foreign keys)
	tables := []string{
		"reactions",
		"ai_messages",
		"ai_conversations",
		"ai_usage_entries",
		"project_costs",
		"ticket_activities",
		"comments",
		"tickets",
		"projects",
		"invitations",
		"org_memberships",
		"organizations",
		"webauthn_credentials",
		"sessions",
		"users",
	}
	for _, table := range tables {
		_, err := pool.Exec(context.Background(), fmt.Sprintf("DELETE FROM %s", table))
		if err != nil {
			pool.Close()
			t.Fatalf("cleaning table %s: %v", table, err)
		}
	}

	t.Cleanup(func() {
		pool.Close()
	})

	return pool
}

// ProjectRoot returns the absolute path to the project root directory.
func ProjectRoot() string {
	_, filename, _, _ := runtime.Caller(0) //nolint:dogsled // standard runtime.Caller idiom
	return filepath.Join(filepath.Dir(filename), "..", "..")
}
