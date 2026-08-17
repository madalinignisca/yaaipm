ALTER TABLE feature_debates
    DROP COLUMN IF EXISTS effort_scorer_model,
    DROP COLUMN IF EXISTS effort_scorer_provider;

ALTER TABLE projects
    DROP COLUMN IF EXISTS scorer_provider;
