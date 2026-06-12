package handlers

import (
	"testing"
	"time"

	"github.com/madalin/forgedesk/internal/models"
)

func strPtr(s string) *string { return &s }
func timePtr(t time.Time) *time.Time { return &t }

func mkRound(id string, n int, status, provider, output string, decided *time.Time) models.DebateRound {
	cost := int64(12345)
	return models.DebateRound{
		ID: id, RoundNumber: n, Status: status, Provider: provider,
		Model: "model-x", OutputText: output, InputText: "in",
		DecidedAt: decided, CostMicros: &cost,
	}
}

func TestBuildDebateView_VersionLabelsCountAcceptedOnly(t *testing.T) {
	now := time.Now()
	deb := &models.FeatureDebate{ID: "d1", Status: models.DebateStatusActive, CurrentText: "v2 text"}
	rounds := []models.DebateRound{
		mkRound("r1", 1, "accepted", "claude", "v1 text", timePtr(now.Add(-20*time.Minute))),
		mkRound("r2", 2, "rejected", "openai", "rejected text", timePtr(now.Add(-5*time.Minute))),
		mkRound("r3", 3, "accepted", "gemini", "v2 text", timePtr(now.Add(-5*time.Minute))),
	}
	v := buildDebateView(deb, rounds, false)

	if v.CurrentVersionLabel != 2 {
		t.Fatalf("CurrentVersionLabel = %d, want 2", v.CurrentVersionLabel)
	}
	if len(v.Versions) != 3 {
		t.Fatalf("len(Versions) = %d, want 3", len(v.Versions))
	}
	if v.Versions[0].RoundID != "r3" || v.Versions[0].VersionLabel != 2 || !v.Versions[0].IsCurrent {
		t.Fatalf("Versions[0] wrong: %+v", v.Versions[0])
	}
	if v.Versions[1].RoundID != "r2" || v.Versions[1].Accepted || v.Versions[1].VersionLabel != 0 {
		t.Fatalf("Versions[1] (dismissed) wrong: %+v", v.Versions[1])
	}
	if v.Versions[2].VersionLabel != 1 || v.Versions[2].RestoreFrom != 2 {
		t.Fatalf("Versions[2] wrong (RestoreFrom must be RoundNumber+1): %+v", v.Versions[2])
	}
	if v.Pending != nil {
		t.Fatal("no in_review round — Pending must be nil")
	}
	if v.LatestAcceptedRoundID != "r3" {
		t.Fatalf("LatestAcceptedRoundID = %q, want r3", v.LatestAcceptedRoundID)
	}
}

func TestBuildDebateView_PendingSuggestion(t *testing.T) {
	deb := &models.FeatureDebate{ID: "d1", Status: models.DebateStatusActive, CurrentText: "seed"}
	rounds := []models.DebateRound{mkRound("r1", 1, "in_review", "claude", "proposal", nil)}
	v := buildDebateView(deb, rounds, false)

	if v.Pending == nil || v.Pending.RoundID != "r1" || v.Pending.NextVersion != 1 {
		t.Fatalf("Pending wrong: %+v", v.Pending)
	}
	if v.CurrentVersionLabel != 0 {
		t.Fatalf("CurrentVersionLabel = %d, want 0 (no accepted rounds)", v.CurrentVersionLabel)
	}
	if len(v.Versions) != 0 {
		t.Fatalf("in_review round leaked into Versions: %+v", v.Versions)
	}
	if v.CanEditSeed {
		t.Fatal("CanEditSeed must be false once any round exists")
	}
}

func TestBuildDebateView_CanEditSeedOnlyWhenNoRoundsAndNotInFlight(t *testing.T) {
	deb := &models.FeatureDebate{ID: "d1", Status: models.DebateStatusActive}
	if v := buildDebateView(deb, nil, false); !v.CanEditSeed {
		t.Fatal("no rounds + not in flight → CanEditSeed must be true")
	}
	deb.InFlightRequestID = strPtr("req-1")
	if v := buildDebateView(deb, nil, false); v.CanEditSeed {
		t.Fatal("in-flight reservation → CanEditSeed must be false")
	}
}

func TestBuildDebateView_StaffGating(t *testing.T) {
	now := time.Now()
	deb := &models.FeatureDebate{ID: "d1", Status: models.DebateStatusActive}
	rounds := []models.DebateRound{mkRound("r1", 1, "accepted", "claude", "x", timePtr(now))}

	client := buildDebateView(deb, rounds, false)
	if client.Versions[0].Model != "" || client.Versions[0].CostMicros != 0 {
		t.Fatalf("client must not see model/cost: %+v", client.Versions[0])
	}
	staff := buildDebateView(deb, rounds, true)
	if staff.Versions[0].Model != "model-x" || staff.Versions[0].CostMicros != 12345 {
		t.Fatalf("staff must see model/cost: %+v", staff.Versions[0])
	}
}

func TestBuildEffortChipView_StaleAndPolling(t *testing.T) {
	now := time.Now()
	recent := timePtr(now.Add(-10 * time.Second))
	old := timePtr(now.Add(-5 * time.Minute))
	window := 90 * time.Second

	deb := &models.FeatureDebate{ID: "d1", Status: models.DebateStatusActive}
	rounds := []models.DebateRound{mkRound("r1", 1, "accepted", "claude", "x", recent)}
	chip := buildEffortChipView(deb, rounds, "t1", now, window)
	if !chip.Rescoring || !chip.Poll {
		t.Fatalf("recent unscored accept: want Rescoring+Poll, got %+v", chip)
	}

	rounds[0].DecidedAt = old
	chip = buildEffortChipView(deb, rounds, "t1", now, window)
	if chip.Poll {
		t.Fatalf("accept older than window must not poll: %+v", chip)
	}

	deb.LastScoredRoundID = strPtr("r1")
	score := 6
	deb.EffortScore = &score
	rounds[0].DecidedAt = recent
	chip = buildEffortChipView(deb, rounds, "t1", now, window)
	if chip.Rescoring || chip.Poll {
		t.Fatalf("fresh score must be neither rescoring nor polling: %+v", chip)
	}

	deb.LastScoredRoundID = nil
	deb.Status = "approved"
	chip = buildEffortChipView(deb, rounds, "t1", now, window)
	if chip.Poll {
		t.Fatalf("terminal debate must not poll: %+v", chip)
	}
}
