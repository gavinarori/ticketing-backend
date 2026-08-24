CREATE TABLE orders (
    id               UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id        UUID NOT NULL REFERENCES tenants(id) ON DELETE RESTRICT,
    user_id          UUID NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    status           TEXT NOT NULL DEFAULT 'pending'
                      CHECK (status IN ('pending', 'awaiting_payment', 'paid', 'cancelled', 'expired', 'refunded')),
    -- Client-supplied key that makes "create order" safe to retry after a
    -- timeout without risking a duplicate order — exactly the kind of bug
    -- a flaky mobile connection mid-drop would otherwise cause.
    idempotency_key  TEXT NOT NULL,
    currency         TEXT NOT NULL DEFAULT 'KES',
    subtotal_cents   BIGINT NOT NULL DEFAULT 0 CHECK (subtotal_cents >= 0),
    fees_cents       BIGINT NOT NULL DEFAULT 0 CHECK (fees_cents >= 0),
    total_cents      BIGINT NOT NULL DEFAULT 0 CHECK (total_cents >= 0),
    -- Mirrors the tightest hold_expires_at among this order's items — the
    -- order (and its held inventory) is abandoned if payment doesn't land
    -- before this.
    expires_at       TIMESTAMPTZ,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (id, tenant_id),
    UNIQUE (tenant_id, idempotency_key)
);
CREATE INDEX idx_orders_tenant_id ON orders(tenant_id);
CREATE INDEX idx_orders_user_id ON orders(user_id);
CREATE INDEX idx_orders_status ON orders(status);

CREATE TRIGGER trg_orders_updated_at
    BEFORE UPDATE ON orders
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE TABLE order_items (
    id                       UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id                UUID NOT NULL,
    order_id                 UUID NOT NULL,
    event_seat_inventory_id  UUID NOT NULL,
    unit_price_cents         BIGINT NOT NULL CHECK (unit_price_cents >= 0),
    currency                 TEXT NOT NULL DEFAULT 'KES',
    created_at               TIMESTAMPTZ NOT NULL DEFAULT now(),
    FOREIGN KEY (order_id, tenant_id) REFERENCES orders(id, tenant_id) ON DELETE CASCADE,
    FOREIGN KEY (event_seat_inventory_id, tenant_id)
        REFERENCES event_seat_inventory(id, tenant_id) ON DELETE RESTRICT,
    UNIQUE (id, tenant_id)
    -- Deliberately NOT UNIQUE(event_seat_inventory_id): the same inventory
    -- row can legitimately appear in more than one *historical*
    -- order_item over its lifetime (held by order A, A expires and
    -- releases it, later sold via order B). It's the inventory row's own
    -- `status` column — not this table — that prevents a seat being live
    -- in two orders at once.
);
CREATE INDEX idx_order_items_order_id ON order_items(order_id);
CREATE INDEX idx_order_items_tenant_id ON order_items(tenant_id);
CREATE INDEX idx_order_items_inventory_id ON order_items(event_seat_inventory_id);
