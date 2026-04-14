-- Drop in reverse order, breaking the cyclic FK first.
-- The project_costs CHECK constraint expansion is NOT reverted to preserve
-- financial audit history (see spec §3.1.ter, §10).

ALTER TABLE feature_debates DROP CONSTRAINT IF EXISTS feature_debates_last_scored_round_id_fkey;

DROP INDEX IF EXISTS idx_feature_debate_rounds_one_in_review_per_debate;
DROP INDEX IF EXISTS idx_feature_debate_rounds_triggered_by_created;
DROP INDEX IF EXISTS idx_feature_debate_rounds_debate_status_number;
DROP INDEX IF EXISTS idx_feature_debate_rounds_debate;

DROP INDEX IF EXISTS idx_feature_debates_started_by;
DROP INDEX IF EXISTS idx_feature_debates_project;
DROP INDEX IF EXISTS idx_feature_debates_org_status;
DROP INDEX IF EXISTS idx_feature_debates_ticket;
DROP INDEX IF EXISTS idx_feature_debates_one_active_per_ticket;

DROP TABLE IF EXISTS feature_debate_rounds;
DROP TABLE IF EXISTS feature_debates;
