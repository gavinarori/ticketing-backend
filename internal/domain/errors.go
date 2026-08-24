package domain

import (
	"errors"
	"fmt"
)

// Sentinel errors that every service/repository in this codebase wraps
// its failures with (via fmt.Errorf("...: %w", ErrX)), so callers —
// including HTTP handlers translating errors to status codes — can branch
// with errors.Is instead of matching on message strings.
var (
	// ErrNotFound means the requested entity does not exist.
	ErrNotFound = errors.New("domain: not found")

	// ErrConflict means the write was rejected because the entity already
	// exists or was concurrently modified (e.g. duplicate slug, duplicate
	// idempotency key). Distinct from ErrNotFound so handlers can return
	// 409 instead of 404/500.
	ErrConflict = errors.New("domain: conflict")

	// ErrInvalidInput means the caller supplied data that fails a domain
	// invariant (e.g. negative price, sales window ending before it
	// starts) — distinct from validation caught at the HTTP boundary so
	// business-rule violations still surface clearly even when the
	// service is called directly (e.g. from the worker, not through HTTP).
	ErrInvalidInput = errors.New("domain: invalid input")

	// ErrUnavailable means an inventory unit could not be held or sold
	// because it is no longer 'available'. This is the *expected* outcome
	// of losing a race during a ticket drop, not a system failure —
	// callers should treat it as ordinary control flow (show the fan
	// "sold out"), not something to log as an error or retry.
	ErrUnavailable = errors.New("domain: unavailable")

	// ErrExpired means a hold_token was well-formed but no longer matches
	// the current state of the row (already expired and possibly re-held
	// or sold to someone else). Kept distinct from ErrUnavailable so a
	// caller can give the fan a clearer "your reservation timed out"
	// message instead of a generic "someone else got it first".
	ErrExpired = errors.New("domain: hold expired")
)

// Error wraps a sentinel with the operation that produced it (e.g.
// "inventory.Hold"), while remaining unwrappable via errors.Is/errors.As
// against the sentinels above.
type Error struct {
	Op  string
	Err error
}

func (e *Error) Error() string {
	return fmt.Sprintf("%s: %v", e.Op, e.Err)
}

func (e *Error) Unwrap() error {
	return e.Err
}

// NewError builds a domain.Error tagged with the failing operation, e.g.:
//
//	return domain.NewError("inventory.Hold", domain.ErrUnavailable)
func NewError(op string, err error) *Error {
	return &Error{Op: op, Err: err}
}
