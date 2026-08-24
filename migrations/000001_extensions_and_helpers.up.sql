-- pgcrypto provides gen_random_uuid(), used as the default for every
-- primary key in this schema. UUID PKs avoid sequential-ID enumeration
-- across tenants and let application code generate an ID before insert
-- when useful (e.g. idempotent order creation).
CREATE EXTENSION IF NOT EXISTS pgcrypto;

-- citext gives us case-insensitive-unique email addresses without the
-- application having to remember to lower() everywhere.
CREATE EXTENSION IF NOT EXISTS citext;

-- set_updated_at() is attached as a BEFORE UPDATE trigger on every table
-- that has an updated_at column, so callers never have to remember to set
-- it manually — including in one-off scripts or future migrations.
CREATE OR REPLACE FUNCTION set_updated_at()
RETURNS TRIGGER AS $$
BEGIN
    NEW.updated_at = now();
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;
