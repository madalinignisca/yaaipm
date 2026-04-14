package models

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/madalin/forgedesk/internal/testutil"
)

// seedFeatureTicket sets up a fresh org → project → user → feature ticket
// chain and returns the four IDs needed by debate tests. Inlined here rather
// than added to testutil because it's only used by debate-mode tests so far.
func seedFeatureTicket(t *testing.T, db *DB, description string) (orgID, userID, projectID, ticketID string) {
	t.Helper()
	ctx := context.Background()

	user, err := db.CreateUser(ctx, t.Name()+"-debate@example.com",
		"$argon2id$v=19$m=65536,t=3,p=4$dGVzdHNhbHQ$dGVzdGhhc2g",
		"Debate Test User", "client")
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	org, err := db.CreateOrgWithOwnerTx(ctx, user.ID, "Debate Org "+t.Name(), "debate-org-"+t.Name(), OrgRoleOwner)
	if err != nil {
		t.Fatalf("CreateOrgWithOwnerTx: %v", err)
	}
	proj, err := db.CreateProject(ctx, org.ID, "Debate Project", "debate-proj-"+t.Name())
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	tk := &Ticket{
		ProjectID:           proj.ID,
		Type:                "feature",
		Title:               "Debate test feature",
		DescriptionMarkdown: description,
		Status:              "backlog",
		Priority:            "medium",
		CreatedBy:           user.ID,
	}
	if err := db.CreateTicket(ctx, tk); err != nil {
		t.Fatalf("CreateTicket: %v", err)
	}
	return org.ID, user.ID, proj.ID, tk.ID
}

func TestStartDebate_FreshTicketCreatesActiveRow(t *testing.T) {
	pool := testutil.SetupTestDB(t)
	db := NewDB(pool)
	ctx := context.Background()

	orgID, userID, projectID, ticketID := seedFeatureTicket(t, db, "Initial description")

	deb, err := db.StartDebate(ctx, ticketID, projectID, orgID, userID)
	if err != nil {
		t.Fatalf("StartDebate: %v", err)
	}
	if deb.Status != "active" {
		t.Errorf("Status = %q, want active", deb.Status)
	}
	if deb.SeedDescription != "Initial description" {
		t.Errorf("SeedDescription = %q, want %q", deb.SeedDescription, "Initial description")
	}
	if deb.CurrentText != "Initial description" {
		t.Errorf("CurrentText = %q, want %q", deb.CurrentText, "Initial description")
	}
	if deb.OriginalTicketDescription != "Initial description" {
		t.Errorf("OriginalTicketDescription = %q", deb.OriginalTicketDescription)
	}
	if deb.TotalCostMicros != 0 {
		t.Errorf("TotalCostMicros = %d, want 0", deb.TotalCostMicros)
	}
	if deb.InFlightRequestID != nil {
		t.Errorf("InFlightRequestID should start nil, got %v", *deb.InFlightRequestID)
	}
	if deb.LastScoredRoundID != nil {
		t.Errorf("LastScoredRoundID should start nil")
	}
}

func TestStartDebate_ConcurrentCallsAreIdempotent(t *testing.T) {
	pool := testutil.SetupTestDB(t)
	db := NewDB(pool)
	ctx := context.Background()

	orgID, userID, projectID, ticketID := seedFeatureTicket(t, db, "desc")

	type res struct {
		deb *FeatureDebate
		err error
	}
	results := make(chan res, 2)
	for range 2 {
		go func() {
			d, err := db.StartDebate(ctx, ticketID, projectID, orgID, userID)
			results <- res{d, err}
		}()
	}
	a := <-results
	b := <-results
	if a.err != nil {
		t.Fatalf("first StartDebate: %v", a.err)
	}
	if b.err != nil {
		t.Fatalf("second StartDebate: %v", b.err)
	}
	if a.deb.ID != b.deb.ID {
		t.Errorf("IDs differ: %s vs %s — concurrent calls must be idempotent", a.deb.ID, b.deb.ID)
	}

	var count int
	if err := db.Pool.QueryRow(ctx,
		`SELECT count(*) FROM feature_debates WHERE ticket_id = $1 AND status = 'active'`,
		ticketID,
	).Scan(&count); err != nil {
		t.Fatalf("counting active debates: %v", err)
	}
	if count != 1 {
		t.Errorf("active debate count = %d, want 1", count)
	}
}

func TestFeatureDebates_OneActivePerTicketEnforced(t *testing.T) {
	pool := testutil.SetupTestDB(t)
	db := NewDB(pool)
	ctx := context.Background()

	orgID, userID, projectID, ticketID := seedFeatureTicket(t, db, "d")

	if _, err := db.StartDebate(ctx, ticketID, projectID, orgID, userID); err != nil {
		t.Fatalf("first StartDebate: %v", err)
	}

	// A raw INSERT bypassing the ON CONFLICT clause must still fail because
	// of the partial unique index. This proves the invariant is enforced
	// at the DB layer (not just by our query helper).
	_, err := db.Pool.Exec(ctx, `
		INSERT INTO feature_debates (ticket_id, project_id, org_id, started_by, status,
			seed_description, current_text, original_ticket_description, total_cost_micros)
		VALUES ($1, $2, $3, $4, 'active', 'x', 'x', 'x', 0)`,
		ticketID, projectID, orgID, userID)
	if err == nil {
		t.Fatal("expected a unique-violation when inserting a second active debate")
	}
}

func TestGetActiveDebate_ReturnsErrNoRowsWhenAbsent(t *testing.T) {
	pool := testutil.SetupTestDB(t)
	db := NewDB(pool)
	ctx := context.Background()

	_, _, _, ticketID := seedFeatureTicket(t, db, "d") //nolint:dogsled // we only need ticketID here

	_, err := db.GetActiveDebate(ctx, ticketID)
	if !errors.Is(err, pgx.ErrNoRows) {
		t.Errorf("expected pgx.ErrNoRows for ticket with no debate, got %v", err)
	}
}

func TestIsDebateActive_BothCases(t *testing.T) {
	pool := testutil.SetupTestDB(t)
	db := NewDB(pool)
	ctx := context.Background()

	orgID, userID, projectID, ticketID := seedFeatureTicket(t, db, "d")

	active, err := db.IsDebateActive(ctx, ticketID)
	if err != nil {
		t.Fatalf("IsDebateActive (no debate): %v", err)
	}
	if active {
		t.Error("IsDebateActive should be false before StartDebate")
	}

	if _, startErr := db.StartDebate(ctx, ticketID, projectID, orgID, userID); startErr != nil {
		t.Fatalf("StartDebate: %v", startErr)
	}
	active, err = db.IsDebateActive(ctx, ticketID)
	if err != nil {
		t.Fatalf("IsDebateActive (after start): %v", err)
	}
	if !active {
		t.Error("IsDebateActive should be true after StartDebate")
	}
}

func TestCountUserRoundsLast24h_ZeroForNewUser(t *testing.T) {
	pool := testutil.SetupTestDB(t)
	db := NewDB(pool)
	ctx := context.Background()

	_, userID, _, _ := seedFeatureTicket(t, db, "d") //nolint:dogsled // only userID needed

	n, err := db.CountUserRoundsLast24h(ctx, userID)
	if err != nil {
		t.Fatalf("CountUserRoundsLast24h: %v", err)
	}
	if n != 0 {
		t.Errorf("count = %d, want 0", n)
	}
}
