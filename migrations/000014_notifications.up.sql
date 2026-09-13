-- notifications is a transactional outbox: rows are written inside the
-- SAME database transaction as the business event they describe (see
-- internal/service/order.Service.ConfirmPayment, which inserts an
-- order_confirmation row alongside marking the order 'paid'). That
-- atomicity is the entire point — it makes "the order is paid but no
-- confirmation was ever queued" a schema-level impossibility, the same
-- way earlier migrations made "sold without a valid hold" impossible.
-- A separate worker loop (see internal/service/notification and
-- cmd/worker) polls for 'pending' rows and actually sends them,
-- decoupled from the request path that created them.
CREATE TABLE notifications (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id   UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    user_id     UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    type        TEXT NOT NULL CHECK (type IN ('order_confirmation')),
    payload     JSONB NOT NULL,
    status      TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'sent', 'failed')),
    attempts    INTEGER NOT NULL DEFAULT 0,
    last_error  TEXT,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    sent_at     TIMESTAMPTZ
);

-- Partial index: only 'pending' rows are ever scanned by the dispatch
-- loop's "find work" query, so this stays cheap regardless of how many
-- 'sent' rows accumulate over the platform's lifetime — same technique
-- as idx_esi_hold_expiry in migrations/000006_inventory.up.sql.
CREATE INDEX idx_notifications_pending ON notifications(created_at) WHERE status = 'pending';
CREATE INDEX idx_notifications_tenant_id ON notifications(tenant_id);
