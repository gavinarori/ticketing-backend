package domain

import (
	"context"
	"time"

	"github.com/google/uuid"
)

type InventoryStatus string

const (
	InventoryStatusAvailable InventoryStatus = "available"
	InventoryStatusHeld      InventoryStatus = "held"
	InventoryStatusSold      InventoryStatus = "sold"
	InventoryStatusVoid      InventoryStatus = "void"
)

// EventSeatInventory is one sellable ticket unit for one event — the
// domain mirror of migrations/000006_inventory.up.sql. See that
// migration's header comment for the full reasoning; the short version:
// every purchasable ticket, seated or general-admission, is exactly one
// row, and the oversell guarantee reduces to a single atomic conditional
// update on Status.
type EventSeatInventory struct {
	ID                    uuid.UUID
	TenantID              uuid.UUID
	EventID               uuid.UUID
	EventTicketCategoryID uuid.UUID
	SeatID                *uuid.UUID // nil for general-admission units
	Status                InventoryStatus
	HoldToken             *uuid.UUID
	HeldByUserID          *uuid.UUID
	HoldExpiresAt         *time.Time
	CreatedAt             time.Time
	UpdatedAt             time.Time
}

// InventoryCounts summarizes how many units of one category are in each
// state — what an event/seat-map page needs to render "12 of 50 VIP
// tickets remaining" without loading every individual row.
type InventoryCounts struct {
	Available int
	Held      int
	Sold      int
	Void      int
}

// InventoryRepository is the single most concurrency-critical interface
// in this codebase — it will be implemented against event_seat_inventory
// by a repository that performs every state transition as the atomic
// conditional UPDATE described in migration 000006.
//
// Every method that can be genuinely contended during a ticket drop
// returns (bool, error) rather than just error. The bool answers "did
// this call win the race"; false-without-error is the *expected*,
// frequent outcome when many fans compete for one seat — not a failure to
// be logged, alerted on, or retried as if it were one. error is reserved
// for actual infrastructure or programming failures (DB unreachable,
// malformed input, and so on).
type InventoryRepository interface {
	// BulkCreate inserts the full set of sellable units for an event in
	// one call — typically triggered when an event moves from "draft" to
	// "scheduled"/"on_sale".
	BulkCreate(ctx context.Context, rows []*EventSeatInventory) error

	GetByID(ctx context.Context, tenantID, id uuid.UUID) (*EventSeatInventory, error)

	// Hold attempts to atomically transition one row from 'available' to
	// 'held'. Returns (true, nil) if this call won the race. Returns
	// (false, nil) — NOT an error — if the row was already held or sold
	// by the time this ran; the caller should surface that as "this seat
	// is no longer available", not retry it or treat it as exceptional.
	Hold(ctx context.Context, tenantID, id uuid.UUID, holdToken, userID uuid.UUID, expiresAt time.Time) (bool, error)

	// Release atomically transitions a row from 'held' back to
	// 'available', but only when holdToken still matches the row's
	// current hold. This guards against a stale or expired release
	// request clobbering a seat that has since been re-held or sold to
	// someone else. Returns (false, nil) if the token no longer matches.
	Release(ctx context.Context, tenantID, id uuid.UUID, holdToken uuid.UUID) (bool, error)

	// ConfirmSale atomically transitions a row from 'held' to 'sold',
	// again gated on holdToken matching. It's intended to be called
	// inside the same database transaction as order/order_item creation
	// (see the order-creation flow, a later task) so a payment can never
	// be recorded as successful against a seat whose hold silently
	// expired moments earlier.
	ConfirmSale(ctx context.Context, tenantID, id uuid.UUID, holdToken uuid.UUID) (bool, error)

	// Void permanently marks a row unsellable (e.g. a venue safety issue
	// removes a seat from sale). An administrative action outside the
	// purchase race, so it's a plain error-returning call rather than the
	// bool/error shape above.
	Void(ctx context.Context, tenantID, id uuid.UUID) error

	// ListExpiredHolds finds 'held' rows whose hold_expires_at has
	// passed, for the worker's sweep loop (internal/service or cmd/worker,
	// a later task) to reclaim via Release. Bounded by limit so a single
	// sweep tick can't attempt an unbounded backlog in one query.
	ListExpiredHolds(ctx context.Context, before time.Time, limit int) ([]*EventSeatInventory, error)

	// CountByStatus powers "N of M remaining" displays without loading
	// every inventory row for an event. The returned map is keyed by
	// EventTicketCategoryID.
	CountByStatus(ctx context.Context, tenantID, eventID uuid.UUID) (map[uuid.UUID]InventoryCounts, error)
}
