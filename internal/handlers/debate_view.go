// Package handlers — debate view model (UI refactor spec §6).
//
// Pure projections of (FeatureDebate, []DebateRound) into what the
// workspace templates render. Version labels ("v1, v2…") are DISPLAY-
// ONLY, derived by counting accepted rounds in round_number order; all
// actions key on round IDs / round_number, never on the label, because
// labels renumber after a Restore.

package handlers

import (
	"time"

	"github.com/madalin/forgedesk/internal/models"
)

// VersionEntry is one rail row. Dismissed (rejected) entries are
// context-only: Accepted=false, VersionLabel=0, no View/Restore.
type VersionEntry struct {
	DecidedAt    *time.Time
	RoundID      string
	Provider     string
	Feedback     string // full feedback text; templates truncate for display
	Model        string // staff/superadmin only — zeroed for clients
	RoundNumber  int
	VersionLabel int   // 0 for dismissed entries
	RestoreFrom  int   // undo ?from= value: RoundNumber+1
	CostMicros   int64 // staff/superadmin only
	Accepted     bool
	IsCurrent    bool
}

// SuggestionView is the pending in_review round, if any.
type SuggestionView struct {
	RoundID     string
	Provider    string
	InputText   string
	OutputText  string
	NextVersion int // CurrentVersionLabel + 1
}

// DebateView feeds debate.html and the region partials.
type DebateView struct {
	Pending               *SuggestionView
	CurrentDecidedAt      *time.Time
	CurrentProvider       string // provider of latest accepted round; "" at v0
	LatestAcceptedRoundID string
	Versions              []VersionEntry // newest-first
	CurrentVersionLabel   int
	CanEditSeed           bool
	IsStaff               bool
}

// EffortChipView feeds debate_effort_chip.html.
type EffortChipView struct {
	Debate    *models.FeatureDebate
	TicketID  string
	OOB       bool
	Rescoring bool // latest accepted round isn't the scored one
	Poll      bool // include the hx-trigger self-poll attributes
}

func buildDebateView(deb *models.FeatureDebate, rounds []models.DebateRound, isStaff bool) DebateView {
	v := DebateView{IsStaff: isStaff}

	accepted := 0
	entries := make([]VersionEntry, 0, len(rounds))
	for _, r := range rounds { // rounds are round_number ASC
		switch r.Status {
		case "in_review":
			v.Pending = &SuggestionView{
				RoundID:    r.ID,
				Provider:   r.Provider,
				InputText:  r.InputText,
				OutputText: r.OutputText,
			}
			continue // never in the rail
		case "accepted":
			accepted++
			v.CurrentVersionLabel = accepted
			v.CurrentProvider = r.Provider
			v.CurrentDecidedAt = r.DecidedAt
			v.LatestAcceptedRoundID = r.ID
		}
		e := VersionEntry{
			RoundID:     r.ID,
			RoundNumber: r.RoundNumber,
			RestoreFrom: r.RoundNumber + 1,
			Provider:    r.Provider,
			Accepted:    r.Status == "accepted",
			DecidedAt:   r.DecidedAt,
		}
		if e.Accepted {
			e.VersionLabel = accepted
		}
		if r.Feedback != nil {
			e.Feedback = *r.Feedback
		}
		if isStaff {
			e.Model = r.Model
			if r.CostMicros != nil {
				e.CostMicros = *r.CostMicros
			}
		}
		entries = append(entries, e)
	}
	// Newest-first for the rail; mark the current version.
	for i, j := 0, len(entries)-1; i < j; i, j = i+1, j-1 {
		entries[i], entries[j] = entries[j], entries[i]
	}
	for i := range entries {
		if entries[i].Accepted && entries[i].RoundID == v.LatestAcceptedRoundID {
			entries[i].IsCurrent = true
			break
		}
	}
	v.Versions = entries

	if v.Pending != nil {
		v.Pending.NextVersion = v.CurrentVersionLabel + 1
	}
	v.CanEditSeed = len(rounds) == 0 && deb.InFlightRequestID == nil
	return v
}

// buildEffortChipView decides staleness and polling server-side (spec
// §5.2): poll only while the debate is active AND the latest accept is
// younger than pollWindow (StaleReservationAge, 90s) — after that the
// chip settles without client-side timers, covering permanent scorer
// failure and terminal debates.
func buildEffortChipView(deb *models.FeatureDebate, rounds []models.DebateRound,
	ticketID string, now time.Time, pollWindow time.Duration,
) EffortChipView {
	chip := EffortChipView{Debate: deb, TicketID: ticketID}

	var latest *models.DebateRound
	for i := range rounds {
		if rounds[i].Status == "accepted" {
			latest = &rounds[i]
		}
	}
	if latest == nil {
		return chip // no accepted rounds: empty state, never polls
	}
	chip.Rescoring = deb.LastScoredRoundID == nil || *deb.LastScoredRoundID != latest.ID
	chip.Poll = chip.Rescoring &&
		deb.Status == models.DebateStatusActive &&
		latest.DecidedAt != nil &&
		now.Sub(*latest.DecidedAt) < pollWindow
	return chip
}
