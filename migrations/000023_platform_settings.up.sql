CREATE TABLE platform_settings (
    id                  INTEGER PRIMARY KEY DEFAULT 1 CHECK (id = 1),
    business_name       TEXT NOT NULL DEFAULT '',
    vat_number          TEXT NOT NULL DEFAULT '',
    registration_number TEXT NOT NULL DEFAULT '',
    address_street      TEXT NOT NULL DEFAULT '',
    address_extra       TEXT NOT NULL DEFAULT '',
    postal_code         TEXT NOT NULL DEFAULT '',
    city                TEXT NOT NULL DEFAULT '',
    country             TEXT NOT NULL DEFAULT '',
    contact_phones      TEXT NOT NULL DEFAULT '',
    contact_emails      TEXT NOT NULL DEFAULT '',
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now()
);

INSERT INTO platform_settings (id) VALUES (1);
