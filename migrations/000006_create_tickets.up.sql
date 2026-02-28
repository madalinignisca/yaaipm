CREATE TABLE tickets (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id          UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    parent_id           UUID REFERENCES tickets(id) ON DELETE SET NULL,
    type                TEXT NOT NULL DEFAULT 'task' CHECK (type IN ('epic', 'task', 'subtask', 'bug')),
    title               TEXT NOT NULL,
    description_markdown TEXT NOT NULL DEFAULT '',
    status              TEXT NOT NULL DEFAULT 'backlog' CHECK (status IN ('backlog', 'ready', 'planning', 'plan_review', 'implementing', 'testing', 'review', 'done', 'cancelled')),
    priority            TEXT NOT NULL DEFAULT 'medium' CHECK (priority IN ('low', 'medium', 'high', 'critical')),
    date_start          DATE,
    date_end            DATE,
    agent_mode          TEXT CHECK (agent_mode IN ('plan', 'implement')),
    agent_name          TEXT CHECK (agent_name IN ('claude', 'gemini', 'codex', 'mistral')),
    assigned_to         UUID REFERENCES users(id) ON DELETE SET NULL,
    created_by          UUID NOT NULL REFERENCES users(id),
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_tickets_project_id ON tickets (project_id);
CREATE INDEX idx_tickets_parent_id ON tickets (parent_id);
CREATE INDEX idx_tickets_status ON tickets (status);
CREATE INDEX idx_tickets_type ON tickets (type);
CREATE INDEX idx_tickets_assigned_to ON tickets (assigned_to);
CREATE INDEX idx_tickets_created_by ON tickets (created_by);
