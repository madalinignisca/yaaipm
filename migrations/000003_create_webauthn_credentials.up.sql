CREATE TABLE webauthn_credentials (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id             UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    credential_id       BYTEA NOT NULL UNIQUE,
    public_key          BYTEA NOT NULL,
    attestation_type    TEXT NOT NULL DEFAULT '',
    authenticator_aaguid BYTEA,
    sign_count          INTEGER NOT NULL DEFAULT 0,
    name                TEXT NOT NULL DEFAULT '',
    last_used_at        TIMESTAMPTZ,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_webauthn_credentials_user_id ON webauthn_credentials (user_id);
CREATE INDEX idx_webauthn_credentials_credential_id ON webauthn_credentials (credential_id);
