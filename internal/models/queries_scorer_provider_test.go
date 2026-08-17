package models

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/madalin/forgedesk/internal/testutil"
)

// Issue #63: projects.scorer_provider selects which AI provider scores a
// project's debates. Every project read path must round-trip it — a missed
// Scan target is a runtime error, not a compile error, so all four are
// asserted here rather than trusting the shared column list.
func TestProjectScorerProvider_DefaultsAndRoundTrips(t *testing.T) {
	pool := testutil.SetupTestDB(t)
	db := NewDB(pool)
	ctx := context.Background()

	org, _ := db.CreateOrg(ctx, "Scorer Org", "scorer-org")
	proj, err := db.CreateProject(ctx, org.ID, "Scorer Project", "scorer-proj")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}

	// Existing behavior must be preserved: v1 always scored with Gemini,
	// so a project nobody has configured keeps doing exactly that.
	if proj.ScorerProvider != "gemini" {
		t.Errorf("CreateProject ScorerProvider = %q, want gemini", proj.ScorerProvider)
	}

	if err := db.UpdateProjectScorerProvider(ctx, proj.ID, "openai"); err != nil {
		t.Fatalf("UpdateProjectScorerProvider: %v", err)
	}

	t.Run("GetProject", func(t *testing.T) {
		got, err := db.GetProject(ctx, org.ID, "scorer-proj")
		if err != nil {
			t.Fatalf("GetProject: %v", err)
		}
		if got.ScorerProvider != "openai" {
			t.Errorf("ScorerProvider = %q, want openai", got.ScorerProvider)
		}
	})

	t.Run("GetProjectByID", func(t *testing.T) {
		got, err := db.GetProjectByID(ctx, proj.ID)
		if err != nil {
			t.Fatalf("GetProjectByID: %v", err)
		}
		if got.ScorerProvider != "openai" {
			t.Errorf("ScorerProvider = %q, want openai", got.ScorerProvider)
		}
	})

	t.Run("ListProjects", func(t *testing.T) {
		projects, err := db.ListProjects(ctx, org.ID)
		if err != nil {
			t.Fatalf("ListProjects: %v", err)
		}
		if len(projects) != 1 {
			t.Fatalf("expected 1 project, got %d", len(projects))
		}
		if projects[0].ScorerProvider != "openai" {
			t.Errorf("ScorerProvider = %q, want openai", projects[0].ScorerProvider)
		}
	})

	t.Run("GetProjectScorerProvider", func(t *testing.T) {
		got, err := db.GetProjectScorerProvider(ctx, proj.ID)
		if err != nil {
			t.Fatalf("GetProjectScorerProvider: %v", err)
		}
		if got != "openai" {
			t.Errorf("provider = %q, want openai", got)
		}
	})
}

// The accept path resolves the provider by project ID in a background
// goroutine; a deleted project must surface as ErrNoRows rather than an
// empty provider string that would silently look like "unconfigured".
func TestGetProjectScorerProvider_UnknownProject(t *testing.T) {
	pool := testutil.SetupTestDB(t)
	db := NewDB(pool)
	ctx := context.Background()

	_, err := db.GetProjectScorerProvider(ctx, "00000000-0000-0000-0000-000000000000")
	if err == nil {
		t.Fatal("expected an error for an unknown project")
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		t.Errorf("error should wrap pgx.ErrNoRows, got: %v", err)
	}
}

// Cheap proof the CHECK constraint from migration 000034 is actually live:
// the handler validates provider names before writing, but the constraint
// is the backstop if a future call site forgets.
func TestUpdateProjectScorerProvider_RejectsUnknownProvider(t *testing.T) {
	pool := testutil.SetupTestDB(t)
	db := NewDB(pool)
	ctx := context.Background()

	org, _ := db.CreateOrg(ctx, "Check Org", "check-org")
	proj, err := db.CreateProject(ctx, org.ID, "Check Project", "check-proj")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}

	if updErr := db.UpdateProjectScorerProvider(ctx, proj.ID, "llama"); updErr == nil {
		t.Fatal("expected the CHECK constraint to reject an unknown provider")
	}

	// The rejected write must not have changed anything.
	got, err := db.GetProjectScorerProvider(ctx, proj.ID)
	if err != nil {
		t.Fatalf("GetProjectScorerProvider: %v", err)
	}
	if got != "gemini" {
		t.Errorf("provider = %q, want it unchanged at gemini", got)
	}
}
