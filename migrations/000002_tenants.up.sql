-- tenants is the root of multi-tenancy: one row per club/league using the
-- platform. Every other tenant-scoped table below carries tenant_id
-- directly and, where it has children of its own, exposes
-- UNIQUE (id, tenant_id) so those children can take a composite foreign
-- key of (parent_id, tenant_id) instead of just (parent_id). That chain is
-- what makes cross-tenant data leakage a schema-level impossibility rather
-- than something we merely try to remember in every query.
CREATE TABLE tenants (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    slug            TEXT NOT NULL UNIQUE,              -- URL-safe identifier, e.g. "gor-mahia-fc"
    name            TEXT NOT NULL,
    sport           TEXT NOT NULL DEFAULT 'football',
    status          TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'suspended', 'inactive')),
    contact_email   CITEXT,
    -- Free-form, owned by the frontend: logo_url, primary_color, etc.
    -- Deliberately not normalized into columns — branding fields change
    -- shape more often than the schema should have to migrate for.
    branding        JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TRIGGER trg_tenants_updated_at
    BEFORE UPDATE ON tenants
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();
