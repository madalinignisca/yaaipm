DROP TABLE IF EXISTS reactions;

UPDATE tickets SET type = 'epic' WHERE type = 'feature';
ALTER TABLE tickets DROP CONSTRAINT IF EXISTS tickets_type_check;
ALTER TABLE tickets ADD CONSTRAINT tickets_type_check
    CHECK (type IN ('epic', 'task', 'subtask', 'bug'));
