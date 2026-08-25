package inventory

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/gavinarori/ticketing-backend/internal/domain"
)

// --- fakes ---
//
// These implement just enough of domain.InventoryRepository and this
// package's locker/waitingRoom/rateLimiter interfaces to exercise
// Service's orchestration logic in isolation, with no Redis or Postgres
// involved. Concurrency-safety of the *real* Postgres CAS is validated
// separately, against a live database, in test/integration.

type fakeInventoryRepo struct {
	mu   sync.Mutex
	rows map[uuid.UUID]*domain.EventSeatInventory
}

func newFakeInventoryRepo(rows ...*domain.EventSeatInventory) *fakeInventoryRepo {
	m := make(map[uuid.UUID]*domain.EventSeatInventory, len(rows))
	for _, r := range rows {
		m[r.ID] = r
	}
	return &fakeInventoryRepo{rows: m}
}

func (f *fakeInventoryRepo) BulkCreate(ctx context.Context, rows []*domain.EventSeatInventory) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, r := range rows {
		f.rows[r.ID] = r
	}
	return nil
}

func (f *fakeInventoryRepo) GetByID(ctx context.Context, tenantID, id uuid.UUID) (*domain.EventSeatInventory, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	row, ok := f.rows[id]
	if !ok {
		return nil, domain.NewError("fake.GetByID", domain.ErrNotFound)
	}
	cp := *row
	return &cp, nil
}

// Hold reproduces the same compare-and-swap semantics as the real
// Postgres UPDATE ... WHERE status = 'available', guarded by a mutex to
// mirror the atomicity a real row-level lock would give.
func (f *fakeInventoryRepo) Hold(ctx context.Context, tenantID, id uuid.UUID, holdToken, userID uuid.UUID, expiresAt time.Time) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	row, ok := f.rows[id]
	if !ok || row.Status != domain.InventoryStatusAvailable {
		return false, nil
	}
	row.Status = domain.InventoryStatusHeld
	row.HoldToken = &holdToken
	row.HeldByUserID = &userID
	row.HoldExpiresAt = &expiresAt
	return true, nil
}

func (f *fakeInventoryRepo) Release(ctx context.Context, tenantID, id uuid.UUID, holdToken uuid.UUID) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	row, ok := f.rows[id]
	if !ok || row.Status != domain.InventoryStatusHeld || row.HoldToken == nil || *row.HoldToken != holdToken {
		return false, nil
	}
	row.Status = domain.InventoryStatusAvailable
	row.HoldToken = nil
	row.HeldByUserID = nil
	row.HoldExpiresAt = nil
	return true, nil
}

func (f *fakeInventoryRepo) ConfirmSale(ctx context.Context, tenantID, id uuid.UUID, holdToken uuid.UUID) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	row, ok := f.rows[id]
	if !ok || row.Status != domain.InventoryStatusHeld || row.HoldToken == nil || *row.HoldToken != holdToken {
		return false, nil
	}
	row.Status = domain.InventoryStatusSold
	row.HoldToken = nil
	row.HeldByUserID = nil
	row.HoldExpiresAt = nil
	return true, nil
}

func (f *fakeInventoryRepo) Void(ctx context.Context, tenantID, id uuid.UUID) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	row, ok := f.rows[id]
	if !ok {
		return domain.NewError("fake.Void", domain.ErrNotFound)
	}
	row.Status = domain.InventoryStatusVoid
	return nil
}

func (f *fakeInventoryRepo) ListExpiredHolds(ctx context.Context, before time.Time, limit int) ([]*domain.EventSeatInventory, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []*domain.EventSeatInventory
	for _, row := range f.rows {
		if row.Status == domain.InventoryStatusHeld && row.HoldExpiresAt != nil && row.HoldExpiresAt.Before(before) {
			cp := *row
			out = append(out, &cp)
			if len(out) >= limit {
				break
			}
		}
	}
	return out, nil
}

func (f *fakeInventoryRepo) CountByStatus(ctx context.Context, tenantID, eventID uuid.UUID) (map[uuid.UUID]domain.InventoryCounts, error) {
	return nil, nil // unused by these tests
}

var _ domain.InventoryRepository = (*fakeInventoryRepo)(nil)

// fakeLocker always succeeds unless configured to deny — enough to test
// HoldSeat's branching without real Redis.
type fakeLocker struct {
	mu   sync.Mutex
	held map[string]string
	deny bool
}

func newFakeLocker() *fakeLocker { return &fakeLocker{held: map[string]string{}} }

func (l *fakeLocker) Acquire(ctx context.Context, key string, ttl time.Duration) (string, bool, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.deny {
		return "", false, nil
	}
	if _, exists := l.held[key]; exists {
		return "", false, nil
	}
	token := uuid.NewString()
	l.held[key] = token
	return token, true, nil
}

func (l *fakeLocker) Release(ctx context.Context, key, token string) (bool, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.held[key] != token {
		return false, nil
	}
	delete(l.held, key)
	return true, nil
}

type fakeWaitingRoom struct {
	mu       sync.Mutex
	admitted map[uuid.UUID]bool
}

