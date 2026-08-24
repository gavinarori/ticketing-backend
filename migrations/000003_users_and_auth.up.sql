-- Users are platform-wide, not tenant-scoped: the same fan account should
-- be able to buy tickets from multiple clubs without creating a separate
-- login per tenant. A user's relationship to a given tenant (e.g. season
-- ticket holder) is expressed later via memberships, not by scoping the
-- account itself.
CREATE TABLE users (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    email               CITEXT NOT NULL UNIQUE,
    phone               TEXT,
    -- Nullable: a user created via a future OAuth/SSO integration may
    -- never have a local password.
    password_hash       TEXT,
    first_name          TEXT NOT NULL,
    last_name           TEXT NOT NULL,
    status              TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'suspended', 'deleted')),
    email_verified_at   TIMESTAMPTZ,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TRIGGER trg_users_updated_at
    BEFORE UPDATE ON users
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- Refresh tokens are persisted (as hashes, never raw) so they can be
-- individually revoked — logout-everywhere, a suspected compromised
-- device, etc. — without needing short-lived-enough access tokens to be
-- the only line of defense.
CREATE TABLE refresh_tokens (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id      UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    token_hash   TEXT NOT NULL UNIQUE,   -- SHA-256 of the actual refresh token; the raw value is never stored
    user_agent   TEXT,
    ip_address   INET,
    expires_at   TIMESTAMPTZ NOT NULL,
    revoked_at   TIMESTAMPTZ,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_refresh_tokens_user_id ON refresh_tokens(user_id);
-- Partial index: only unexpired, unrevoked tokens are relevant to the hot
-- "is this refresh token still valid" lookup path.
CREATE INDEX idx_refresh_tokens_active ON refresh_tokens(expires_at) WHERE revoked_at IS NULL;
