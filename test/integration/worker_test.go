//go:build integration

package integration

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/gavinarori/ticketing-backend/internal/domain"
	"github.com/gavinarori/ticketing-backend/internal/service/admission"
)

// TestAdmission_RunOnce_AdmitsUpToAvailableInventory proves the core
// admission policy against real Postgres + Redis: a fan joins the queue
// for an event with 2 available seats, RunOnce admits them (bounded by
// availability), and a second fan beyond capacity is correctly left
// waiting rather than admitted past what the event can actually sell.
func TestAdmission_RunOnce_AdmitsUpToAvailableInventory(t *testing.T) {
	env := setup(t)
	ctx := context.Background()

	tenantID, eventID, _ := seedInventoryRow(t, env) // 1 inventory row seeded

	// Add a second inventory row so this event has 2 available seats
	// total, to prove RunOnce's min(queue, available, cap) bound rather
	// than just "admits everyone."
	secondInvID := uuid.New()
	var etcID uuid.UUID
	if err := env.pool.QueryRow(ctx, `SELECT event_ticket_category_id FROM event_seat_inventory WHERE event_id = $1 LIMIT 1`, eventID).Scan(&etcID); err != nil {
		t.Fatal(err)
	}
	if _, err := env.pool.Exec(ctx,
		`INSERT INTO event_seat_inventory (id, tenant_id, event_id, event_ticket_category_id) VALUES ($1, $2, $3, $4)`,
		secondInvID, tenantID, eventID, etcID,
	); err != nil {
		t.Fatal(err)
	}

	userA, userB, userC := uuid.New(), uuid.New(), uuid.New()
	seedUsers(t, env, []uuid.UUID{userA, userB, userC})

	for _, u := range []uuid.UUID{userA, userB, userC} {
		if _, err := env.svc.JoinQueue(ctx, eventID, u); err != nil {
			t.Fatal(err)
		}
	}

	admissionSvc := admission.NewService(env.tenantRepo, env.eventRepo, env.repo, env.svc, 100, zap.NewNop())

	admitted, err := admissionSvc.RunOnce(ctx)
	if err != nil {
		t.Fatalf("RunOnce failed: %v", err)
	}
	if admitted != 2 {
		t.Fatalf("expected exactly 2 admitted (bounded by 2 available seats, not the 3 waiting), got %d", admitted)
	}

	// The third fan (whoever joined last, per FIFO) must still be waiting.
	statusA, err := env.svc.QueueStatus(ctx, eventID, userA)
	if err != nil {
		t.Fatal(err)
	}
	statusC, err := env.svc.QueueStatus(ctx, eventID, userC)
	if err != nil {
		t.Fatal(err)
	}
	if !statusA.Admitted {
		t.Error("expected the first fan to join (userA) to be admitted")
	}
	if statusC.Admitted {
		t.Error("expected the last fan to join (userC) to still be waiting — only 2 of 3 should be admitted")
	}
}

// TestAdmission_RunOnce_SkipsEventsWithEmptyQueue confirms RunOnce
// doesn't touch (or error on) an on-sale event nobody is waiting for —
// the common case in any real tick. Scoped to this test's own event
// specifically: RunOnce is deliberately platform-wide (see its doc
// comment, matching SweepExpiredHolds' own platform-wide design), so
// asserting a global "0 admitted" would be flaky against a shared test
// database where other tests' events may have their own leftover queued
// fans still waiting — that's correct RunOnce behavior, not something
// this test should be sensitive to.
func TestAdmission_RunOnce_SkipsEventsWithEmptyQueue(t *testing.T) {
	env := setup(t)
	ctx := context.Background()
	_, eventID, _ := seedInventoryRow(t, env)

	admissionSvc := admission.NewService(env.tenantRepo, env.eventRepo, env.repo, env.svc, 100, zap.NewNop())

	if _, err := admissionSvc.RunOnce(ctx); err != nil {
		t.Fatalf("RunOnce failed: %v", err)
	}

	length, err := env.svc.QueueLength(ctx, eventID)
	if err != nil {
		t.Fatal(err)
	}
	if length != 0 {
		t.Errorf("expected our event's queue length to remain 0, got %d", length)
	}
}

