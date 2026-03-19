CREATE INDEX IF NOT EXISTS idx_comments_user_id ON comments(user_id);
CREATE INDEX IF NOT EXISTS idx_ai_usage_entries_user_id ON ai_usage_entries(user_id);
CREATE INDEX IF NOT EXISTS idx_ai_messages_user_id ON ai_messages(user_id);
CREATE INDEX IF NOT EXISTS idx_sessions_selected_org_id ON sessions(selected_org_id);
CREATE INDEX IF NOT EXISTS idx_invitations_pending_expires ON invitations(expires_at) WHERE status = 'pending';
