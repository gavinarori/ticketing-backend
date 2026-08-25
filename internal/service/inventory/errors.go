// Package inventory orchestrates the seat-purchase hot path: the virtual
// waiting room, per-user rate limiting, a short-lived Redis lock that
// cheaply rejects thundering-herd contention on one hot seat, and the
// Postgres compare-and-swap (via domain.InventoryRepository) that is the
// actual, sole guarantee against overselling. Redis involvement in this
// package exists to make contention fast and predictable; correctness
// does not depend on it.
package inventory

import "errors"

var (
	// ErrNotAdmitted means the caller hasn't been let through the waiting
	// room for this event yet — see WaitingRoom.IsAdmitted.
	ErrNotAdmitted = errors.New("inventory: not admitted from waiting room")

	// ErrRateLimited means the caller has made too many hold attempts in
	// the current window — see Config.HoldRateLimit.
	ErrRateLimited = errors.New("inventory: rate limited")

	// ErrLockContention means another request for the exact same
	// inventory row was already in flight when this one arrived, and was
	// rejected immediately rather than queued — see Service.HoldSeat.
	// This is the cheap Redis-level rejection described in the package
	// doc; expect it to fire far more often than a genuine Postgres
	// conflict during a hot seat's drop.
	ErrLockContention = errors.New("inventory: seat contended, try again")
)
