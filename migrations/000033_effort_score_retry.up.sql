-- Phase-2 issue #68: background retry of stale-NULL effort scores.
--
-- v1's scoreAfterAccept (handlers/debate.go) is fire-and-forget: if the
-- scorer call fails, effort_* stays NULL forever and the sidebar shows
-- "Score appears after the first accepted round." indefinitely. This
-- migration adds the state a periodic sweep needs to self-heal that:
--
--   effort_retry_attempts  how many times the sweep has already claimed
--                          this debate, so the backoff can grow.
--   effort_retry_next_at   the lease / not-before timestamp. The sweep
--                          claims rows with FOR UPDATE SKIP LOCKED and
--                          pushes this forward by an exponential backoff,
--                          so the two forgedesk-server replicas never both
--                          fire a billable scorer call for the same row,
--                          and a failed attempt waits before retrying.
--
-- Existing stuck debates get attempts=0 / next_at=NULL and so become
-- eligible on the first sweep after deploy — the migration itself heals
-- the v1 backlog. NULL next_at sorts first (NULLS FIRST) so the oldest
-- never-attempted debates are scored before ones already in backoff.

ALTER TABLE feature_debates
    ADD COLUMN effort_retry_attempts INT NOT NULL DEFAULT 0,
    ADD COLUMN effort_retry_next_at  TIMESTAMPTZ;

-- Partial index over the candidate set the sweep scans every few minutes:
-- debates that are unscored and still active or approved. Keeps the claim
-- query off a sequential scan as feature_debates grows; abandoned and
-- already-scored debates never appear in the predicate so the index stays
-- small.
CREATE INDEX idx_feature_debates_effort_retry
    ON feature_debates (effort_retry_next_at)
    WHERE effort_scored_at IS NULL AND status IN ('active', 'approved');
