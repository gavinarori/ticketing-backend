-- A Ticket is the deliverable the fan actually presents at the gate.
-- One row per paid order_item. `code` is a high-entropy bearer value
-- printed as a barcode/QR; looking it up is an atomic status transition
-- so two scanners cannot admit the same ticket.

CREATE TABLE tickets (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id       UUID NOT NULL,
    order_id        UUID NOT NULL,
    order_item_id   UUID NOT NULL,
    event_id        UUID NOT NULL,
    user_id         UUID NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    inventory_id    UUID NOT NULL,
    code            TEXT NOT NULL,
    status          TEXT NOT NULL DEFAULT 'valid'
                    CHECK (status IN ('valid', 'used', 'void', 'listed', 'transferred')),
    scanned_at      TIMESTAMPTZ,
    scanned_by      UUID REFERENCES users(id) ON DELETE SET NULL,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    FOREIGN KEY (order_id, tenant_id) REFERENCES orders(id, tenant_id) ON DELETE RESTRICT,
    FOREIGN KEY (order_item_id, tenant_id) REFERENCES order_items(id, tenant_id) ON DELETE RESTRICT,
    FOREIGN KEY (event_id, tenant_id) REFERENCES events(id, tenant_id) ON DELETE RESTRICT,
    FOREIGN KEY (inventory_id, tenant_id) REFERENCES event_seat_inventory(id, tenant_id) ON DELETE RESTRICT,
    UNIQUE (id, tenant_id),
    UNIQUE (order_item_id),
    UNIQUE (code)
);
CREATE INDEX idx_tickets_tenant_user ON tickets(tenant_id, user_id);
CREATE INDEX idx_tickets_tenant_event ON tickets(tenant_id, event_id);
CREATE INDEX idx_tickets_status ON tickets(tenant_id, status);

CREATE TRIGGER trg_tickets_updated_at
    BEFORE UPDATE ON tickets
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

ALTER TABLE tickets ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON tickets
    USING (tenant_id = current_setting('app.current_tenant_id', true)::uuid);
