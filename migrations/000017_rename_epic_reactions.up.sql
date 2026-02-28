-- Rename ticket type: epic → feature
ALTER TABLE tickets DROP CONSTRAINT IF EXISTS tickets_type_check;
UPDATE tickets SET type = 'feature' WHERE type = 'epic';
ALTER TABLE tickets ADD CONSTRAINT tickets_type_check
    CHECK (type IN ('feature', 'task', 'subtask', 'bug'));

-- Emoji reactions (polymorphic: works for both tickets and comments)
CREATE TABLE reactions (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    target_type TEXT NOT NULL CHECK (target_type IN ('ticket', 'comment')),
    target_id UUID NOT NULL,
    user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    emoji TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE(target_type, target_id, user_id, emoji)
);
CREATE INDEX idx_reactions_target ON reactions(target_type, target_id);
