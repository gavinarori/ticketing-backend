# Fan-facing & admin-dashboard API

The routes that turn every prior round's service-layer code into an
actual product: an admin can stand up a club, price and publish an
event, and a fan can browse, queue, hold, buy, and see their order go to
`paid` — all over real HTTP. Validated by hand with `curl` against the
live compiled binary first, then locked in with an automated integration
test that reproduces the exact same flow.

## Scope decision: GA-only publish

`PublishEvent` generates general-admission inventory (a quantity of
seat-less rows for one ticket category) — not reserved-seat inventory.
The schema has no `seat_category_id` on `venue_sections`, so there's no
way to derive "which physical seats are VIP" for reserved publish. This
is a real, stated gap, not a hidden shortcut: `docs/database-schema.md`
and `docs/order-flow.md` would both need updating (a
`section_ticket_categories` join table, most likely) before reserved-seat
publish could be built. Venue/section/seat CRUD (`VenueRepository`'s full
interface) is implemented for compile-correctness and future use, but
only `CreateVenue`/`ListVenues` are exposed over HTTP this round —
section and seat management aren't yet, since GA publish doesn't need
them.

## Two tenant-resolution mechanisms, deliberately not one

- **Admin routes** resolve their tenant from `appmw.TenantIDFromContext`
  — the admin's own JWT claim. Never a header, never a request body.
- **Fan routes** resolve their tenant from `appmw.RequireTenantHeader` —
  the `X-Tenant-ID` header.

This isn't inconsistency, it's a security boundary. An admin token is a
*privilege* to act on one specific club's data; if it read `X-Tenant-ID`
from a header, an admin token could be replayed against a different
tenant's resources by simply changing a header, defeating the entire
point of tenant scoping. A fan choosing which club's events to browse is
not a privilege at all — any fan can browse any club's public events —
so a header-based `X-Tenant-ID` is the right, and safe, mechanism there.
`middleware/tenant.go`'s doc comment states this explicitly so it isn't
lost the next time someone's tempted to unify the two "for consistency."

## What each layer actually does

- **`internal/repository/postgres/{venue,seatcategory}.go`** — real
  Postgres implementations, not stubs. `SeatCategoryRepository` didn't
  exist as a domain interface before this round; it was added because
  `AdminEventHandler.CreateSeatCategory` needed somewhere real to write
  to, not a workaround.
- **`internal/handler/http/admin_venue.go`, `admin_event.go`** — venue
  creation/listing, seat category creation, event creation, ticket
  category (pricing) creation, and publish. Every handler checks
  `appmw.TenantIDFromContext` first and 403s if absent (an admin token
  with no tenant, which shouldn't be constructible given migration
  `000013`'s CHECK constraint, but the handler doesn't trust that from a
  distance).
- **`internal/handler/http/event.go`** — public event browsing
  (`on_sale` only — draft/cancelled events never leak through this
  surface) and event detail with live per-category availability, sourced
  from `InventoryRepository.CountByStatus`.
- **`internal/handler/http/inventory.go`** — thin HTTP wrapping of
  `internal/service/inventory`: join queue, check queue status, hold,
  release. Every error branch from `HoldSeat` (not admitted, rate
  limited, lock contention, unavailable) maps to a distinct, correct HTTP
  status — nothing new is invented here, this just exposes what that
  service already guarantees.
- **`internal/handler/http/order.go`** — thin HTTP wrapping of
  `internal/service/order`: create order, authorize payment, list/get.

## What was actually validated

**By hand, against the live compiled binary, before any test was
written** — the standard this whole project has held to since the
inventory-locking round:

1. Seeded a tenant, bootstrapped an admin, logged in.
2. Admin created a venue, a seat category, an event (starts `draft`),
   priced it with a ticket category, and published 5 GA tickets — event
   moved to `on_sale`, confirmed via the response.
3. A fan registered, logged in, and browsed events **publicly, with no
   auth token** — confirmed the event appears with `available: 5`.
4. Confirmed browsing **without** `X-Tenant-ID` correctly 400s.
5. Attempted a hold **before** joining the waiting room — correctly
   403'd with `not-admitted`, and the seat's status was confirmed
   unchanged in the database.
6. Joined the queue, admitted the fan (via a direct Redis script — the
   admission-control worker loop isn't built yet, a stated gap), then
   held successfully.
7. Created an order — confirmed the total (`250000` cents) matched the
   seeded ticket category price exactly, not a hardcoded or guessed
   value.
8. Authorized payment, fired a correctly-signed webhook by hand, and
   confirmed via `curl` that the fan's own order list showed `paid`.
9. **Checked the database directly**: the inventory row was `sold`, and
   the public event listing's `available` count had dropped from 5 to 4
   — proving the whole loop, not just the last call in it.

**Then locked in as `TestFullFlow_AdminPublishesFanBuys`** (integration,
real Postgres + Redis, driven through the actual chi router via
`httptest.NewServer`) — the same sequence above, automated, plus its own
assertions on the order total, the generated-count, the 403-before-
admission, and the final `available: 2` (of 3, after one sale) on the
public listing. Run 3 consecutive times clean.

**Also re-confirmed**: the whole suite (12 integration tests, 23+ unit
tests across `auth`/`inventory`/`payment`) still passes together —
nothing this round's new handlers touch broke anything built in prior
rounds.

**A process note worth recording honestly**: mid-validation, a login
call that had worked in the previous round's testing failed again with a
500. Extensive log inspection turned up nothing — because the actual
cause was operational, not a code bug: a stale, pre-fix binary was still
bound to port 8080 from an earlier, incompletely-torn-down test run in
this sandbox, and a `go build` that appeared to succeed had actually
timed out without producing a new binary. Confirmed by checking the
binary's own compiled strings for a log message known to exist only in
the fixed source, then force-killing and rebuilding explicitly. Recorded
here because "the server you're hitting isn't the code you think it is"
is a real, recurring class of confusion worth naming rather than
quietly working around.

## What's deliberately not here yet

- **Reserved-seat publish** — see the GA-only scope note above.
- **Admission-control worker loop** — `AdmitNext` exists and is fully
  tested, but nothing calls it automatically; every test and the manual
  `curl` flow both admit fans directly rather than through a real ticking
  process. This is the same gap flagged after the inventory-locking
  round, still open.
- **Venue section/seat management over HTTP** — `VenueRepository`'s full
  interface is implemented; only venue create/list is exposed.
- **Admin views of orders/sales/refunds** — no reporting or refund
  endpoints exist; `AdminEventHandler` covers setup, not operations.
- **Pagination parameters** — every list endpoint hardcodes a limit
  (`50`) and offset (`0`); `internal/pkg/pagination` is still an unused
  stub.
