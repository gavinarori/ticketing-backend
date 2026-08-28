# Order creation flow

Turns a set of active seat holds (from `internal/service/inventory`) into
a paid order. Validated end-to-end against real Postgres, using a mock
payment gateway (no real Stripe network access needed or possible from
this environment) — see "What was actually validated" below.

## The central design decision: payment before sale confirmation

`event_seat_inventory` status stays `'held'` all the way through payment.
The `'held' -> 'sold'` transition (`ConfirmSale`) only happens after the
payment gateway has actually captured funds.

**The alternative (confirm sale first, charge second) was considered and
rejected.** Marking a seat `'sold'` before payment succeeds means a
declined card leaves a sold-but-unpaid ticket that has to be explicitly
detected and voided — an extra failure mode on the *common* path, since
most payments succeed. Payment-first pushes that cost onto the *rare*
path instead: if payment captures but the hold has since expired (an
unusually slow 3-D Secure flow outlasting `HoldDuration`, or a sweep
reclaiming it in a race), the seat may already be gone, and the money that
already moved has to be refunded.

That refund path is real, not hypothetical — it's excercised directly by
`TestConfirmPayment_HoldExpiredMeanwhile_RefundsAndFails`, which
authorizes payment, then explicitly releases the hold (simulating the
sweep winning the race), then confirms payment and checks all three
things that must be true afterward: the order ends up `'expired'`, the
payment ends up `'refunded'`, and the seat never ends up `'sold'`.

## Why `order_items` needed a schema change

`ConfirmSale` requires the *original* hold token, not whatever token is
currently on the inventory row — by the time `ConfirmPayment` runs
(possibly minutes after the hold was created), the row's current
`hold_token` might belong to a completely different fan if this hold
expired and was re-claimed in the meantime. Using the row's *current*
token at confirmation time would silently confirm sale using someone
else's hold — a real correctness bug, not a stylistic concern. Migration
`000012` adds `order_items.hold_token`, captured at order-creation time
and never re-read from the inventory row afterward.

## Why `PaymentGateway` gained a `Refund` method

Directly required by the design decision above — `domain.PaymentGateway`
didn't have a way to reverse a capture, and the payment-first design
can't function without one. Implemented in both `MockGateway` (in-memory,
for dev/tests) and `StripeGateway` (real HTTP call to `/v1/refunds`).

## Transaction boundaries: two, not one

`CreateOrder` and `ConfirmPayment` each wrap their Postgres writes in
`postgres.WithTx` — but the payment gateway call (`AuthorizePayment`,
`refundAndFail`) deliberately happens **outside** any transaction. Holding
a database transaction open across a network call to Stripe is a
correctness and availability anti-pattern regardless of provider: it pins
a connection and locks for however long that call takes, and a slow or
down payment provider would otherwise fail order creation outright even
though nothing about the order itself was invalid.

## Why the order service depends on `*pgxpool.Pool` directly

Every other service in this codebase depends only on `domain` interfaces.
`internal/service/order` is the one exception — it needs
`postgres.WithTx` to guarantee multiple repository calls commit or roll
back together, and "run these calls in one transaction" is inherently
Postgres-shaped in a way the domain-level repository interfaces
deliberately don't try to express. This is called out explicitly in the
package's doc comment rather than pretending the dependency isn't there.
One consequence: unlike `internal/service/inventory`, this service isn't
practically unit-testable against fakes (there's no meaningful way to fake
multi-statement transactional atomicity) — it's proven correct via the
integration suite against real Postgres instead.

## What was actually validated

- **The entire project builds clean** (`go build ./...`), not just the
  packages touched this task — this caught a real, pre-existing bug: an
  unused `"time"` import in `cmd/api/main.go` that had never actually been
  compiled before (only `gofmt`-checked). Fixed as part of this task.
- **`go vet ./...`** — clean across the whole project.
- **8 unit tests** in `internal/platform/payment` (`mock_test.go`,
  `stripe_test.go`), run with `-race`: `MockGateway`'s create/refund/
  signature-verify behavior, and `StripeGateway`'s HTTP request shape
  (headers, form fields), error handling for a declined card, and — the
  one genuinely security-relevant piece — Stripe's actual webhook
  signature algorithm, verified against an independently-computed
  signature (valid case), a wrong secret, a tampered payload, and a
  malformed header. All fully offline via `httptest.Server` and pure
  crypto — no real Stripe network access needed or possible here.
- **8 integration tests** against real Postgres + real Redis, run with
  `-race`, **5 consecutive times with zero flakes**: the full
  hold-to-paid happy path (checking every table's state at each step, not
  just return values), idempotent order creation, rejection of an
  already-expired hold, and the refund-on-race edge case described above.
- **All 12 migrations** (the new `000012` included) apply cleanly in
  order and roll back cleanly in reverse, back to an empty schema.

Two real bugs were caught and fixed by actually running this, not by
review: the `cmd/api` unused import above, and an unused `eventID`
variable in a first draft of `TestCreateOrder_ExpiredHold_Rejected`
(introduced, then caught, within this same task).

## What's deliberately not here yet

- **Webhook handler** — `ConfirmPayment` is called directly by tests;
  wiring a real `POST /webhooks/stripe` handler that calls
  `VerifyWebhookSignature` then `ConfirmPayment` is next.
- **Order cancellation** — a fan explicitly abandoning checkout before
  paying has no dedicated method yet; `ReleaseHold` (inventory service)
  covers the seat side, but nothing marks the order `'cancelled'`.
- **Fee/tax calculation** — `Service.CreateOrder` hardcodes `fees = 0`;
  real fee policy is a business decision left for later.
- **HTTP handlers generally** — this task, like inventory locking, is
  service-layer only.
