ALTER TABLE tickets ADD COLUMN archived_at TIMESTAMPTZ;
CREATE INDEX idx_tickets_archived_at ON tickets (archived_at) WHERE archived_at IS NOT NULL;
