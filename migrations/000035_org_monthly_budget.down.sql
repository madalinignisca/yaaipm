-- LOSSY: dropping monthly_budget_cents resets EVERY org to unlimited, and
-- dropping org_budget_changes discards the entire audit trail of who
-- raised/lowered a money control and when. There is no way to reconstruct
-- either after this runs. If you need to roll back a bad deploy, roll back
-- the application binary instead of running this migration down.
DROP TABLE IF EXISTS org_budget_changes;
ALTER TABLE organizations DROP COLUMN IF EXISTS monthly_budget_cents;
