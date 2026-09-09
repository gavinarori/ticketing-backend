-- Expand club-staff roles beyond a single "admin" and add hashed,
-- single-use tokens for email verification and password reset.
-- Fans remain platform-wide (tenant_id IS NULL); every staff role is
-- tenant-scoped. The CHECK pairing is the same invariant as 000013,
-- widened to the new roles.

ALTER TABLE users DROP CONSTRAINT IF EXISTS users_role_check;
ALTER TABLE users DROP CONSTRAINT IF EXISTS users_role_tenant_consistency;

ALTER TABLE users ADD CONSTRAINT users_role_check
    CHECK (role IN ('fan', 'admin', 'box_office', 'scanner'));

ALTER TABLE users ADD CONSTRAINT users_role_tenant_consistency CHECK (
    (role = 'fan' AND tenant_id IS NULL)
    OR (role IN ('admin', 'box_office', 'scanner') AND tenant_id IS NOT NULL)
);

CREATE TABLE auth_tokens (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id     UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    purpose     TEXT NOT NULL CHECK (purpose IN ('email_verify', 'password_reset')),
    token_hash  TEXT NOT NULL UNIQUE,
    expires_at  TIMESTAMPTZ NOT NULL,
    consumed_at TIMESTAMPTZ,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_auth_tokens_user_purpose ON auth_tokens(user_id, purpose)
    WHERE consumed_at IS NULL;
