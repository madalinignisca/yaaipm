// Package testutil provides shared test helpers for integration tests.
package testutil

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	TestDBURL  = "postgres://testuser:testpass@localhost:5433/forgedesk_test?sslmode=disable"
	TestAESKey = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	TestSecret = "test-session-secret-32-chars-long!"
)

// requireTestDB reports whether an unreachable test database must FAIL the
// test rather than skip it.
//
// Skipping is the right default for a developer with no Postgres running. It is
// the wrong behavior in CI, where it makes the entire integration suite report
// `ok` and exit 0 while executing nothing — indistinguishable from a working
// run (#138). So CI declares the database as a precondition by setting
// REQUIRE_TEST_DB, and a missing database becomes a hard failure.
//
// This is the one legitimate shape for a runtime skip: an explicitly declared
// opt-in, not an inference from "the thing under test appears to be missing".
func requireTestDB() bool {
	v := strings.TrimSpace(os.Getenv("REQUIRE_TEST_DB"))
	return v != "" && v != "0" && !strings.EqualFold(v, "false")
}

// SetupTestDB connects to the test database and cleans all data.
//
// Skips the test if the test database is not reachable — unless REQUIRE_TEST_DB
// is set, in which case it fails. See requireTestDB.
func SetupTestDB(t *testing.T) *pgxpool.Pool {
	t.Helper()

	dbURL := os.Getenv("TEST_DATABASE_URL")
	if dbURL == "" {
		dbURL = TestDBURL
	}

	unreachable := func(format string, args ...any) {
		t.Helper()
		if requireTestDB() {
			t.Fatalf("REQUIRE_TEST_DB is set: "+format, args...)
		}
		t.Skipf("skipping integration test: "+format, args...)
	}

	pool, err := pgxpool.New(context.Background(), dbURL)
	if err != nil {
		unreachable("cannot connect to test DB: %v", err)
		return nil
	}

	if err := pool.Ping(context.Background()); err != nil {
		pool.Close()
		unreachable("cannot ping test DB: %v", err)
		return nil
	}

	// Clean all tables (order matters due to foreign keys).
	// Child tables listed before parents: even though CASCADE would handle
	// most chains, explicit deletion keeps cleanup deterministic and makes
	// FK-dependency changes obvious in diffs.
	tables := []string{
		"reactions",
		"ai_messages",
		"ai_conversations",
		"ai_usage_entries",
		"project_costs",
		"ticket_activities",
		"comments",
		// Appended by #129. Listed explicitly even though its org_id FK is
		// ON DELETE CASCADE, matching this list's children-first convention:
		// if that FK is ever relaxed, spend rows would otherwise start
		// leaking between tests silently.
		"debate_spend",
		// Debate rounds reference debates; debates reference tickets + users
		// (ON DELETE RESTRICT on started_by/triggered_by), so purge rounds
		// first, then debates, then the rest.
		"feature_debate_rounds",
		"feature_debates",
		"brief_revisions",
		"tickets",
		"projects",
		"invitations",
		"org_memberships",
		// References organizations; must go before it even though the FK
		// is ON DELETE CASCADE, to match this list's "children first"
		// convention (#64).
		"org_budget_changes",
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
