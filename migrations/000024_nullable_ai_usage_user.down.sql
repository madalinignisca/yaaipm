-- Backfill NULLs with the first superadmin before re-adding constraint
UPDATE ai_usage_entries
SET user_id = (SELECT id FROM users WHERE role = 'superadmin' ORDER BY created_at LIMIT 1)
WHERE user_id IS NULL;

ALTER TABLE ai_usage_entries ALTER COLUMN user_id SET NOT NULL;
