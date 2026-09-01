-- Phase-2 issue #63: per-project configurable debate scorer provider.
--
-- v1 hardcoded Gemini as the scorer (spec §3.2, cmd/server/main.go). This
-- migration adds the two pieces of state that per-project selection needs:
--
--   projects.scorer_provider          which provider runs the scorer for
--                                     debates on this project. Staff-only
--                                     setting; clients never see it.
--   feature_debates.effort_scorer_*   which provider/model actually produced
--                                     the CURRENT effort_* snapshot.
--
-- The second one is not redundant with the first. The effort chip labels a
-- score with the provider that produced it; if it read the project's live
-- setting instead, flipping the dropdown would retroactively relabel every
-- older score — a Gemini-scored debate would start claiming "via ChatGPT".
-- The columns sit beside the effort_* snapshot they describe and are
-- written in the same conditional UPDATE, so they cannot drift from it.
--
-- TEXT + inline CHECK rather than a PG enum, matching the provider column
-- on feature_debate_rounds (000032, line 38): adding a fourth provider
-- later is an ALTER of the constraint, not a type migration.
--
-- Existing projects default to 'gemini', which is exactly what v1 did, so
-- behaviour is unchanged until an admin opts in.

ALTER TABLE projects
    ADD COLUMN scorer_provider TEXT NOT NULL DEFAULT 'gemini'
        CHECK (scorer_provider IN ('claude', 'gemini', 'openai'));

-- Nullable with no default: a debate that has never been scored has no
-- scorer, and NULL says that honestly. The CHECK admits NULL for the same
-- reason. effort_scorer_model is deliberately un-constrained — model IDs
-- change far more often than provider names, and pinning them in a CHECK
-- would turn every model bump into a migration.
ALTER TABLE feature_debates
    ADD COLUMN effort_scorer_provider TEXT
        CHECK (effort_scorer_provider IS NULL
               OR effort_scorer_provider IN ('claude', 'gemini', 'openai')),
    ADD COLUMN effort_scorer_model TEXT;

-- Every score that exists today was produced by GeminiScorer, so stamping
-- the provider is a statement of historical fact, not a guess. Without it
-- the effort chip would silently drop its "via Gemini" label for every
-- pre-existing debate the moment this ships. The model is left NULL: the
-- exact GEMINI_MODEL in force at scoring time is not recoverable, and
-- inventing one would be a guess.
UPDATE feature_debates
   SET effort_scorer_provider = 'gemini'
 WHERE effort_scored_at IS NOT NULL;
