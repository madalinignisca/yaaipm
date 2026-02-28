DROP INDEX IF EXISTS idx_tickets_archived_at;
ALTER TABLE tickets DROP COLUMN IF EXISTS archived_at;
