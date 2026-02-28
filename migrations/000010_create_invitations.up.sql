CREATE TABLE invitations (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    email       TEXT NOT NULL,
    org_id      UUID NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    org_role    TEXT NOT NULL DEFAULT 'member' CHECK (org_role IN ('owner','admin','member')),
    token_hash  TEXT NOT NULL UNIQUE,
    status      TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','accepted','declined','expired')),
    invited_by  UUID NOT NULL REFERENCES users(id),
    expires_at  TIMESTAMPTZ NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_invitations_email ON invitations (email);
CREATE INDEX idx_invitations_org_id ON invitations (org_id);
CREATE UNIQUE INDEX idx_invitations_email_org_pending ON invitations (email, org_id) WHERE status = 'pending';