func newFakeWaitingRoom(admittedUsers ...uuid.UUID) *fakeWaitingRoom {
	m := map[uuid.UUID]bool{}
	for _, u := range admittedUsers {
		m[u] = true
	}
	return &fakeWaitingRoom{admitted: m}
}

func (w *fakeWaitingRoom) Join(ctx context.Context, eventID, userID uuid.UUID) (int64, error) {
	return 1, nil
}
func (w *fakeWaitingRoom) Position(ctx context.Context, eventID, userID uuid.UUID) (int64, error) {
	return 1, nil
}
func (w *fakeWaitingRoom) IsAdmitted(ctx context.Context, eventID, userID uuid.UUID) (bool, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.admitted[userID], nil
}
func (w *fakeWaitingRoom) Admit(ctx context.Context, eventID uuid.UUID, count int64, ttl time.Duration) ([]uuid.UUID, error) {
	return nil, nil
}
func (w *fakeWaitingRoom) QueueLength(ctx context.Context, eventID uuid.UUID) (int64, error) {
	return 0, nil
}
func (w *fakeWaitingRoom) admit(userID uuid.UUID) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.admitted[userID] = true
}

type fakeRateLimiter struct{ allow bool }

func (r *fakeRateLimiter) Allow(ctx context.Context, key string, limit int, window time.Duration) (bool, error) {
	return r.allow, nil
}

// --- tests ---

func testRow(status domain.InventoryStatus) *domain.EventSeatInventory {
	return &domain.EventSeatInventory{
		ID:                    uuid.New(),
		TenantID:              uuid.New(),
		EventID:               uuid.New(),
		EventTicketCategoryID: uuid.New(),
		Status:                status,
	}
}

func TestHoldSeat_NotAdmitted(t *testing.T) {
	row := testRow(domain.InventoryStatusAvailable)
	userID := uuid.New()

	svc := NewService(
		newFakeInventoryRepo(row),
		newFakeLocker(),
		newFakeWaitingRoom(), // userID NOT admitted
		&fakeRateLimiter{allow: true},
		DefaultConfig(),
	)

	_, err := svc.HoldSeat(context.Background(), row.TenantID, row.EventID, row.ID, userID)
	if !errors.Is(err, ErrNotAdmitted) {
		t.Fatalf("expected ErrNotAdmitted, got %v", err)
	}
}

func TestHoldSeat_RateLimited(t *testing.T) {
	row := testRow(domain.InventoryStatusAvailable)
	userID := uuid.New()

	svc := NewService(
		newFakeInventoryRepo(row),
		newFakeLocker(),
		newFakeWaitingRoom(userID),
		&fakeRateLimiter{allow: false},
		DefaultConfig(),
	)

	_, err := svc.HoldSeat(context.Background(), row.TenantID, row.EventID, row.ID, userID)
	if !errors.Is(err, ErrRateLimited) {
		t.Fatalf("expected ErrRateLimited, got %v", err)
	}
}

func TestHoldSeat_LockContention(t *testing.T) {
	row := testRow(domain.InventoryStatusAvailable)
	userID := uuid.New()

	locker := newFakeLocker()
	locker.deny = true // simulate another request already holding the Redis lock

	svc := NewService(
		newFakeInventoryRepo(row),
		locker,
		newFakeWaitingRoom(userID),
		&fakeRateLimiter{allow: true},
		DefaultConfig(),
	)

	_, err := svc.HoldSeat(context.Background(), row.TenantID, row.EventID, row.ID, userID)
	if !errors.Is(err, ErrLockContention) {
		t.Fatalf("expected ErrLockContention, got %v", err)
	}
}

func TestHoldSeat_Success(t *testing.T) {
	row := testRow(domain.InventoryStatusAvailable)
	userID := uuid.New()

	svc := NewService(
		newFakeInventoryRepo(row),
		newFakeLocker(),
		newFakeWaitingRoom(userID),
		&fakeRateLimiter{allow: true},
		DefaultConfig(),
	)

	result, err := svc.HoldSeat(context.Background(), row.TenantID, row.EventID, row.ID, userID)
	if err != nil {
		t.Fatalf("expected success, got error: %v", err)
	}
	if result.HoldToken == uuid.Nil {
		t.Error("expected a non-nil hold token")
	}
	if !result.ExpiresAt.After(time.Now()) {
		t.Error("expected ExpiresAt to be in the future")
	}
}

func TestHoldSeat_AlreadyHeld_ReturnsUnavailable(t *testing.T) {
	row := testRow(domain.InventoryStatusHeld) // already held by someone else
	userID := uuid.New()

	svc := NewService(
		newFakeInventoryRepo(row),
		newFakeLocker(),
		newFakeWaitingRoom(userID),
		&fakeRateLimiter{allow: true},
		DefaultConfig(),
	)

	_, err := svc.HoldSeat(context.Background(), row.TenantID, row.EventID, row.ID, userID)
	if !errors.Is(err, domain.ErrUnavailable) {
		t.Fatalf("expected domain.ErrUnavailable, got %v", err)
	}
}

