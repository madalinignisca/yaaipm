CREATE TABLE ticket_activities (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    ticket_id   UUID NOT NULL REFERENCES tickets(id) ON DELETE CASCADE,
    user_id     UUID REFERENCES users(id) ON DELETE SET NULL,
    agent_name  TEXT,
    action      TEXT NOT NULL CHECK (action IN ('status_change', 'comment', 'assignment', 'deploy', 'merge', '2fa_reset')),
    details_json JSONB NOT NULL DEFAULT '{}',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_ticket_activities_ticket_id ON ticket_activities (ticket_id);