// TestAdmission_RunOnce_RespectsMaxPerTick confirms the per-tick cap is
// actually enforced, independent of how much inventory or queue depth an
// event has — the safeguard against one huge event starving every other
// tenant's admission processing in the same tick.
func TestAdmission_RunOnce_RespectsMaxPerTick(t *testing.T) {
	env := setup(t)
	ctx := context.Background()
	tenantID, eventID, firstInvID := seedInventoryRow(t, env)

	var etcID uuid.UUID
	if err := env.pool.QueryRow(ctx, `SELECT event_ticket_category_id FROM event_seat_inventory WHERE id = $1`, firstInvID).Scan(&etcID); err != nil {
		t.Fatal(err)
	}
	// Generate 5 more inventory rows (6 total) so availability isn't the
	// limiting factor — the maxPerTick cap of 2 should be.
	rows := make([]*domain.EventSeatInventory, 0, 5)
	for i := 0; i < 5; i++ {
		rows = append(rows, &domain.EventSeatInventory{
			ID: uuid.New(), TenantID: tenantID, EventID: eventID, EventTicketCategoryID: etcID,
		})
	}
	if err := env.repo.BulkCreate(ctx, rows); err != nil {
		t.Fatal(err)
	}

	users := make([]uuid.UUID, 5)
	for i := range users {
		users[i] = uuid.New()
	}
	seedUsers(t, env, users)
	for _, u := range users {
		if _, err := env.svc.JoinQueue(ctx, eventID, u); err != nil {
			t.Fatal(err)
		}
	}

	const maxPerTick = 2
	admissionSvc := admission.NewService(env.tenantRepo, env.eventRepo, env.repo, env.svc, maxPerTick, zap.NewNop())

	admitted, err := admissionSvc.RunOnce(ctx)
	if err != nil {
		t.Fatalf("RunOnce failed: %v", err)
	}
	if admitted != maxPerTick {
		t.Fatalf("expected exactly %d admitted (the per-tick cap), got %d despite more queue and inventory being available", maxPerTick, admitted)
	}
}

// TestSweepLoop_ReclaimsAcrossMultipleEvents proves the sweep operates
// platform-wide (no tenant/event filter), reclaiming expired holds
// belonging to two entirely separate seeded events in a single call —
// this is what runSweepLoop in cmd/worker actually calls on a ticker.
func TestSweepLoop_ReclaimsAcrossMultipleEvents(t *testing.T) {
	env := setup(t)
	ctx := context.Background()

	tenantA, _, invA := seedInventoryRow(t, env)
	tenantB, _, invB := seedInventoryRow(t, env)
	userA, userB := uuid.New(), uuid.New()
	seedUsers(t, env, []uuid.UUID{userA, userB})

	tokenA, tokenB := uuid.New(), uuid.New()
	past := time.Now().Add(-time.Minute)
	if ok, err := env.repo.Hold(ctx, tenantA, invA, tokenA, userA, past); err != nil || !ok {
		t.Fatalf("seed hold A failed: ok=%v err=%v", ok, err)
	}
	if ok, err := env.repo.Hold(ctx, tenantB, invB, tokenB, userB, past); err != nil || !ok {
		t.Fatalf("seed hold B failed: ok=%v err=%v", ok, err)
	}

	released, err := env.svc.SweepExpiredHolds(ctx, 100)
	if err != nil {
		t.Fatalf("sweep failed: %v", err)
	}
	if released < 2 {
		t.Fatalf("expected at least 2 rows released across both tenants' events, got %d", released)
	}

	rowA, err := env.repo.GetByID(ctx, tenantA, invA)
	if err != nil {
		t.Fatal(err)
	}
	rowB, err := env.repo.GetByID(ctx, tenantB, invB)
	if err != nil {
		t.Fatal(err)
	}
	if rowA.Status != domain.InventoryStatusAvailable || rowB.Status != domain.InventoryStatusAvailable {
		t.Errorf("expected both tenants' expired holds reclaimed, got %q and %q", rowA.Status, rowB.Status)
	}
}
