package inventory

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/gavinarori/ticketing-backend/internal/domain"
	redisrepo "github.com/gavinarori/ticketing-backend/internal/repository/redis"
)

// locker, waitingRoom, and rateLimiter are the minimal interfaces this
// service needs from redisrepo.Locker / redisrepo.WaitingRoom /
// redisrepo.RateLimiter. Defining them here — "accept interfaces, return
// structs" — means unit tests can substitute in-memory fakes without
// spinning up Redis; only the integration tests under test/integration
// need the real Redis-backed implementations. The concrete
// *redisrepo.Locker etc. types satisfy these automatically, structurally.
type locker interface {
	Acquire(ctx context.Context, key string, ttl time.Duration) (token string, ok bool, err error)
	Release(ctx context.Context, key, token string) (ok bool, err error)
}

type waitingRoom interface {
	Join(ctx context.Context, eventID, userID uuid.UUID) (position int64, err error)
	Position(ctx context.Context, eventID, userID uuid.UUID) (int64, error)
	IsAdmitted(ctx context.Context, eventID, userID uuid.UUID) (bool, error)
	Admit(ctx context.Context, eventID uuid.UUID, count int64, admissionTTL time.Duration) ([]uuid.UUID, error)
	QueueLength(ctx context.Context, eventID uuid.UUID) (int64, error)
}

type rateLimiter interface {
	Allow(ctx context.Context, key string, limit int, window time.Duration) (bool, error)
}

// ErrNotQueued is returned by QueueStatus when the caller has never
// joined the waiting room for that event (or was already admitted and
// removed from the queue). Re-exported from the redis package's sentinel
// so callers of this package never need to import
// internal/repository/redis just to check errors.Is.
var ErrNotQueued = redisrepo.ErrNotQueued

// Service orchestrates a Hold/Release/ConfirmSale attempt across the
// waiting room, rate limiter, Redis lock, and the Postgres repository
// that actually owns correctness. See the package doc in errors.go for
// the layering rationale.
type Service struct {
	inventory   domain.InventoryRepository
	locker      locker
	waitingRoom waitingRoom
	rateLimiter rateLimiter
	cfg         Config
}

// NewService wires the collaborators together. In production, pass
// concrete *redisrepo.Locker / *redisrepo.WaitingRoom /
// *redisrepo.RateLimiter values (internal/repository/redis) — they
// satisfy the interfaces above structurally, no adapter needed.
func NewService(inventory domain.InventoryRepository, l locker, wr waitingRoom, rl rateLimiter, cfg Config) *Service {
	return &Service{inventory: inventory, locker: l, waitingRoom: wr, rateLimiter: rl, cfg: cfg}
}

// HoldResult is returned by a successful HoldSeat.
type HoldResult struct {
	HoldToken uuid.UUID
	ExpiresAt time.Time
}

func lockKey(tenantID, inventoryID uuid.UUID) string {
	return fmt.Sprintf("lock:inventory:%s:%s", tenantID, inventoryID)
}

func rateLimitKey(tenantID, eventID, userID uuid.UUID) string {
	return fmt.Sprintf("ratelimit:hold:%s:%s:%s", tenantID, eventID, userID)
}

// HoldSeat attempts to reserve one inventory row for userID. The checks
// run in increasing order of cost, each a cheap way to reject a doomed
// request before paying for the next, more expensive step:
//
//  1. waiting room admission (one Redis EXISTS)
//  2. rate limit (one Redis INCR)
//  3. per-row Redis lock (one Redis SETNX) — rejects thundering-herd
//     concurrent attempts at the exact same seat before they'd otherwise
//     all pile onto the same Postgres row at once
//  4. the actual Postgres compare-and-swap — the only step that can
//     authoritatively say the hold succeeded
//
// A caller that gets past step 3 but loses at step 4 learns that via
// domain.ErrUnavailable, exactly as if Redis weren't in the picture at
// all — Redis only narrows how often that round trip is wasted; it never
// changes the outcome.
func (s *Service) HoldSeat(ctx context.Context, tenantID, eventID, inventoryID, userID uuid.UUID) (*HoldResult, error) {
	admitted, err := s.waitingRoom.IsAdmitted(ctx, eventID, userID)
	if err != nil {
		return nil, fmt.Errorf("inventory: check admission: %w", err)
	}
	if !admitted {
		return nil, ErrNotAdmitted
	}

	allowed, err := s.rateLimiter.Allow(ctx, rateLimitKey(tenantID, eventID, userID), s.cfg.HoldRateLimit, s.cfg.HoldRateLimitWindow)
	if err != nil {
		return nil, fmt.Errorf("inventory: check rate limit: %w", err)
	}
	if !allowed {
		return nil, ErrRateLimited
	}

	key := lockKey(tenantID, inventoryID)
	lockToken, acquired, err := s.locker.Acquire(ctx, key, s.cfg.LockTTL)
	if err != nil {
		return nil, fmt.Errorf("inventory: acquire lock: %w", err)
	}
	if !acquired {
		// Someone else is mid-attempt on this exact row right now.
		// Rejecting immediately (rather than blocking or retrying) keeps
		// a single hot seat from queuing up thousands of waiting
		// goroutines; the frontend is expected to retry a different seat
		// or briefly poll, not spin on this one.
		return nil, ErrLockContention
	}
	defer func() {
		// Best-effort release. If this fails, the key still expires
		// after LockTTL on its own — a failed unlock degrades to
		// "briefly slower contention recovery", never to a stuck lock.
		_, _ = s.locker.Release(ctx, key, lockToken)
	}()

	holdToken := uuid.New()
	expiresAt := time.Now().Add(s.cfg.HoldDuration)

	ok, err := s.inventory.Hold(ctx, tenantID, inventoryID, holdToken, userID, expiresAt)
	if err != nil {
		return nil, fmt.Errorf("inventory: hold: %w", err)
	}
	if !ok {
		return nil, domain.NewError("inventory.HoldSeat", domain.ErrUnavailable)
	}

	return &HoldResult{HoldToken: holdToken, ExpiresAt: expiresAt}, nil
}

