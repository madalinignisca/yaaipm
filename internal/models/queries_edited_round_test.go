package models

import (
	"context"
	"testing"

	"github.com/madalin/forgedesk/internal/testutil"
)

// seedAcceptedRound inserts an accepted round directly, optionally edited, so
// these tests can build a version history without driving the whole AI flow.
func seedAcceptedRound(t *testing.T, db *DB, debateID, userID string, num int, output string, edited *string) string {
	t.Helper()
	var id string
	if err := db.Pool.QueryRow(context.Background(),
		`INSERT INTO feature_debate_rounds (
			debate_id, round_number, provider, model, triggered_by,
			input_text, output_text, edited_text, status, decided_at
		) VALUES ($1, $2, 'claude', 'claude-test', $3, 'in', $4, $5, 'accepted', now())
		RETURNING id`,
		debateID, num, userID, output, edited,
	).Scan(&id); err != nil {
		t.Fatalf("seedAcceptedRound: %v", err)
	}
	return id
}

func strptr(s string) *string { return &s }

// TestUndoPreservesAnEarlierRoundsEdit pins the data-loss bug in #66.
//
// UndoRoundsFromTx recomputes current_text from the largest REMAINING accepted
// round. If that round was hand-edited, recomputing from output_text silently
// reverts the document to the AI's draft — discarding the user's correction
// while the row still records that it was edited. The round is not deleted, so
// nothing looks wrong; the text just quietly changes underneath.
func TestUndoPreservesAnEarlierRoundsEdit(t *testing.T) {
	pool := testutil.SetupTestDB(t)
	db := NewDB(pool)
	ctx := context.Background()

	orgID, userID, projID, ticketID := seedFeatureTicket(t, db, "seed text")
	deb, err := db.StartDebate(ctx, ticketID, projID, orgID, userID)
	if err != nil {
		t.Fatalf("StartDebate: %v", err)
	}

	// Round 1 was corrected by hand before being accepted.
	const aiText = "AI wrote this, with a typo"
	const userText = "AI wrote this, corrected by hand"
	seedAcceptedRound(t, db, deb.ID, userID, 1, aiText, strptr(userText))
	// Round 2 accepted as-is, and is what we undo.
	seedAcceptedRound(t, db, deb.ID, userID, 2, "second round text", nil)

	tx, err := db.Pool.Begin(ctx)
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if uErr := db.UndoRoundsFromTx(ctx, tx, deb.ID, 2); uErr != nil {
		_ = tx.Rollback(ctx)
		t.Fatalf("UndoRoundsFromTx: %v", uErr)
	}
	if cErr := tx.Commit(ctx); cErr != nil {
		t.Fatalf("Commit: %v", cErr)
	}

	var current string
	if sErr := db.Pool.QueryRow(ctx,
		`SELECT current_text FROM feature_debates WHERE id = $1`, deb.ID,
	).Scan(&current); sErr != nil {
		t.Fatalf("reading current_text: %v", sErr)
	}

	if current == aiText {
		t.Fatalf("undo reverted to the AI's draft and lost the user's edit:\n  got:  %q\n  want: %q", current, userText)
	}
	if current != userText {
		t.Fatalf("current_text = %q, want the edited text %q", current, userText)
	}
}

// TestShippedTextPrefersTheEdit is the single definition of "what shipped",
// used by accept, undo, both scorer paths and the version-history viewer. Each
// of those fails silently if it reads output_text directly, so this pins the
// helper they all go through.
func TestShippedTextPrefersTheEdit(t *testing.T) {
	unedited := &DebateRound{OutputText: "ai text"}
	if got := unedited.ShippedText(); got != "ai text" {
		t.Errorf("unedited round shipped %q, want the AI text", got)
	}

	edited := &DebateRound{OutputText: "ai text", EditedText: strptr("human text")}
	if got := edited.ShippedText(); got != "human text" {
		t.Errorf("edited round shipped %q, want the human text", got)
	}

	// An empty edit must never be treated as the shipped text — the CHECK
	// constraint and the handler both refuse it, and if one ever let it
	// through, shipping "" would blank the brief.
	blank := &DebateRound{OutputText: "ai text", EditedText: strptr("")}
	if got := blank.ShippedText(); got == "" {
		t.Error("an empty edit was treated as the shipped text; that would blank the document")
	}
}

// TestCheckConstraintRejectsWhitespaceOnlyEdit guards the database backstop.
//
// The handler refuses a blank edit first; this CHECK is what catches anything
// that bypasses it. It nearly did not: btrim(x) with ONE argument strips only
// SPACES, so a tab- or newline-only value survived it and read as non-empty —
// leaving the backstop weaker than the guard it backs up, precisely in the case
// it exists for. The explicit character set fixes that, and this test pins it.
func TestCheckConstraintRejectsWhitespaceOnlyEdit(t *testing.T) {
	db := NewDB(testutil.SetupTestDB(t))
	ctx := context.Background()

	orgID, userID, projID, ticketID := seedFeatureTicket(t, db, "seed")
	deb, err := db.StartDebate(ctx, ticketID, projID, orgID, userID)
	if err != nil {
		t.Fatalf("StartDebate: %v", err)
	}
	roundID := seedAcceptedRound(t, db, deb.ID, userID, 1, "ai text", nil)

	for name, blank := range map[string]string{
		"spaces":  "   ",
		"tab":     "\t",
		"newline": "\n",
		"mixed":   " \t\n\r ",
		"empty":   "",
	} {
		t.Run(name, func(t *testing.T) {
			_, err := db.Pool.Exec(ctx,
				`UPDATE feature_debate_rounds SET edited_text = $1 WHERE id = $2`, blank, roundID)
			if err == nil {
				t.Errorf("the database accepted a whitespace-only edit (%q); shipping it would blank the brief", blank)
			}
		})
	}
}
