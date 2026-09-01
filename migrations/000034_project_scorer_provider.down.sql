-- WARNING: this is lossy once non-Gemini scores exist. Dropping
-- effort_scorer_provider discards which provider produced each score, and
-- the up migration's backfill then stamps every scored debate as 'gemini'
-- — so a down+up cycle SILENTLY RELABELS Claude/OpenAI scores as Gemini.
-- Harmless before any project switches provider (everything really was
-- Gemini then). After that, do not reach for down+up as a "safe reset";
-- roll the application back instead and leave the schema alone, which is
-- safe because these columns are additive and old code ignores them.

ALTER TABLE feature_debates
    DROP COLUMN IF EXISTS effort_scorer_model,
    DROP COLUMN IF EXISTS effort_scorer_provider;

ALTER TABLE projects
    DROP COLUMN IF EXISTS scorer_provider;
