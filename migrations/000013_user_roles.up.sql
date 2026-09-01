-- Adds the minimum needed to distinguish a platform-wide fan account from
-- a tenant-scoped club-staff/admin account. role is deliberately just
-- 'fan' | 'admin' for now — a real RBAC system (granular permissions,
-- multiple staff roles per club) is future work; this unblocks admin
-- endpoints existing at all.
--
-- tenant_id is NULL for fans (platform-wide, as designed in migration
-- 000003) and set for admin accounts, scoping them to the one club they
-- administer. A platform-wide superadmin role is not modeled here.
ALTER TABLE users ADD COLUMN role TEXT NOT NULL DEFAULT 'fan' CHECK (role IN ('fan', 'admin'));
ALTER TABLE users ADD COLUMN tenant_id UUID REFERENCES tenants(id) ON DELETE CASCADE;

-- An admin account must belong to a tenant; a fan account must not.
ALTER TABLE users ADD CONSTRAINT users_role_tenant_consistency CHECK (
    (role = 'admin' AND tenant_id IS NOT NULL) OR (role = 'fan' AND tenant_id IS NULL)
);

CREATE INDEX idx_users_tenant_id ON users(tenant_id) WHERE tenant_id IS NOT NULL;