func TestHoldSeat_ReleasesLockEvenOnFailure(t *testing.T) {
	row := testRow(domain.InventoryStatusHeld) // Postgres CAS will fail
	userID := uuid.New()
	locker := newFakeLocker()

	svc := NewService(
		newFakeInventoryRepo(row),
		locker,
		newFakeWaitingRoom(userID),
		&fakeRateLimiter{allow: true},
		DefaultConfig(),
	)

	_, _ = svc.HoldSeat(context.Background(), row.TenantID, row.EventID, row.ID, userID)

	if len(locker.held) != 0 {
		t.Errorf("expected lock to be released after a failed hold, but %d locks still held", len(locker.held))
	}
}

func TestReleaseHold_WrongToken_ReturnsExpired(t *testing.T) {
	row := testRow(domain.InventoryStatusAvailable)
	svc := NewService(newFakeInventoryRepo(row), newFakeLocker(), newFakeWaitingRoom(), &fakeRateLimiter{allow: true}, DefaultConfig())

	err := svc.ReleaseHold(context.Background(), row.TenantID, row.ID, uuid.New())
	if !errors.Is(err, domain.ErrExpired) {
		t.Fatalf("expected domain.ErrExpired, got %v", err)
	}
}

func TestConfirmSale_Success(t *testing.T) {
	row := testRow(domain.InventoryStatusAvailable)
	userID := uuid.New()
	repo := newFakeInventoryRepo(row)

	svc := NewService(repo, newFakeLocker(), newFakeWaitingRoom(userID), &fakeRateLimiter{allow: true}, DefaultConfig())

	result, err := svc.HoldSeat(context.Background(), row.TenantID, row.EventID, row.ID, userID)
	if err != nil {
		t.Fatalf("hold failed: %v", err)
	}

	if err := svc.ConfirmSale(context.Background(), row.TenantID, row.ID, result.HoldToken); err != nil {
		t.Fatalf("expected confirm sale to succeed, got %v", err)
	}

	got, _ := repo.GetByID(context.Background(), row.TenantID, row.ID)
	if got.Status != domain.InventoryStatusSold {
		t.Errorf("expected status 'sold', got %q", got.Status)
	}
}

func TestSweepExpiredHolds_ReleasesExpiredOnly(t *testing.T) {
	past := time.Now().Add(-time.Minute)
	future := time.Now().Add(time.Hour)
	expiredToken := uuid.New()
	activeToken := uuid.New()

	expiredRow := testRow(domain.InventoryStatusHeld)
	expiredRow.HoldToken = &expiredToken
	expiredRow.HoldExpiresAt = &past

	activeRow := testRow(domain.InventoryStatusHeld)
	activeRow.HoldToken = &activeToken
	activeRow.HoldExpiresAt = &future

	repo := newFakeInventoryRepo(expiredRow, activeRow)
	svc := NewService(repo, newFakeLocker(), newFakeWaitingRoom(), &fakeRateLimiter{allow: true}, DefaultConfig())

	released, err := svc.SweepExpiredHolds(context.Background(), 10)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if released != 1 {
		t.Errorf("expected exactly 1 row released, got %d", released)
	}

	got, _ := repo.GetByID(context.Background(), expiredRow.TenantID, expiredRow.ID)
	if got.Status != domain.InventoryStatusAvailable {
		t.Errorf("expected expired hold to be released back to 'available', got %q", got.Status)
	}

	stillHeld, _ := repo.GetByID(context.Background(), activeRow.TenantID, activeRow.ID)
	if stillHeld.Status != domain.InventoryStatusHeld {
		t.Errorf("expected non-expired hold to remain 'held', got %q", stillHeld.Status)
	}
}

// TestHoldSeat_ConcurrentRaceOnFake exercises the same "many fans, one
// seat" race the integration test proves against real Postgres, but here
// purely to confirm the fake's own locking mirrors CAS semantics — exactly
// one goroutine should win, using Go's race detector (`go test -race`) to
// catch any actual data race in Service itself, not just in the fake.
func TestHoldSeat_ConcurrentRaceOnFake(t *testing.T) {
	row := testRow(domain.InventoryStatusAvailable)
	repo := newFakeInventoryRepo(row)
	wr := newFakeWaitingRoom()
	svc := NewService(repo, newFakeLocker(), wr, &fakeRateLimiter{allow: true}, DefaultConfig())

	const attempts = 50

	var wg sync.WaitGroup
	var mu sync.Mutex
	successCount := 0

	for i := 0; i < attempts; i++ {
		userID := uuid.New()
		wr.admit(userID)
		wg.Add(1)
		go func(uid uuid.UUID) {
			defer wg.Done()
			_, err := svc.HoldSeat(context.Background(), row.TenantID, row.EventID, row.ID, uid)
			if err == nil {
				mu.Lock()
				successCount++
				mu.Unlock()
			}
		}(userID)
	}
	wg.Wait()

	if successCount != 1 {
		t.Errorf("expected exactly 1 successful hold out of %d concurrent attempts, got %d", attempts, successCount)
	}
}
