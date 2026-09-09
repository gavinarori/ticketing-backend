-- Fan-to-fan resale of an already-issued ticket. The listing does not
-- move ownership until payment is captured; check-in of a listed ticket
-- is rejected (status='listed') so a sold-but-not-transferred ticket
-- cannot be used at the gate by the original holder.

CREATE TABLE ticket_listings (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id       UUID NOT NULL,
    ticket_id       UUID NOT NULL,
    seller_user_id  UUID NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    price_cents     BIGINT NOT NULL CHECK (price_cents > 0),
    currency        TEXT NOT NULL,
    status          TEXT NOT NULL DEFAULT 'active'
                    CHECK (status IN ('active', 'sold', 'cancelled')),
    buyer_user_id   UUID REFERENCES users(id) ON DELETE RESTRICT,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    FOREIGN KEY (ticket_id, tenant_id) REFERENCES tickets(id, tenant_id) ON DELETE RESTRICT,
    UNIQUE (id, tenant_id),
    UNIQUE (ticket_id)
);
CREATE INDEX idx_ticket_listings_tenant_status ON ticket_listings(tenant_id, status);

CREATE TRIGGER trg_ticket_listings_updated_at
    BEFORE UPDATE ON ticket_listings
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

ALTER TABLE ticket_listings ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON ticket_listings
    USING (tenant_id = current_setting('app.current_tenant_id', true)::uuid);
