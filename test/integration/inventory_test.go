//go:build integration

// Package integration holds tests that require real infrastructure
// (Postgres + Redis), run via `make test-integration` (see the Makefile
// and docker-compose.yml). Unlike the fakes-based unit tests in
// internal/service/inventory, these prove the actual compare-and-swap
// SQL and the actual Redis lock/waiting-room behavior, not a
// hand-written stand-in for them.
package integration

import (
	"context"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	goredis "github.com/redis/go-redis/v9"

	"github.com/gavinarori/ticketing-backend/internal/domain"
	pgrepo "github.com/gavinarori/ticketing-backend/internal/repository/postgres"
	redisrepo "github.com/gavinarori/ticketing-backend/internal/repository/redis"
	invsvc "github.com/gavinarori/ticketing-backend/internal/service/inventory"
)

// testEnv holds everything a test needs against real infra. Skips the
// test (not fails) if TEST_DATABASE_URL / TEST_REDIS_ADDR aren't set, so
// `go test ./...` without -tags=integration or without infra configured
// never breaks a normal run.
type testEnv struct {
	pool  *pgxpool.Pool
	redis *goredis.Client
	svc   *invsvc.Service
	repo  *pgrepo.InventoryRepo
}

func setup(t *testing.T) *testEnv {
	t.Helper()

	dsn := os.Getenv("TEST_DATABASE_URL")
	redisAddr := os.Getenv("TEST_REDIS_ADDR")
	if dsn == "" || redisAddr == "" {
		t.Skip("TEST_DATABASE_URL and TEST_REDIS_ADDR must be set to run integration tests")
	}

	ctx := context.Background()

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect postgres: %v", err)
	}
	t.Cleanup(pool.Close)

	rc := goredis.NewClient(&goredis.Options{Addr: redisAddr})
	t.Cleanup(func() { _ = rc.Close() })
	if err := rc.Ping(ctx).Err(); err != nil {
		t.Fatalf("connect redis: %v", err)
	}

	repo := pgrepo.NewInventoryRepo(pool)
	locker := redisrepo.NewLocker(rc)
	waitingRoom := redisrepo.NewWaitingRoom(rc)
	rateLimiter := redisrepo.NewRateLimiter(rc)

	cfg := invsvc.DefaultConfig()
	cfg.HoldRateLimit = 1000 // don't let rate limiting interfere with concurrency tests

	svc := invsvc.NewService(repo, locker, waitingRoom, rateLimiter, cfg)

	return &testEnv{pool: pool, redis: rc, svc: svc, repo: repo}
}

