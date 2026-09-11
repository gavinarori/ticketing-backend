# Worker: sweep and admission loops

Closes the gap flagged after the fan/admin API round: `SweepExpiredHolds`
and `AdmitNext` existed and were fully tested, but nothing invoked either
automatically. `cmd/worker` is no longer a skeleton — it runs both as
independent ticker-driven goroutines, and was proven live: seeded a
realistic contention scenario, launched the actual compiled binary,
watched it act with zero manual intervention, and verified the database
and Redis state afterward.

## Two loops, deliberately independent

`runSweepLoop` and `runAdmissionLoop` run as separate goroutines, each
with its own ticker and its own error handling — a slow or failing sweep
tick never blocks or delays an admission tick, and vice versa. Both
respect the same `context.Context` cancellation for shutdown, tracked via
one `sync.WaitGroup` so `run()` can wait for whichever tick is in flight
to finish cleanly before the process exits — verified directly (see
below): a `SIGTERM` sent mid-run resulted in `"shutdown signal received,
waiting for in-flight ticks to finish"` immediately followed by a clean
exit, not an abrupt kill.

## The admission policy lives in its own package, on purpose

`internal/service/admission` is new this round. `invsvc.Service.AdmitNext`
was deliberately built, several rounds ago, to accept a caller-supplied
count rather than compute one itself — its own doc comment says "how
exactly that admission rate is computed is a policy decision left to the
caller." This package is that caller. The policy implemented —
`min(queue length, total available inventory, a per-tick cap)` — is a
starting heuristic, stated as such in the code: it doesn't model
conversion rates, doesn't back off under sustained contention, and
doesn't prioritize across tenants sharing one worker tick. Good enough to
make the waiting room self-operating; not a tuned capacity-planning
model.

The per-tick cap (`AdmissionMaxPerTick`, default 200) exists specifically
so one event with a massive queue can't starve every other tenant's
admission processing in the same tick — verified directly by
`TestAdmission_RunOnce_RespectsMaxPerTick`, which seeds far more queue
depth and inventory than the cap and confirms exactly the cap is admitted,
not more.

## A real, small gap this round closed along the way

`domain.TenantRepository` had no Postgres implementation until this
round — tenants had only ever been inserted directly via raw SQL in
tests and manual seeding. The admission worker needs to discover which
tenants to check for on-sale events, so `internal/repository/postgres/tenant.go`
was built for real, not stubbed.

## What was actually validated

**Automated, against real Postgres + Redis:**
- 4 new integration tests in `test/integration/worker_test.go`:
  - `TestAdmission_RunOnce_AdmitsUpToAvailableInventory` — 3 fans queued,
    only 2 seats available; confirms exactly 2 are admitted (bounded by
    availability, not queue depth) and the third is correctly still
    waiting.
  - `TestAdmission_RunOnce_SkipsEventsWithEmptyQueue` — the common case:
    an on-sale event nobody is waiting for is a no-op, not an error.
  - `TestAdmission_RunOnce_RespectsMaxPerTick` — the per-tick cap holds
    even with more queue and inventory available than the cap.
  - `TestSweepLoop_ReclaimsAcrossMultipleEvents` — proves the sweep is
    genuinely platform-wide: two entirely separate tenants' expired
    holds are both reclaimed in a single call, with no tenant/event
    filter needed.
- The full existing suite (16 integration tests total, 23+ unit tests)
  re-run clean alongside the new ones — nothing this round touched broke
  prior rounds' guarantees.
- All 13 migrations still apply and roll back cleanly.

**By hand, against the actual compiled `cmd/worker` binary** — same
standard held throughout this project:
1. Seeded a realistic contention scenario directly: one event with 2
   available seats, a third seat already `held` with an
   **already-expired** hold, and 3 fans placed in the Redis waiting room
   queue — all before starting the worker.
2. Built the real binary, confirmed via `strings` that the log messages
   from the actual source were present (not a stale binary — a lesson
   learned the hard way in the previous round), and launched it with
   fast tick intervals (2s admission, 3s sweep) purely to observe
   multiple cycles quickly.
3. **Watched it self-operate with zero further input**: tick 1 admitted
   exactly 2 of the 3 waiting fans (bounded by the 2 available seats,
   exactly as designed); tick 2 (sweep) reclaimed the expired hold,
   bringing availability to 3; **tick 3 — with no manual intervention —
   automatically admitted the third, previously-blocked fan**, having
   picked up on the sweep's freed capacity on its own. This is the
   two-loops-working-together behavior the whole task existed to prove,
   caught live rather than asserted.
4. Verified the final state directly: all 3 inventory rows correctly
   `available` (admission grants permission to attempt a hold — it does
   not hold anything itself), all 3 fans showing as admitted in Redis,
   the waiting-room queue empty, and the `inventory_audit_log` table
   showing the automatic trigger correctly recorded both the seeded hold
   and the sweep's release with real timestamps matching the worker's
   own tick.
5. Sent `SIGTERM` mid-run and confirmed graceful shutdown: the log shows
   the shutdown message followed immediately by a clean process exit —
   no hang, no abrupt kill required.

## What's deliberately not here yet

- **Kafka consumers** — `cmd/worker`'s doc comment already scoped this
  out; still true. No async event bus work has started.
- **Tuned admission policy** — see above; the current policy is a
  correct, simple starting point, not a modeled one.
- **Per-tenant/per-event configurable tick rates** — one global interval
  for all tenants sharing this worker process.
- **Worker observability** — logs only, same gap as the rest of the
  project; no metrics on tick duration, admission throughput, or sweep
  backlog size.
