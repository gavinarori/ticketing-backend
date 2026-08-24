-- Reusable price-tier labels within a tenant (e.g. "VIP", "Category 1",
-- "Terraces"). Actual prices live per-event in event_ticket_categories
-- below, since the same "VIP" category prices very differently for a
-- relegation six-pointer than for a pre-season friendly.
CREATE TABLE seat_categories (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id   UUID NOT NULL REFERENCES tenants(id) ON DELETE RESTRICT,
    name        TEXT NOT NULL,
    color       TEXT,          -- hex color for seat-map UI rendering
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (id, tenant_id),
    UNIQUE (tenant_id, name)
);

CREATE TRIGGER trg_seat_categories_updated_at
    BEFORE UPDATE ON seat_categories
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE TABLE events (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id       UUID NOT NULL,
    venue_id        UUID NOT NULL,
    name            TEXT NOT NULL,        -- e.g. "Gor Mahia vs AFC Leopards"
    competition     TEXT,
    home_team       TEXT,
    away_team       TEXT,
    starts_at       TIMESTAMPTZ NOT NULL,
    doors_open_at   TIMESTAMPTZ,
    sales_start_at  TIMESTAMPTZ NOT NULL,
    sales_end_at    TIMESTAMPTZ NOT NULL,
    status          TEXT NOT NULL DEFAULT 'draft'
                     CHECK (status IN ('draft', 'scheduled', 'on_sale', 'sold_out', 'completed', 'cancelled')),
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    FOREIGN KEY (venue_id, tenant_id) REFERENCES venues(id, tenant_id) ON DELETE RESTRICT,
    UNIQUE (id, tenant_id),
    CHECK (sales_end_at > sales_start_at),
    CHECK (starts_at >= sales_start_at)
);
CREATE INDEX idx_events_tenant_id ON events(tenant_id);
CREATE INDEX idx_events_venue_id ON events(venue_id);
CREATE INDEX idx_events_status ON events(status);
CREATE INDEX idx_events_starts_at ON events(starts_at);

CREATE TRIGGER trg_events_updated_at
    BEFORE UPDATE ON events
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE TABLE event_ticket_categories (
    id                 UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id          UUID NOT NULL,
    event_id           UUID NOT NULL,
    seat_category_id   UUID NOT NULL,
    price_cents        BIGINT NOT NULL CHECK (price_cents >= 0),
    currency           TEXT NOT NULL DEFAULT 'KES',
    max_per_order      INTEGER NOT NULL DEFAULT 6 CHECK (max_per_order > 0),
    created_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    FOREIGN KEY (event_id, tenant_id) REFERENCES events(id, tenant_id) ON DELETE CASCADE,
    FOREIGN KEY (seat_category_id, tenant_id) REFERENCES seat_categories(id, tenant_id) ON DELETE RESTRICT,
    UNIQUE (id, tenant_id),
    UNIQUE (event_id, seat_category_id)
);
CREATE INDEX idx_etc_event_id ON event_ticket_categories(event_id);
CREATE INDEX idx_etc_tenant_id ON event_ticket_categories(tenant_id);

CREATE TRIGGER trg_etc_updated_at
    BEFORE UPDATE ON event_ticket_categories
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();
