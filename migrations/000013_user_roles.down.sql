ALTER TABLE users DROP CONSTRAINT users_role_tenant_consistency;
DROP INDEX IF EXISTS idx_users_tenant_id;
ALTER TABLE users DROP COLUMN tenant_id;
ALTER TABLE users DROP COLUMN role;
