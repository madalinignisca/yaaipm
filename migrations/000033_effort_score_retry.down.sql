DROP INDEX IF EXISTS idx_feature_debates_effort_retry;

ALTER TABLE feature_debates
    DROP COLUMN IF EXISTS effort_retry_next_at,
    DROP COLUMN IF EXISTS effort_retry_attempts;