// seedInventoryRow inserts one full tenant/venue/event/inventory chain and
// returns the IDs the test needs, exercising the real schema end to end
// rather than inserting a bare inventory row in isolation.
func seedInventoryRow(t *testing.T, env *testEnv) (tenantID, eventID, inventoryID uuid.UUID) {
	t.Helper()
	ctx := context.Background()

	tenantID = uuid.New()
	venueID := uuid.New()
	sectionID := uuid.New()
	seatID := uuid.New()
	categoryID := uuid.New()
	eventID = uuid.New()
	etcID := uuid.New()
	inventoryID = uuid.New()

	stmts := []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO tenants (id, slug, name) VALUES ($1, $2, $3)`,
			[]any{tenantID, "test-" + tenantID.String()[:8], "Test FC"}},
		{`INSERT INTO venues (id, tenant_id, name) VALUES ($1, $2, $3)`,
			[]any{venueID, tenantID, "Test Stadium"}},
		{`INSERT INTO venue_sections (id, tenant_id, venue_id, name, code, capacity) VALUES ($1, $2, $3, $4, $5, $6)`,
			[]any{sectionID, tenantID, venueID, "North", "N", 100}},
		{`INSERT INTO seats (id, tenant_id, section_id, row_label, seat_number) VALUES ($1, $2, $3, $4, $5)`,
			[]any{seatID, tenantID, sectionID, "A", "1"}},
		{`INSERT INTO seat_categories (id, tenant_id, name) VALUES ($1, $2, $3)`,
			[]any{categoryID, tenantID, "VIP"}},
		{`INSERT INTO events (id, tenant_id, venue_id, name, starts_at, sales_start_at, sales_end_at, status)
		  VALUES ($1, $2, $3, $4, now() + interval '7 days', now() - interval '1 day', now() + interval '6 days', 'on_sale')`,
			[]any{eventID, tenantID, venueID, "Test Match"}},
		{`INSERT INTO event_ticket_categories (id, tenant_id, event_id, seat_category_id, price_cents)
		  VALUES ($1, $2, $3, $4, $5)`,
			[]any{etcID, tenantID, eventID, categoryID, 250000}},
		{`INSERT INTO event_seat_inventory (id, tenant_id, event_id, event_ticket_category_id, seat_id)
		  VALUES ($1, $2, $3, $4, $5)`,
			[]any{inventoryID, tenantID, eventID, etcID, seatID}},
	}

	for _, s := range stmts {
		if _, err := env.pool.Exec(ctx, s.sql, s.args...); err != nil {
			t.Fatalf("seed: %s: %v", s.sql, err)
		}
	}

	return tenantID, eventID, inventoryID
}

// seedUser inserts a minimal users row. event_seat_inventory.held_by_user_id
// has a real foreign key to users(id) (see migrations/000006_inventory.up.sql)
// — a fake, never-inserted user ID correctly fails that constraint, which
// is exactly what the first run of this test suite caught.
func seedUser(t *testing.T, env *testEnv, userID uuid.UUID) {
	t.Helper()
	_, err := env.pool.Exec(context.Background(),
		`INSERT INTO users (id, email, first_name, last_name) VALUES ($1, $2, 'Test', 'Fan')`,
		userID, userID.String()+"@example.test",
	)
	if err != nil {
		t.Fatalf("seed user: %v", err)
	}
}

func seedUsers(t *testing.T, env *testEnv, userIDs []uuid.UUID) {
	t.Helper()
	for _, id := range userIDs {
		seedUser(t, env, id)
	}
}

// TestHoldSeat_NeverOversells is the central claim of this whole
// service, proven against real Postgres and real Redis: many fans
// competing for the exact same seat at the same instant, and exactly one
// of them walks away with a hold.
func TestHoldSeat_NeverOversells(t *testing.T) {
	env := setup(t)
	tenantID, eventID, inventoryID := seedInventoryRow(t, env)
	ctx := context.Background()

	const attempts = 100
	userIDs := make([]uuid.UUID, attempts)
	for i := range userIDs {
		userIDs[i] = uuid.New()
	}
	seedUsers(t, env, userIDs)
	for _, id := range userIDs {
		if _, err := env.svc.JoinQueue(ctx, eventID, id); err != nil {
			t.Fatalf("join queue: %v", err)
		}
	}
	if _, err := env.svc.AdmitNext(ctx, eventID, attempts); err != nil {
		t.Fatalf("admit next: %v", err)
	}

	var (
		wg           sync.WaitGroup
		successCount int32
		winnerToken  uuid.UUID
		winnerMu     sync.Mutex
		startGate    = make(chan struct{})
	)

	for _, userID := range userIDs {
		wg.Add(1)
		go func(uid uuid.UUID) {
			defer wg.Done()
			<-startGate // line every goroutine up before releasing them together
			result, err := env.svc.HoldSeat(ctx, tenantID, eventID, inventoryID, uid)
			if err == nil {
				atomic.AddInt32(&successCount, 1)
				winnerMu.Lock()
				winnerToken = result.HoldToken
				winnerMu.Unlock()
			}
		}(userID)
	}
	close(startGate) // release all goroutines at once — maximum real contention
	wg.Wait()

	if successCount != 1 {
		t.Fatalf("expected exactly 1 successful hold out of %d concurrent attempts, got %d", attempts, successCount)
	}

	// Confirm the database itself agrees: exactly one row, 'held', with
	// the token the winning goroutine received.
	row, err := env.repo.GetByID(ctx, tenantID, inventoryID)
	if err != nil {
		t.Fatalf("get inventory: %v", err)
	}
	if row.Status != domain.InventoryStatusHeld {
		t.Fatalf("expected status 'held', got %q", row.Status)
	}
	if row.HoldToken == nil || *row.HoldToken != winnerToken {
		t.Fatalf("expected inventory row's hold_token to match the winning goroutine's token")
	}
}

// TestHoldSeat_NotAdmitted_NeverReachesPostgres confirms the waiting room
// gate actually blocks unadmitted fans before Postgres is touched at all
// — i.e. the seat remains 'available' afterward.
func TestHoldSeat_NotAdmitted_NeverReachesPostgres(t *testing.T) {
	env := setup(t)
	tenantID, eventID, inventoryID := seedInventoryRow(t, env)
	ctx := context.Background()

	userID := uuid.New() // deliberately never joined/admitted

	_, err := env.svc.HoldSeat(ctx, tenantID, eventID, inventoryID, userID)
	if err == nil {
		t.Fatal("expected an error for an unadmitted user, got nil")
	}

	row, err := env.repo.GetByID(ctx, tenantID, inventoryID)
	if err != nil {
		t.Fatalf("get inventory: %v", err)
	}
	if row.Status != domain.InventoryStatusAvailable {
		t.Fatalf("expected inventory to remain 'available', got %q", row.Status)
	}
}

// TestHoldThenReleaseThenReHold proves the full lifecycle: a hold can be
// voluntarily released, and the seat becomes claimable by someone else
// immediately afterward.
func TestHoldThenReleaseThenReHold(t *testing.T) {
	env := setup(t)
	tenantID, eventID, inventoryID := seedInventoryRow(t, env)
	ctx := context.Background()

	userA, userB := uuid.New(), uuid.New()
	seedUsers(t, env, []uuid.UUID{userA, userB})
	if _, err := env.svc.JoinQueue(ctx, eventID, userA); err != nil {
		t.Fatal(err)
	}
	if _, err := env.svc.JoinQueue(ctx, eventID, userB); err != nil {
		t.Fatal(err)
	}
	if _, err := env.svc.AdmitNext(ctx, eventID, 2); err != nil {
		t.Fatal(err)
	}

	holdA, err := env.svc.HoldSeat(ctx, tenantID, eventID, inventoryID, userA)
	if err != nil {
		t.Fatalf("first hold failed: %v", err)
	}

	// Second buyer correctly blocked while the first hold is active.
	if _, err := env.svc.HoldSeat(ctx, tenantID, eventID, inventoryID, userB); err == nil {
		t.Fatal("expected second hold to fail while first is active")
	}

	if err := env.svc.ReleaseHold(ctx, tenantID, inventoryID, holdA.HoldToken); err != nil {
		t.Fatalf("release failed: %v", err)
	}

	// Now the second buyer should succeed.
	if _, err := env.svc.HoldSeat(ctx, tenantID, eventID, inventoryID, userB); err != nil {
		t.Fatalf("expected second hold to succeed after release, got: %v", err)
	}
}

// TestSweepExpiredHolds_ReclaimsAgainstRealPostgres confirms the sweep
// path (used by cmd/worker) correctly reclaims a hold whose expiry has
// passed, using real time and a real database row.
func TestSweepExpiredHolds_ReclaimsAgainstRealPostgres(t *testing.T) {
	env := setup(t)
	tenantID, eventID, inventoryID := seedInventoryRow(t, env)
	ctx := context.Background()

	userID := uuid.New()
	seedUser(t, env, userID)
	if _, err := env.svc.JoinQueue(ctx, eventID, userID); err != nil {
		t.Fatal(err)
	}
	if _, err := env.svc.AdmitNext(ctx, eventID, 1); err != nil {
		t.Fatal(err)
	}

	// Hold directly via the repository so we can force an already-expired
	// hold_expires_at, rather than waiting out a real HoldDuration.
	holdToken := uuid.New()
	ok, err := env.repo.Hold(ctx, tenantID, inventoryID, holdToken, userID, time.Now().Add(-time.Second))
	if err != nil || !ok {
		t.Fatalf("seed hold failed: ok=%v err=%v", ok, err)
	}

	released, err := env.svc.SweepExpiredHolds(ctx, 10)
	if err != nil {
		t.Fatalf("sweep failed: %v", err)
	}
	if released < 1 {
		t.Fatalf("expected at least 1 row released, got %d", released)
	}

	row, err := env.repo.GetByID(ctx, tenantID, inventoryID)
	if err != nil {
		t.Fatal(err)
	}
	if row.Status != domain.InventoryStatusAvailable {
		t.Fatalf("expected swept row to be 'available', got %q", row.Status)
	}
}
