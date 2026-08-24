-- Row Level Security is defense-in-depth, not the primary isolation
-- mechanism (that's the composite-FK chain from migrations 000002-000009).
-- RLS protects against a different, very real bug class: a query that
-- forgot its `WHERE tenant_id = $1` clause. With RLS enabled, Postgres
-- enforces that filter even then, as long as the app sets
-- app.current_tenant_id at the start of each transaction, e.g.:
--
--   SET LOCAL app.current_tenant_id = '3fa85f64-5717-...';
--
-- (That SET LOCAL call belongs in a repository-layer transaction helper —
-- e.g. internal/repository/postgres/tx.go — added alongside the
-- service/repository layer in a future task; this migration only
-- prepares the database side.)
--
-- If app.current_tenant_id is never set in a session, current_setting(...,
-- true) returns NULL, the equality check evaluates to NULL (not TRUE), and
-- RLS hides every row — a forgotten SET LOCAL fails closed, not open.
--
-- Important limitation to know about: table owners and superusers bypass
-- RLS entirely by default. The default local-dev Postgres role created by
-- docker-compose.yml (POSTGRES_USER=ticketing) owns these tables and will
-- NOT be restricted by these policies — you will only see RLS take effect
-- against a separate, non-owner role with rowsecurity forced on
-- (`ALTER TABLE ... FORCE ROW LEVEL SECURITY` for owner-inclusive testing,
-- or a dedicated least-privilege `app_user` role in staging/production).
-- This is expected: RLS here guards against application bugs, not a fully
-- compromised database credential.

ALTER TABLE venues                  ENABLE ROW LEVEL SECURITY;
ALTER TABLE venue_sections          ENABLE ROW LEVEL SECURITY;
ALTER TABLE seats                   ENABLE ROW LEVEL SECURITY;
ALTER TABLE seat_categories         ENABLE ROW LEVEL SECURITY;
ALTER TABLE events                  ENABLE ROW LEVEL SECURITY;
ALTER TABLE event_ticket_categories ENABLE ROW LEVEL SECURITY;
ALTER TABLE event_seat_inventory    ENABLE ROW LEVEL SECURITY;
ALTER TABLE orders                  ENABLE ROW LEVEL SECURITY;
ALTER TABLE order_items             ENABLE ROW LEVEL SECURITY;
ALTER TABLE payments                ENABLE ROW LEVEL SECURITY;
ALTER TABLE memberships             ENABLE ROW LEVEL SECURITY;
ALTER TABLE inventory_audit_log     ENABLE ROW LEVEL SECURITY;

CREATE POLICY tenant_isolation ON venues
    USING (tenant_id = current_setting('app.current_tenant_id', true)::uuid);
CREATE POLICY tenant_isolation ON venue_sections
    USING (tenant_id = current_setting('app.current_tenant_id', true)::uuid);
CREATE POLICY tenant_isolation ON seats
    USING (tenant_id = current_setting('app.current_tenant_id', true)::uuid);
CREATE POLICY tenant_isolation ON seat_categories
    USING (tenant_id = current_setting('app.current_tenant_id', true)::uuid);
CREATE POLICY tenant_isolation ON events
    USING (tenant_id = current_setting('app.current_tenant_id', true)::uuid);
CREATE POLICY tenant_isolation ON event_ticket_categories
    USING (tenant_id = current_setting('app.current_tenant_id', true)::uuid);
CREATE POLICY tenant_isolation ON event_seat_inventory
    USING (tenant_id = current_setting('app.current_tenant_id', true)::uuid);
CREATE POLICY tenant_isolation ON orders
    USING (tenant_id = current_setting('app.current_tenant_id', true)::uuid);
CREATE POLICY tenant_isolation ON order_items
    USING (tenant_id = current_setting('app.current_tenant_id', true)::uuid);
CREATE POLICY tenant_isolation ON payments
    USING (tenant_id = current_setting('app.current_tenant_id', true)::uuid);
CREATE POLICY tenant_isolation ON memberships
    USING (tenant_id = current_setting('app.current_tenant_id', true)::uuid);
CREATE POLICY tenant_isolation ON inventory_audit_log
    USING (tenant_id = current_setting('app.current_tenant_id', true)::uuid);
