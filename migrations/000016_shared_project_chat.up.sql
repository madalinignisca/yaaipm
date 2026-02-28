-- Add user attribution to AI messages for shared project chat
ALTER TABLE ai_messages ADD COLUMN user_id UUID REFERENCES users(id) ON DELETE SET NULL;
ALTER TABLE ai_messages ADD COLUMN user_name TEXT NOT NULL DEFAULT '';

-- Ensure one conversation per project (prevents race condition duplicates)
CREATE UNIQUE INDEX idx_ai_conversations_project_active
    ON ai_conversations(project_id) WHERE project_id IS NOT NULL;
