# Inventory locking service

Implements the seat-purchase hot path: waiting room → rate limit → Redis
lock → Postgres compare-and-swap. Validated end-to-end against real
Postgres 16 and real Redis 7 (not just unit-tested against fakes) — see
"What was actually validated" below.

## Layering

```
internal/service/inventory/     orchestration — HoldSeat, ReleaseHold,
                                 ConfirmSale, SweepExpiredHolds, waiting
                                 room pass-through
internal/repository/redis/      Locker, WaitingRoom, RateLimiter
internal/repository/postgres/   InventoryRepo — implements
                                 domain.InventoryRepository
internal/domain/inventory.go    the interface + entity (already built)
```

**The one fact that governs every design decision here: Redis is not
what prevents overselling.** The Postgres compare-and-swap —
`UPDATE event_seat_inventory SET status = 'held', ... WHERE id = $1 AND
status = 'available'` — is the entire correctness guarantee, and it would
hold even with every Redis component in this service deleted. Everything
Redis does exists purely to make contention *cheap and fast*, and to keep
requests that are already doomed (rate-limited, not yet admitted) from
reaching Postgres at all. This distinction shows up directly in the code:
`Hold`/`Release`/`ConfirmSale` all return `(bool, error)`, and losing the
race (`false, nil`) is modeled as ordinary control flow everywhere, never
as something to retry or log as a failure.

## HoldSeat: four gates, increasing cost

```go
func (s *Service) HoldSeat(ctx, tenantID, eventID, inventoryID, userID) (*HoldResult, error)
```

1. **Waiting room admission** (`redis.WaitingRoom.IsAdmitted` — one
   `EXISTS`). Rejects fans who haven't been let through yet.
2. **Rate limit** (`redis.RateLimiter.Allow` — one `INCR`). Blunts a
   single client hammering the endpoint.
3. **Per-row Redis lock** (`redis.Locker.Acquire` — one `SETNX`). This is
   the interesting one: Postgres's row-level locking during an `UPDATE`
   already serializes concurrent writers to the same row correctly on its
   own — the Redis lock isn't needed for correctness. It exists because,
   for a genuinely hot seat (the one everyone wants), N concurrent
   requests would otherwise all pay a full Postgres round trip and briefly
   contend for the same row's internal lock, when only one of them can
   ever win. A cheap in-memory `SETNX` check rejects the other N-1 before
   they ever reach the database. Losing this step returns
   `ErrLockContention` — deliberately not retried automatically; the
   frontend is expected to try a different seat or poll, not spin on one
   contested row.
4. **The Postgres CAS itself** — the only step that can authoritatively
   say the hold succeeded. A caller that clears step 3 but loses here gets
   `domain.ErrUnavailable`, exactly as if Redis weren't in the picture.

## Waiting room design

Two Redis structures per event:
- `waitingroom:queue:{eventID}` — a sorted set; FIFO via `ZADD`/`ZRANK`/
  `ZPOPMIN`.
- `waitingroom:admitted:{eventID}:{userID}` — a plain key with a TTL,
  present only during a fan's admission window.

**Score is a Redis-`INCR`'d sequence number, not a timestamp.** Wall clock
time can skew slightly between app instances under load, which could let
a fan who joined later get a numerically earlier score than one who
joined first — unfair, and hard to notice in testing since it only shows
up under real multi-instance skew. A shared counter can't drift that way.

**Known simplification, stated rather than hidden:** a fan admitted via
`Admit` who doesn't complete a hold within `AdmissionTTL` is evicted, not
automatically requeued. A production system would likely re-add them near
the front of the queue on expiry. `Admit`'s admission-rate policy (how
many fans to let through, how often) is also deliberately left to the
caller — a future admission-control loop in `cmd/worker` would derive it
from real available-inventory counts via `CountByStatus`.

## Distributed lock: explicitly not Redlock

