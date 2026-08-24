CREATE TABLE payments (
    id                   UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id            UUID NOT NULL,
    order_id             UUID NOT NULL,
    provider             TEXT NOT NULL CHECK (provider IN ('stripe', 'adyen')),
    provider_payment_id  TEXT,     -- provider's charge/payment-intent ID, populated once known
    status               TEXT NOT NULL DEFAULT 'pending'
                          CHECK (status IN ('pending', 'authorized', 'captured', 'failed', 'refunded', 'partially_refunded')),
    amount_cents         BIGINT NOT NULL CHECK (amount_cents >= 0),
    currency             TEXT NOT NULL DEFAULT 'KES',
    -- Guards the payment-creation call the same way orders.idempotency_key
    -- guards order creation — critical on a flaky mobile network mid-drop,
    -- where a retried request must never result in a second charge.
    idempotency_key      TEXT NOT NULL,
    raw_response         JSONB,    -- last raw provider response, kept for support/debugging
    created_at           TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at           TIMESTAMPTZ NOT NULL DEFAULT now(),
    FOREIGN KEY (order_id, tenant_id) REFERENCES orders(id, tenant_id) ON DELETE RESTRICT,
    UNIQUE (id, tenant_id),
    UNIQUE (tenant_id, idempotency_key),
    -- Multiple NULL provider_payment_id values are allowed (Postgres
    -- treats NULLs as distinct under UNIQUE) — fine, since it's only
    -- populated once the provider responds.
    UNIQUE (provider, provider_payment_id)
);
CREATE INDEX idx_payments_order_id ON payments(order_id);
CREATE INDEX idx_payments_tenant_id ON payments(tenant_id);

CREATE TRIGGER trg_payments_updated_at
    BEFORE UPDATE ON payments
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();
