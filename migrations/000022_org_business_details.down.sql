ALTER TABLE organizations
    DROP COLUMN IF EXISTS business_name,
    DROP COLUMN IF EXISTS vat_number,
    DROP COLUMN IF EXISTS registration_number,
    DROP COLUMN IF EXISTS address_street,
    DROP COLUMN IF EXISTS address_extra,
    DROP COLUMN IF EXISTS postal_code,
    DROP COLUMN IF EXISTS city,
    DROP COLUMN IF EXISTS country,
    DROP COLUMN IF EXISTS contact_phones,
    DROP COLUMN IF EXISTS contact_emails;
