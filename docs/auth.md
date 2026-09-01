# Auth & identity

Registration, login, refresh-token rotation, logout, JWT-based
authentication middleware, and a one-off tenant-admin bootstrap path.
Validated by running the real compiled binary against real Postgres +
Redis and driving it with `curl`, then locked in with an integration test
that reproduces the exact HTTP conditions that surfaced a real bug (see
below) — not just service-level unit tests.

## Design

- **Fans are platform-wide** (`role='fan'`, `tenant_id=NULL`); **admins
  are tenant-scoped** (`role='admin'`, `tenant_id` set). The database
  enforces the pairing directly (migration `000013`'s CHECK constraint) —
  a `domain.User` can't represent an admin with no tenant or a fan with
  one.
- **Access tokens are short-lived signed JWTs** (HS256, `Claims.Role` +
  `Claims.TenantID`), verified by signature alone — `ParseAccessToken`
  makes no database call, which is the entire point of using a token
  here instead of a server-side session lookup on every request.
- **Refresh tokens are opaque random strings**, never JWTs. Only a
  SHA-256 hash is persisted (`hashToken`) — deliberately not bcrypt; a
  refresh token is already 256 bits of random data, not a human-chosen
  password, so it needs no slow salted KDF, and using one would add
  needless latency to every refresh call.
- **Refresh tokens rotate on every use.** `RefreshAccessToken` revokes
  the token it was just given before issuing a new pair. If a refresh
  token is ever stolen, the thief and the legitimate client are racing to
  use it first; whichever loses gets `ErrRefreshRevoked` on their very
  next attempt instead of a silently-still-valid stolen credential.
  Verified directly: `TestRefreshAccessToken_RotatesAndOldTokenBecomesUnusable`
  (unit) and the reuse assertion inside `TestAuthFlow_ThroughRealHTTP`
  (integration, through real HTTP).
- **Login never distinguishes "no such account" from "wrong password."**
  Both return the same `ErrInvalidCredentials`, and a nonexistent-account
  login still runs a dummy `bcrypt.CompareHashAndPassword` call so the
  response *timing* doesn't leak the distinction either — a real,
  specific defense against account enumeration, not a generic gesture at
  "security."
- **The admin-bootstrap chicken-and-egg problem** (every admin-creation
  path requires an existing admin — so how does a tenant's *first* admin
  get created?) is solved with `POST /api/v1/admin/bootstrap`, gated by a
  shared secret (`X-Bootstrap-Secret` header vs `ADMIN_BOOTSTRAP_SECRET`)
  instead of a JWT. An unset secret disables the endpoint entirely — 404,
  not 401 — so its existence isn't even revealed in a default
  configuration. Verified: `TestBootstrapAdmin_DisabledWhenSecretUnset`.

## A real bug, found by actually running this — not by review

The first live `curl` login attempt against the running server returned
a bare 500. The cause: `clientIP(r)` fell back to `r.RemoteAddr` when no
`X-Forwarded-For` header was present, and `RemoteAddr` is always
`"host:port"` — but `refresh_tokens.ip_address` is a real Postgres
`INET` column, which correctly rejects a port-suffixed value as an
invalid address literal. Every login attempt without a reverse proxy in
front of it (i.e. every local/dev/test request) was failing at the
database layer.

**This class of bug is exactly why this round was validated against a
live process and real Postgres instead of only unit tests with fakes** —
a fake `RefreshTokenRepository` would never have modeled the `INET`
column's validation at all, so the bug would have shipped invisibly past
every unit test.

Fixed with `net.SplitHostPort` to correctly strip the port before the
value ever reaches the database, and locked in two ways:
1. `TestAuthFlow_ThroughRealHTTP` drives the real router via
   `httptest.NewServer` (whose request `RemoteAddr` is `"host:port"`,
   identical to a real listener's) against real Postgres — the same
   conditions that surfaced the bug, reproduced deliberately.
2. While fixing it, `AuthHandler` turned out to have no logger at all —
   every internal error was silently swallowed into a bare "internal
   server error" with zero detail in the logs, which is exactly what
   made this bug slow to diagnose by hand. Added `zap.Error(err)` logging
   at every internal-error branch so the next one won't require manual
   `curl` archaeology to find.

## What was actually validated

- Whole project: `go build ./...`, `go vet ./...` clean.
- 15 unit tests in `internal/service/auth` (fakes, `-race`): password
  hashing/clearing, weak-password rejection, wrong-password rejection,
  unknown-email indistinguishability, inactive-account rejection, refresh
  rotation + reuse rejection, expired-token rejection, logout revocation,
  tampered-token rejection, wrong-signing-secret rejection, admin
  registration setting role/tenant correctly.
- 3 new integration tests (real Postgres, real HTTP via `httptest`,
  `-race`) plus the 8 from prior rounds — **11 total, 3 consecutive clean
  runs**.
- The exact HTTP flow driven by hand against the live binary: register →
  duplicate-email rejected → weak-password rejected → wrong-password
  rejected → login → `/me` (with and without a token) → refresh →
  old-token-reuse rejected → admin bootstrap (wrong secret rejected,
  correct secret succeeds) → admin login returns correct `role`/
  `tenant_id` claims → logout → post-logout refresh rejected.
- Migration `000013` applies and rolls back cleanly alongside all 12
  prior migrations.

## What's deliberately not here yet

- **Fan-facing and admin-dashboard business routes** — this round wires
  auth and its middleware (`RequireAuth`, `RequireAdmin`) only; no
  handler in the codebase uses `RequireAdmin` yet because no admin
  resource endpoints exist to protect.
- **Full RBAC** — `role` is a two-value enum (`fan`/`admin`), not a
  permissions system. `RequireAdmin` checks the role but not that the
  admin's `tenant_id` matches the resource being acted on — that check
  is documented as each future handler's own responsibility.
- **"Log out everywhere"** — `RevokeAllForUser` exists on the repository
  and is exercised by nothing yet; no endpoint calls it.
- **Password reset, email verification** — `email_verified_at` exists in
  the schema and is unused; no reset-password flow exists.
- **Rate limiting on auth endpoints specifically** — the
  `internal/repository/redis.RateLimiter` built for inventory holds isn't
  wired in front of `/auth/login` yet, which is a real gap (unlimited
  login attempts against bcrypt is a viable, if slow, brute-force
  surface).
