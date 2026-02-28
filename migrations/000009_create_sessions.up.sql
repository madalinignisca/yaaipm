CREATE TABLE sessions (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    token_hash      TEXT NOT NULL UNIQUE,
    user_id         UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    two_factor_verified BOOLEAN NOT NULL DEFAULT FALSE,
    must_setup_2fa  BOOLEAN NOT NULL DEFAULT FALSE,
    ip_address      TEXT NOT NULL DEFAULT '',
    user_agent      TEXT NOT NULL DEFAULT '',
    expires_at      TIMESTAMPTZ NOT NULL,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_sessions_token_hash ON sessions (token_hash);
CREATE INDEX idx_sessions_user_id ON sessions (user_id);
CREATE INDEX idx_sessions_expires_at ON sessions (expires_at);
