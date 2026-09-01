-- Append-only record of money actually paid to AI providers for debates (#129).
--
-- WHY THIS EXISTS AND IS NOT JUST A COLUMN ON feature_debate_rounds:
-- budget enforcement summed cost_micros + scorer_cost_micros over live round
-- rows, but undo HARD-DELETES rounds. The provider call already happened and
-- the invoice is real, so deleting user content was silently deleting the
-- accounting with it — an org near its cap could undo to buy back headroom for
-- money it had already spent.
--
-- The root error was a category confusion: spend is an immutable financial
-- fact, a round is mutable user content. One row served both, so editing the
-- content edited the books.
CREATE TABLE debate_spend (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id      UUID NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,

    -- Deliberately NOT foreign keys. ON DELETE CASCADE would reintroduce the
    -- exact bug this table fixes; ON DELETE SET NULL would work but costs the
    -- audit trail. These are provenance, not referential integrity — the
    -- spend must outlive the round and the debate that caused it.
    debate_id   UUID,
    round_id    UUID,

    kind        TEXT   NOT NULL CHECK (kind IN ('round', 'scorer')),
    cost_micros BIGINT NOT NULL CHECK (cost_micros >= 0),
    incurred_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Enforcement scans one org's entries over a month range.
CREATE INDEX debate_spend_org_incurred_idx ON debate_spend (org_id, incurred_at);

-- Backfill so the cap does not silently reset on deploy.
--
-- Without this, switching enforcement to an empty ledger would drop every
-- org's observed spend to zero and lift every cap for the rest of the month.
--
-- NOT EXISTS makes this re-runnable. That matters because migrations run
-- BEFORE the new image rolls out: rounds created in the window between the
-- migration and the rollout are written by old code that does not know about
-- this table, so they land in feature_debate_rounds only. Re-running the same
-- backfill after the rollout picks them up without duplicating anything.
--
-- Deliberately NOT a UNIQUE constraint: a round can legitimately carry SEVERAL
-- scorer charges (the retry sweep re-scores, and every attempt is a real billed
-- call). A uniqueness rule here would silently discard genuine charges.
INSERT INTO debate_spend (org_id, debate_id, round_id, kind, cost_micros, incurred_at)
SELECT d.org_id, r.debate_id, r.id, 'round', r.cost_micros, r.created_at
  FROM feature_debate_rounds r
  JOIN feature_debates d ON d.id = r.debate_id
 WHERE COALESCE(r.cost_micros, 0) > 0
   AND NOT EXISTS (
        SELECT 1 FROM debate_spend s
         WHERE s.round_id = r.id AND s.kind = 'round');

INSERT INTO debate_spend (org_id, debate_id, round_id, kind, cost_micros, incurred_at)
SELECT d.org_id, r.debate_id, r.id, 'scorer', r.scorer_cost_micros, r.created_at
  FROM feature_debate_rounds r
  JOIN feature_debates d ON d.id = r.debate_id
 WHERE COALESCE(r.scorer_cost_micros, 0) > 0
   AND NOT EXISTS (
        SELECT 1 FROM debate_spend s
         WHERE s.round_id = r.id AND s.kind = 'scorer');

-- The backfill is a FLOOR, not a reconstruction. Rounds undone before this
-- migration are gone, and their spend is unrecoverable. Likewise a round that
-- was scored more than once only ever kept its LAST scorer cost, so earlier
-- attempts cannot be recovered either.
