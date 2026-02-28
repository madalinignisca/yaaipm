CREATE TABLE users (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    email           TEXT NOT NULL UNIQUE,
    password_hash   TEXT NOT NULL,
    name            TEXT NOT NULL,
    role            TEXT NOT NULL DEFAULT 'client' CHECK (role IN ('superadmin', 'staff', 'client')),
    totp_secret     BYTEA,
    totp_verified   BOOLEAN NOT NULL DEFAULT FALSE,
    recovery_codes  BYTEA,
    must_setup_2fa  BOOLEAN NOT NULL DEFAULT TRUE,
    preferred_2fa_method TEXT CHECK (preferred_2fa_method IN ('totp', 'webauthn')),
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_users_email ON users (email);
CREATE INDEX idx_users_role ON users (role);
