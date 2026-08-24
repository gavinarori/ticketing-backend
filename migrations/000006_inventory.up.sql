-- event_seat_inventory is the single source of truth for sellable ticket
-- units. Every purchasable ticket — reserved-seat or general-admission —
-- is exactly one row here, pre-generated when an event goes on sale. This
-- uniform "one row per sellable unit" model means the oversell guarantee
-- is identical regardless of seating type: a purchase can only ever
-- succeed via an atomic compare-and-swap update, e.g.
--
--   UPDATE event_seat_inventory
--   SET status = 'held', hold_token = $1, held_by_user_id = $2, hold_expires_at = $3
--   WHERE id = $4 AND status = 'available';
--
-- If that UPDATE affects 0 rows, someone else got there first — no
-- exceptions, no race window, no separate "version" column needed (the
-- status column itself is the compare-and-swap guard). Redis is used
-- ahead of this in the purchase path purely to make that contention fast
-- and to power the waiting room (see the inventory-locking service, next
-- task) — Postgres is what we actually trust to never oversell.
CREATE TABLE event_seat_inventory (
    id                        UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id                 UUID NOT NULL,
    event_id                  UUID NOT NULL,
    event_ticket_category_id  UUID NOT NULL,
    -- NULL for general-admission units (no assigned physical seat).
    seat_id                   UUID,
    status                    TEXT NOT NULL DEFAULT 'available'
                               CHECK (status IN ('available', 'held', 'sold', 'void')),
    hold_token                UUID,
    held_by_user_id           UUID REFERENCES users(id) ON DELETE SET NULL,
    hold_expires_at           TIMESTAMPTZ,
    created_at                TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at                TIMESTAMPTZ NOT NULL DEFAULT now(),

    FOREIGN KEY (event_id, tenant_id) REFERENCES events(id, tenant_id) ON DELETE CASCADE,
    FOREIGN KEY (event_ticket_category_id, tenant_id)
        REFERENCES event_ticket_categories(id, tenant_id) ON DELETE RESTRICT,
    -- Postgres' default MATCH SIMPLE means this FK check is skipped
    -- entirely when seat_id IS NULL — exactly what we want for GA units.
    FOREIGN KEY (seat_id, tenant_id) REFERENCES seats(id, tenant_id) ON DELETE RESTRICT,

    UNIQUE (id, tenant_id),
    -- A physical seat can only ever be sold once per event. (GA rows have
    -- seat_id NULL and are exempt — Postgres does not enforce uniqueness
    -- across NULLs — which is correct: many interchangeable GA units are
    -- expected per event.)
    UNIQUE (event_id, seat_id),

    -- A row claims to be "held" only while it actually carries hold
    -- metadata, and vice versa. This closes off a whole bug class where a
    -- hold is released but its token/expiry is left dangling (which would
    -- corrupt the expiry-sweep count) or "held" is set without a token.
    CHECK (
        (status = 'held' AND hold_token IS NOT NULL AND hold_expires_at IS NOT NULL)
        OR
        (status <> 'held' AND hold_token IS NULL AND hold_expires_at IS NULL AND held_by_user_id IS NULL)
    )
);

CREATE INDEX idx_esi_event_status ON event_seat_inventory(event_id, status);
CREATE INDEX idx_esi_tenant_id ON event_seat_inventory(tenant_id);
CREATE INDEX idx_esi_category ON event_seat_inventory(event_ticket_category_id);
-- Partial index powering the hold-expiry sweep (worker): only 'held' rows
-- are indexed, so the sweep's "find expired holds" query stays cheap even
-- with millions of 'sold'/'available' rows in the table.
CREATE INDEX idx_esi_hold_expiry ON event_seat_inventory(hold_expires_at) WHERE status = 'held';

CREATE TRIGGER trg_esi_updated_at
    BEFORE UPDATE ON event_seat_inventory
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();
