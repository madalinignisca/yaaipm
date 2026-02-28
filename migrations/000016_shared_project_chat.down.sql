DROP INDEX IF EXISTS idx_ai_conversations_project_active;
ALTER TABLE ai_messages DROP COLUMN IF EXISTS user_name;
ALTER TABLE ai_messages DROP COLUMN IF EXISTS user_id;