`redis.Locker` is a single-Redis-node `SET NX PX` lock with a Lua
check-and-delete unlock (`GET` then `DEL` only if the value still matches
this caller's token — prevents releasing a lock that expired and was
re-acquired by someone else). This is **not** the multi-node Redlock
algorithm, and that's a deliberate trade-off: since the lock is an
optimization rather than a correctness mechanism (see above), a lost lock
degrades to "extra contention hits Postgres's own CAS", never to an
oversold seat. Redlock would be justified the moment this lock became
load-bearing for correctness; today it isn't.

## Rate limiter: fixed window, not sliding

`redis.RateLimiter.Allow` is a plain `INCR` + `EXPIRE`. It allows up to
~2x the stated limit right at a window boundary — a known, accepted
imprecision. The limiter exists to stop scripted hammering of the hold
endpoint, not to enforce an exact business quota, and (again) Postgres's
CAS is unaffected by how generous the limiter is at the edges.

## Transaction propagation, ready for the order-creation flow

`internal/repository/postgres/tx.go` implements an executor-in-context
pattern: `WithTx(ctx, pool, fn)` begins a transaction and stashes it in
`ctx`; every repository method resolves its executor via `db(ctx, pool)`,
which returns the ambient transaction if present, otherwise the pool
directly. This means `InventoryRepo.ConfirmSale` already works correctly
today, standalone — and will work correctly, unchanged, when the
order-creation service (next task) calls it inside the same transaction as
order/order_item inserts, by simply running it inside `WithTx`. No changes
to this package will be needed to wire that up.

The same file's `SetTenantContext` issues `set_config('app.current_tenant_id',
..., true)` — the parameterized equivalent of `SET LOCAL` — so RLS
(migration `000011`) actually applies once a lower-privilege application
role is in use. Not yet called anywhere; that wiring belongs to request
middleware in a later task.

## What was actually validated

Not just written and reasoned about — run:

- **`go build` / `go vet`** on `internal/domain`, `internal/repository/...`,
  `internal/service/...` against the real dependency graph (pgx, go-redis,
  google/uuid resolved for real, not assumed).
- **10 unit tests** (`internal/service/inventory/service_test.go`) against
  in-memory fakes, run with `-race`: admission gating, rate-limit gating,
  lock-contention gating, successful hold, hold-already-taken, lock
  release on failure, wrong-token release/confirm, and a 50-goroutine race
  against the fake repository.
- **4 integration tests** (`test/integration/inventory_test.go`, build tag
  `integration`) against a real Postgres 16 + real Redis 7, run with
  `-race`, **6 consecutive times with zero flakes**:
  - `TestHoldSeat_NeverOversells` — 100 goroutines, released simultaneously
    via a shared gate channel, all racing for the exact same seat.
    **Exactly 1 succeeds, every time**, and the database is checked
    afterward to confirm its `hold_token` matches the winning goroutine's
    token.
  - `TestHoldSeat_NotAdmitted_NeverReachesPostgres` — confirms an
    unadmitted fan is rejected without the seat's status changing at all.
  - `TestHoldThenReleaseThenReHold` — full lifecycle: hold, blocked second
    hold, release, successful second hold.
  - `TestSweepExpiredHolds_ReclaimsAgainstRealPostgres` — the worker sweep
    path against a real expired `hold_expires_at`.

One real bug this process caught, worth recording rather than quietly
fixing: the first integration test run failed with a foreign key
violation on `held_by_user_id`, because the test seeded fake user UUIDs
without inserting corresponding `users` rows. That's the schema's FK
constraint working correctly — the fix was in the test (seed real user
rows), not the schema.

## What's deliberately not here yet

- **Order-creation flow** calling `ConfirmSale` inside a shared
  transaction with order/order_item/payment writes — next task.
- **Admission-control loop** computing a real `AdmitNext` rate from
  `CountByStatus` — currently a manual/external call.
- **HTTP handlers** exposing `HoldSeat`/`JoinQueue`/`QueueStatus` — no
  routes are wired yet; this is service-layer only.
