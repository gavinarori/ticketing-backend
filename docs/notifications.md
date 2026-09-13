# Notifications (order confirmation)

A transactional outbox: `order.Service.ConfirmPayment` enqueues a
`notifications` row in the **same database transaction** as marking an
order `paid` (migration `000014`) — so "paid but no confirmation ever
queued" is a schema-level impossibility, the same way earlier migrations
made "sold without a valid hold" impossible. Sending is fully decoupled:
`cmd/worker`'s new notification-dispatch loop polls `ProcessPending` on a
ticker and does the actual delivery via `domain.EmailSender`
(`SMTPSender` if `SMTP_HOST` is configured, otherwise `ConsoleSender` —
same dev-convenience fallback pattern as the mock payment gateway).

## Two real bugs this round caught, both in `UserRepo.scanUser`

Both `phone` and `password_hash` are nullable columns by design (a phone
number is optional; `password_hash` is null-able for a future OAuth-only
account). `scanUser` was scanning both directly into plain `string`
fields instead of through `*string` — so **any** user row with either
column `NULL` failed `GetByID`/`GetByEmail` outright, not just in this
feature's test.

This was caught by running the actual dispatch flow against a real
Postgres row with a NULL phone (`can't scan into dest[2]: cannot scan
NULL into *string`), fixed, and then immediately hit an **identical**
second instance for `password_hash` on the very next fix-and-rerun cycle.
Both are fixed the same way: scan through a nullable pointer, coalesce to
`""` if `NULL`. A fakes-based unit test would never have caught either —
a fake repository doesn't model column nullability — which is exactly
why this project validates against real Postgres, not just mocks.

## What was actually validated

- Unit tests in `internal/platform/email` (message construction,
  `ConsoleSender`, `SMTPSender` with an injected fake transport).
- Integration tests (`test/integration/notification_test.go`): enqueue
  happens inside the same transaction as `ConfirmPayment`; a webhook
  replay does not double-enqueue; `ProcessPending` actually sends and
  marks `sent`.
- Full suite (20 integration tests, unit tests across all packages)
  re-run clean, twice, after the fixes.
- **Live, against the compiled `cmd/worker` binary**: seeded a `pending`
  notification row exactly as `ConfirmPayment` would leave one, launched
  the real binary, watched the notification loop pick it up, render it,
  "send" it via `ConsoleSender`, and mark it `sent` — confirmed directly
  in Postgres afterward. Graceful shutdown via `SIGTERM` confirmed clean.

## What's deliberately not here yet

- **No retry/backoff** — a `failed` row is currently terminal, stated
  plainly in `notification.Service.ProcessPending`'s doc comment.
- **One notification type** — order confirmation only; no hold-expiring
  reminders, no admin alerts.
- **No event details in the message** — `OrderConfirmationData` omits
  event name/venue/date deliberately, to avoid `order.Service` needing
  venue/event lookups purely for message copy.
