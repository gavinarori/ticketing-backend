DROP TABLE IF EXISTS auth_tokens;

ALTER TABLE users DROP CONSTRAINT IF EXISTS users_role_tenant_consistency;
ALTER TABLE users DROP CONSTRAINT IF EXISTS users_role_check;

ALTER TABLE users ADD CONSTRAINT users_role_check
    CHECK (role IN ('fan', 'admin'));
ALTER TABLE users ADD CONSTRAINT users_role_tenant_consistency CHECK (
    (role = 'admin' AND tenant_id IS NOT NULL) OR (role = 'fan' AND tenant_id IS NULL)
);
