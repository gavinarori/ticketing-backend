# Database schema

Applies via `make migrate-up` (wraps `go run ./cmd/migrate`). Migrations live
in `migrations/`, numbered `000001`–`000011`, and were validated end-to-end
against a real Postgres 16 instance: every `.up.sql` applies cleanly in
order, every `.down.sql` reverses cleanly in reverse order back to an empty
schema, and the constraints below were exercised directly (not just
reasoned about).

## Table map

```
tenants
  └─ venues
       └─ venue_sections
            └─ seats
  └─ seat_categories
  └─ events (venue_id → venues)
       └─ event_ticket_categories (seat_category_id → seat_categories)
            └─ event_seat_inventory (seat_id → seats, nullable for GA)
                 └─ inventory_audit_log (populated by trigger, not app code)
  └─ orders (user_id → users)
       └─ order_items (event_seat_inventory_id → event_seat_inventory)
  └─ payments (order_id → orders)
  └─ memberships (user_id → users)

users (platform-wide, not tenant-scoped)
  └─ refresh_tokens
```

## Key design decisions

**Multi-tenancy: composite FK chain, not just a `tenant_id` column.**
Every tenant-scoped table stores `tenant_id` directly (denormalized) *and*
exposes `UNIQUE (id, tenant_id)` so its children reference the parent as
`FOREIGN KEY (parent_id, tenant_id) REFERENCES parent(id, tenant_id)`
instead of a plain `FOREIGN KEY (parent_id) REFERENCES parent(id)`. This
means Postgres itself refuses to let a `venue_section` attach to a `venue`
owned by a different tenant — verified directly: attempting exactly that
insert fails with `violates foreign key constraint
"venue_sections_venue_id_tenant_id_fkey"`. App-level tenant checks are
still the primary mechanism; this is a database-enforced backstop against
the specific bug of the app sending a mismatched `tenant_id`.

**Row Level Security is a second, independent backstop — enabled in
migration `000011`.** It protects against a different bug: a query that
forgot its `WHERE tenant_id = $1` clause entirely. Verified against a
genuinely non-owner role (`app_user`): zero rows visible with no tenant
context set, correct rows visible once `SET app.current_tenant_id = ...`
is run, zero rows visible with the wrong tenant set. **Important caveat,
also verified**: table owners and superusers bypass RLS by default — the
`ticketing` user created by `docker-compose.yml` owns these tables and is
*not* restricted by these policies. RLS only takes effect for a
lower-privilege application role, which staging/production should use
(and which will need `SET LOCAL app.current_tenant_id` added to the
repository layer's transaction helper — not yet built).

**Oversell prevention lives entirely in `event_seat_inventory`.** Every
purchasable ticket — reserved-seat or general-admission — is exactly one
row, created up front. A purchase attempt is one atomic conditional
update:

```sql
UPDATE event_seat_inventory
SET status = 'held', hold_token = $1, held_by_user_id = $2, hold_expires_at = $3
WHERE id = $4 AND status = 'available';
```

If that affects 0 rows, someone else already holds or bought it — verified
directly: a second concurrent hold attempt on the same row after a
successful first hold returns `UPDATE 0`. No separate optimistic-lock
`version` column is needed; `status` itself is the compare-and-swap guard.
A `CHECK` constraint additionally guarantees a row can never claim to be
`held` without carrying a token and expiry (or vice versa) — verified: an
`UPDATE ... SET status='held', hold_token=NULL` is rejected outright.

**A physical seat can only be sold once per event**, enforced by
`UNIQUE (event_id, seat_id)` — verified: inserting a duplicate
`(event_id, seat_id)` pair fails outright. General-admission rows
(`seat_id IS NULL`) are exempt from this, correctly, since many
interchangeable GA units are expected per event.

**Every inventory status change is logged automatically**, not by
application code remembering to call an audit method. An `AFTER INSERT OR
UPDATE` trigger on `event_seat_inventory` writes to the append-only
`inventory_audit_log` whenever a row is created or its `status` changes —
verified: the hold above produced exactly two audit rows,
`NULL → available` (creation) and `available → held` (the hold), with no
application code involved in either write.

**Prices are per-event, not per-category.** `seat_categories` holds
reusable tier labels ("VIP", "Terraces"); `event_ticket_categories` holds
the actual price for one category within one specific event, since a VIP
seat for a relegation six-pointer and a pre-season friendly are priced
very differently.

**Status columns are `TEXT` + `CHECK`, not native Postgres `ENUM` types.**
Adding a new status value later is a plain (fast, non-locking) migration
instead of an `ALTER TYPE ... ADD VALUE`, which has its own transactional
restrictions. Trade-off: the database won't reject a typo'd status string
at the type level the way an enum would — validation for that moves to
the Go domain layer (next task).

**IDs are UUIDs** (via `pgcrypto`'s `gen_random_uuid()`) everywhere except
`inventory_audit_log`, which uses `BIGSERIAL` — it's append-only,
high-volume, and has no anti-enumeration requirement, so a sequence is
cheaper to index and naturally orders by insertion time.

## What's deliberately not here yet

- **Waiting room / rate-limit state** — lives in Redis, not Postgres; see
  the inventory-locking service (next task).
- **Tenant-context transaction helper** (`SET LOCAL app.current_tenant_id`)
  — belongs in `internal/repository/postgres/tx.go`, part of the
  repository/service layer, not the schema itself.
- **Secondary market / resale** — explicitly called out as optional/later
  in the original scope; no tables yet.
