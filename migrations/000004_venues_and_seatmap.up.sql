CREATE TABLE venues (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id   UUID NOT NULL REFERENCES tenants(id) ON DELETE RESTRICT,
    name        TEXT NOT NULL,
    address     TEXT,
    city        TEXT,
    country     TEXT,
    timezone    TEXT NOT NULL DEFAULT 'Africa/Nairobi',
    capacity    INTEGER CHECK (capacity IS NULL OR capacity >= 0),
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    -- Lets venue_sections (and, transitively, events) take a composite FK
    -- of (venue_id, tenant_id) — see migration 000002 header for why.
    UNIQUE (id, tenant_id)
);
CREATE INDEX idx_venues_tenant_id ON venues(tenant_id);

CREATE TRIGGER trg_venues_updated_at
    BEFORE UPDATE ON venues
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE TABLE venue_sections (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id    UUID NOT NULL,
    venue_id     UUID NOT NULL,
    name         TEXT NOT NULL,          -- e.g. "North Stand", "Block 12"
    code         TEXT NOT NULL,          -- short code printed on tickets, e.g. "NS12"
    seating_type TEXT NOT NULL DEFAULT 'reserved' CHECK (seating_type IN ('reserved', 'general_admission')),
    capacity     INTEGER NOT NULL CHECK (capacity >= 0),
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    -- Composite FK: Postgres rejects any attempt to attach a section to a
    -- venue owned by a different tenant, even if the app sent the wrong
    -- tenant_id by mistake.
    FOREIGN KEY (venue_id, tenant_id) REFERENCES venues(id, tenant_id) ON DELETE CASCADE,
    UNIQUE (id, tenant_id),
    UNIQUE (venue_id, code)
);
CREATE INDEX idx_venue_sections_venue_id ON venue_sections(venue_id);
CREATE INDEX idx_venue_sections_tenant_id ON venue_sections(tenant_id);

CREATE TRIGGER trg_venue_sections_updated_at
    BEFORE UPDATE ON venue_sections
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE TABLE seats (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id     UUID NOT NULL,
    section_id    UUID NOT NULL,
    row_label     TEXT NOT NULL,          -- e.g. "A", "12"
    seat_number   TEXT NOT NULL,          -- text, not int: some venues label seats "12A"
    is_accessible BOOLEAN NOT NULL DEFAULT false,
    -- Optional coordinates for a visual seat-map renderer on the frontend.
    pos_x         NUMERIC(8, 2),
    pos_y         NUMERIC(8, 2),
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    FOREIGN KEY (section_id, tenant_id) REFERENCES venue_sections(id, tenant_id) ON DELETE CASCADE,
    UNIQUE (id, tenant_id),
    UNIQUE (section_id, row_label, seat_number)
);
CREATE INDEX idx_seats_section_id ON seats(section_id);
CREATE INDEX idx_seats_tenant_id ON seats(tenant_id);
