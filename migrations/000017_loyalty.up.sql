-- Loyalty is a running point balance per (tenant, user), with an
-- append-only ledger so awards/redemptions are auditable. Points are
-- awarded when an order is paid (see internal/service/order).

CREATE TABLE loyalty_accounts (
    tenant_id   UUID NOT NULL REFERENCES tenants(id) ON DELETE RESTRICT,
    user_id     UUID NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    points      BIGINT NOT NULL DEFAULT 0 CHECK (points >= 0),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, user_id)
);

CREATE TABLE loyalty_ledger (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id   UUID NOT NULL,
    user_id     UUID NOT NULL,
    delta       BIGINT NOT NULL,
    reason      TEXT NOT NULL,
    order_id    UUID,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    FOREIGN KEY (tenant_id, user_id) REFERENCES loyalty_accounts(tenant_id, user_id) ON DELETE CASCADE
);
CREATE INDEX idx_loyalty_ledger_user ON loyalty_ledger(tenant_id, user_id, created_at DESC);

ALTER TABLE loyalty_accounts ENABLE ROW LEVEL SECURITY;
ALTER TABLE loyalty_ledger   ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON loyalty_accounts
    USING (tenant_id = current_setting('app.current_tenant_id', true)::uuid);
CREATE POLICY tenant_isolation ON loyalty_ledger
    USING (tenant_id = current_setting('app.current_tenant_id', true)::uuid);
