ALTER TABLE organizations
    ADD COLUMN business_name       TEXT NOT NULL DEFAULT '',
    ADD COLUMN vat_number          TEXT NOT NULL DEFAULT '',
    ADD COLUMN registration_number TEXT NOT NULL DEFAULT '',
    ADD COLUMN address_street      TEXT NOT NULL DEFAULT '',
    ADD COLUMN address_extra       TEXT NOT NULL DEFAULT '',
    ADD COLUMN postal_code         TEXT NOT NULL DEFAULT '',
    ADD COLUMN city                TEXT NOT NULL DEFAULT '',
    ADD COLUMN country             TEXT NOT NULL DEFAULT '',
    ADD COLUMN contact_phones      TEXT NOT NULL DEFAULT '',
    ADD COLUMN contact_emails      TEXT NOT NULL DEFAULT '';
