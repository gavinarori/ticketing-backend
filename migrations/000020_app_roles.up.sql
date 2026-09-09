-- Least-privilege login roles so RLS actually applies. Table owners
-- (the `ticketing` user that ran these migrations) bypass RLS by
-- default — that is a Postgres fact, not a bug. Application traffic
-- should connect as `app_user`; the inventory-sweep / outbox worker
-- connects as `worker_user` (BYPASSRLS) because ListExpiredHolds and
-- outbox drain are cross-tenant by design.
--
-- Passwords here are local-dev defaults. Staging/production MUST
-- CREATE/ALTER these roles with secrets from the orchestrator, not
-- these literals. The DO blocks are idempotent so `migrate up` on an
-- already-initialized volume is safe.

DO $$
BEGIN
    IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'app_user') THEN
        CREATE ROLE app_user LOGIN PASSWORD 'app_user';
    END IF;
    IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'worker_user') THEN
        CREATE ROLE worker_user LOGIN PASSWORD 'worker_user' BYPASSRLS;
    END IF;
END
$$;

GRANT USAGE ON SCHEMA public TO app_user, worker_user;
GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA public TO app_user, worker_user;
GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA public TO app_user, worker_user;
GRANT EXECUTE ON ALL FUNCTIONS IN SCHEMA public TO app_user, worker_user;

ALTER DEFAULT PRIVILEGES IN SCHEMA public
    GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO app_user, worker_user;
ALTER DEFAULT PRIVILEGES IN SCHEMA public
    GRANT USAGE, SELECT ON SEQUENCES TO app_user, worker_user;
