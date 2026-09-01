package models

import (
	"context"
	"errors"
	"testing"

	"github.com/madalin/forgedesk/internal/testutil"
)

// acceptWith runs AcceptRoundTx in its own transaction and returns the round.
func acceptWith(t *testing.T, db *DB, debateID, roundID string, edited *string) (*DebateRound, error) {
	t.Helper()
	ctx := context.Background()
	tx, err := db.Pool.Begin(ctx)
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	round, acceptErr := db.AcceptRoundTx(ctx, tx, debateID, roundID, edited)
	if acceptErr != nil {
		_ = tx.Rollback(ctx)
		return nil, acceptErr
	}
	if cErr := tx.Commit(ctx); cErr != nil {
		t.Fatalf("Commit: %v", cErr)
	}
	return round, nil
}

// seedPendingRound inserts an in_review round ready to be accepted.
func seedPendingRound(t *testing.T, db *DB, debateID, userID string, num int, output string) string {
	t.Helper()
	var id string
	if err := db.Pool.QueryRow(context.Background(),
		`INSERT INTO feature_debate_rounds (
			debate_id, round_number, provider, model, triggered_by,
			input_text, output_text, status
		) VALUES ($1, $2, 'claude', 'claude-test', $3, 'in', $4, 'in_review')
		RETURNING id`,
		debateID, num, userID, output,
	).Scan(&id); err != nil {
		t.Fatalf("seedPendingRound: %v", err)
	}
	return id
}

func newDebate(t *testing.T, db *DB) (debate *FeatureDebate, userID string) {
	t.Helper()
	orgID, userID, projID, ticketID := seedFeatureTicket(t, db, "seed text")
	deb, err := db.StartDebate(context.Background(), ticketID, projID, orgID, userID)
	if err != nil {
		t.Fatalf("StartDebate: %v", err)
	}
	return deb, userID
}

func currentText(t *testing.T, db *DB, debateID string) string {
	t.Helper()
	var s string
	if err := db.Pool.QueryRow(context.Background(),
		`SELECT current_text FROM feature_debates WHERE id = $1`, debateID).Scan(&s); err != nil {
		t.Fatalf("reading current_text: %v", err)
	}
	return s
}

// TestAcceptWithEditShipsTheEditAndKeepsTheAIText is the core of #66.
//
// The edited text becomes the document; output_text still holds what the AI
// produced. That separation is the point: the cost columns and the scorer bill
// for the AI's text, so overwriting it would destroy the record of what the
// money bought.
func TestAcceptWithEditShipsTheEditAndKeepsTheAIText(t *testing.T) {
	db := NewDB(testutil.SetupTestDB(t))
	deb, userID := newDebate(t, db)

	const aiText = "AI draft with a typo"
	const userText = "AI draft, corrected"
	roundID := seedPendingRound(t, db, deb.ID, userID, 1, aiText)

	round, err := acceptWith(t, db, deb.ID, roundID, strptr(userText))
	if err != nil {
		t.Fatalf("AcceptRoundTx: %v", err)
	}

	if got := currentText(t, db, deb.ID); got != userText {
		t.Errorf("current_text = %q, want the edited text %q", got, userText)
	}
	if round.OutputText != aiText {
		t.Errorf("output_text = %q; the AI's original must survive editing", round.OutputText)
	}
	if round.EditedText == nil || *round.EditedText != userText {
		t.Errorf("edited_text = %v, want %q", round.EditedText, userText)
	}
	if got := round.ShippedText(); got != userText {
		t.Errorf("ShippedText() = %q, want %q", got, userText)
	}
}

// TestAcceptWithoutEditLeavesEditedTextNull covers both "did not open the tab"
// and "opened it and changed nothing". The second is the subtle one: handing
// back the AI's own text is not an edit, and recording it as one would make
// every later "was this hand-edited?" answer wrong.
func TestAcceptWithoutEditLeavesEditedTextNull(t *testing.T) {
	db := NewDB(testutil.SetupTestDB(t))
	const aiText = "AI draft, untouched"

	for _, tc := range []struct {
		name   string
		edited *string
	}{
		{"field absent entirely", nil},
		{"field present but identical to the AI text", strptr(aiText)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			deb, userID := newDebate(t, db)
			roundID := seedPendingRound(t, db, deb.ID, userID, 1, aiText)

			round, err := acceptWith(t, db, deb.ID, roundID, tc.edited)
			if err != nil {
				t.Fatalf("AcceptRoundTx: %v", err)
			}
			if round.EditedText != nil {
				t.Errorf("edited_text = %q, want NULL — nothing was edited", *round.EditedText)
			}
			if got := currentText(t, db, deb.ID); got != aiText {
				t.Errorf("current_text = %q, want %q", got, aiText)
			}
		})
	}
}

// TestAcceptRefusesAnEmptyEdit: shipping a blank would set current_text to
// nothing and destroy the brief, so it is a validation error rather than a
// silent no-op.
func TestAcceptRefusesAnEmptyEdit(t *testing.T) {
	db := NewDB(testutil.SetupTestDB(t))
	const aiText = "AI draft"

	// Subtests, not a bare loop: seedFeatureTicket keys its user's email off
	// t.Name(), so repeating it inside one test collides on the unique index.
	for name, blank := range map[string]string{
		"empty string":    "",
		"spaces":          "   ",
		"newline and tab": "\n\t ",
	} {
		t.Run(name, func(t *testing.T) {
			deb, userID := newDebate(t, db)
			roundID := seedPendingRound(t, db, deb.ID, userID, 1, aiText)
			before := currentText(t, db, deb.ID)

			_, err := acceptWith(t, db, deb.ID, roundID, strptr(blank))
			if !errors.Is(err, ErrEmptyEdit) {
				t.Errorf("accept with %q = %v, want ErrEmptyEdit", blank, err)
			}
			if got := currentText(t, db, deb.ID); got != before {
				t.Errorf("current_text changed to %q after a refused empty edit", got)
			}
		})
	}
}

// TestWhitespaceOnlyDifferenceIsNotAnEdit makes an untested choice deliberate.
//
// The user opens the Edit tab, adds a stray trailing space, and accepts. That
// is not an edit of the brief, and recording one would make every later "was
// this hand-edited?" answer wrong — the same concern that motivates folding
// CRLF in the handler: a difference nobody can see must not become data.
func TestWhitespaceOnlyDifferenceIsNotAnEdit(t *testing.T) {
	db := NewDB(testutil.SetupTestDB(t))
	const aiText = "The AI's proposal."

	deb, userID := newDebate(t, db)
	roundID := seedPendingRound(t, db, deb.ID, userID, 1, aiText)

	round, err := acceptWith(t, db, deb.ID, roundID, strptr(aiText+"   \n"))
	if err != nil {
		t.Fatalf("AcceptRoundTx: %v", err)
	}
	if round.EditedText != nil {
		t.Errorf("edited_text = %q; a whitespace-only difference is not an edit", *round.EditedText)
	}
	if got := currentText(t, db, deb.ID); got != aiText {
		t.Errorf("current_text = %q, want the AI text unchanged", got)
	}
}