// ReleaseHold voluntarily gives up a hold — e.g. the fan closed the
// checkout tab, or removed the seat from their cart before paying.
func (s *Service) ReleaseHold(ctx context.Context, tenantID, inventoryID, holdToken uuid.UUID) error {
	ok, err := s.inventory.Release(ctx, tenantID, inventoryID, holdToken)
	if err != nil {
		return fmt.Errorf("inventory: release: %w", err)
	}
	if !ok {
		return domain.NewError("inventory.ReleaseHold", domain.ErrExpired)
	}
	return nil
}

// ConfirmSale converts a hold into a sale. It's intended to be called
// from inside the same database transaction as order/order_item creation
// (a future order-creation service) — pass a ctx that already carries a
// postgres.WithTx transaction so both writes commit or roll back
// together. Called standalone, with no ambient transaction, it still
// behaves correctly as its own single-statement transaction.
func (s *Service) ConfirmSale(ctx context.Context, tenantID, inventoryID, holdToken uuid.UUID) error {
	ok, err := s.inventory.ConfirmSale(ctx, tenantID, inventoryID, holdToken)
	if err != nil {
		return fmt.Errorf("inventory: confirm sale: %w", err)
	}
	if !ok {
		return domain.NewError("inventory.ConfirmSale", domain.ErrExpired)
	}
	return nil
}

// SweepExpiredHolds reclaims 'held' rows whose hold has expired,
// releasing each back to 'available'. Intended to be called on a ticker
// from cmd/worker. Returns the number of rows actually released — a row
// present in the expired list that fails to Release (e.g. it was
// confirmed sold in the moment between the list query and this call) is
// not counted, and is not an error: that's the same CAS guard working
// exactly as intended, just observed from the sweep's side.
func (s *Service) SweepExpiredHolds(ctx context.Context, batchSize int) (released int, err error) {
	expired, err := s.inventory.ListExpiredHolds(ctx, time.Now(), batchSize)
	if err != nil {
		return 0, fmt.Errorf("inventory: list expired holds: %w", err)
	}

	for _, row := range expired {
		if row.HoldToken == nil {
			continue // defensive: the CHECK constraint should make this impossible
		}
		ok, err := s.inventory.Release(ctx, row.TenantID, row.ID, *row.HoldToken)
		if err != nil {
			return released, fmt.Errorf("inventory: sweep release %s: %w", row.ID, err)
		}
		if ok {
			released++
		}
	}
	return released, nil
}

// --- Waiting room pass-through ---
//
// These wrap the waitingRoom collaborator directly rather than adding
// logic of their own. They exist so callers (HTTP handlers, a future
// admission-control loop in cmd/worker) depend on this service instead of
// importing internal/repository/redis directly — keeping Redis an
// implementation detail of the inventory service rather than something
// the handler layer reaches around it to use.

func (s *Service) JoinQueue(ctx context.Context, eventID, userID uuid.UUID) (position int64, err error) {
	pos, err := s.waitingRoom.Join(ctx, eventID, userID)
	if err != nil {
		return 0, fmt.Errorf("inventory: join queue: %w", err)
	}
	return pos, nil
}

// QueueStatus reports where a fan stands: still queued (with Position),
// or already Admitted.
type QueueStatus struct {
	Admitted bool
	Position int64 // meaningful only when Admitted is false
}

func (s *Service) QueueStatus(ctx context.Context, eventID, userID uuid.UUID) (*QueueStatus, error) {
	admitted, err := s.waitingRoom.IsAdmitted(ctx, eventID, userID)
	if err != nil {
		return nil, fmt.Errorf("inventory: check admission: %w", err)
	}
	if admitted {
		return &QueueStatus{Admitted: true}, nil
	}

	pos, err := s.waitingRoom.Position(ctx, eventID, userID)
	if errors.Is(err, redisrepo.ErrNotQueued) {
		return nil, ErrNotQueued
	}
	if err != nil {
		return nil, fmt.Errorf("inventory: queue status: %w", err)
	}
	return &QueueStatus{Admitted: false, Position: pos}, nil
}

// AdmitNext lets up to count more fans through the waiting room for
// eventID. Intended to be called periodically (e.g. from cmd/worker on a
// ticker) with count derived from real available-inventory counts — how
// exactly that admission rate is computed is a policy decision left to
// the caller, deliberately outside this method.
func (s *Service) AdmitNext(ctx context.Context, eventID uuid.UUID, count int64) ([]uuid.UUID, error) {
	admitted, err := s.waitingRoom.Admit(ctx, eventID, count, s.cfg.AdmissionTTL)
	if err != nil {
		return nil, fmt.Errorf("inventory: admit next: %w", err)
	}
	return admitted, nil
}

func (s *Service) QueueLength(ctx context.Context, eventID uuid.UUID) (int64, error) {
	n, err := s.waitingRoom.QueueLength(ctx, eventID)
	if err != nil {
		return 0, fmt.Errorf("inventory: queue length: %w", err)
	}
	return n, nil
}
