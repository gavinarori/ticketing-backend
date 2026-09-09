-- Transactional outbox: domain transactions insert a row here in the
-- same Postgres transaction as the business write, and the worker
-- publishes to Kafka afterwards. That way a paid order still produces
-- tickets even if the broker is down — the event is durable in Postgres
-- until publish succeeds. Deliberately NOT tenant-scoped / RLS'd: the
-- worker must read every unpublished row regardless of tenant.

CREATE TABLE outbox (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    topic        TEXT NOT NULL,
    payload      JSONB NOT NULL,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    published_at TIMESTAMPTZ,
    attempts     INTEGER NOT NULL DEFAULT 0 CHECK (attempts >= 0)
);
CREATE INDEX idx_outbox_unpublished ON outbox(created_at) WHERE published_at IS NULL;
