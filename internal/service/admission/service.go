// Package admission implements the admission-rate POLICY that
// internal/service/inventory.Service.AdmitNext deliberately leaves to its
// caller (see that method's own doc comment: "how exactly that admission
// rate is computed is a policy decision left to the caller"). This
// package is that caller — kept separate from the inventory service so
// the policy (how many fans to let through, how often) can evolve
// independently of the locking mechanics it sits on top of.
package admission

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/gavinarori/ticketing-backend/internal/domain"
	invsvc "github.com/gavinarori/ticketing-backend/internal/service/inventory"
)

// Service periodically discovers on-sale events with fans waiting and
// admits a batch sized to currently available inventory.
type Service struct {
	tenants         domain.TenantRepository
	events          domain.EventRepository
	inventoryRepo   domain.InventoryRepository
	inventory       *invsvc.Service
	maxAdmitPerTick int64
	log             *zap.Logger
}

func NewService(
	tenants domain.TenantRepository,
	events domain.EventRepository,
	inventoryRepo domain.InventoryRepository,
	inventory *invsvc.Service,
	maxAdmitPerTick int64,
	log *zap.Logger,
) *Service {
	return &Service{
		tenants: tenants, events: events, inventoryRepo: inventoryRepo,
		inventory: inventory, maxAdmitPerTick: maxAdmitPerTick, log: log,
	}
}

// RunOnce processes every active tenant's on-sale events once: for each
// event with fans in the waiting room, it admits
// min(queue length, total available inventory, maxAdmitPerTick) fans.
//
// The policy itself — "admit up to as many fans as there are seats
// left, capped so one huge event can't starve every other tenant's
// admission processing in the same tick" — is a starting heuristic, not
// a tuned algorithm. It intentionally errs toward simplicity: it doesn't
// account for how many currently-admitted fans are likely to actually
// convert, doesn't back off under sustained contention, and doesn't
// prioritize across tenants. Good enough to make the waiting room
// self-operating; a real capacity-planning model is future work.
//
// A failure processing one event (a transient Redis/Postgres error, a
// malformed row) is logged and skipped rather than aborting the whole
// run — one bad event must never block admission for every other tenant
// sharing this same worker tick.
func (s *Service) RunOnce(ctx context.Context) (admittedTotal int, err error) {
	tenants, err := s.tenants.List(ctx, domain.TenantStatusActive, 1000, 0)
	if err != nil {
		return 0, fmt.Errorf("admission: list tenants: %w", err)
	}

	onSale := domain.EventStatusOnSale
	for _, tenant := range tenants {
		events, err := s.events.List(ctx, domain.EventFilter{TenantID: tenant.ID, Status: &onSale}, 1000, 0)
		if err != nil {
			s.log.Error("admission_list_events_failed", zap.String("tenant_id", tenant.ID.String()), zap.Error(err))
			continue
		}

		for _, event := range events {
			admitted, err := s.admitForEvent(ctx, tenant.ID, event.ID)
			if err != nil {
				s.log.Error("admission_process_event_failed",
					zap.String("tenant_id", tenant.ID.String()), zap.String("event_id", event.ID.String()), zap.Error(err))
				continue
			}
			admittedTotal += admitted
		}
	}

	return admittedTotal, nil
}

func (s *Service) admitForEvent(ctx context.Context, tenantID, eventID uuid.UUID) (int, error) {
	queueLen, err := s.inventory.QueueLength(ctx, eventID)
	if err != nil {
		return 0, fmt.Errorf("queue length: %w", err)
	}
	if queueLen == 0 {
		return 0, nil
	}

	counts, err := s.inventoryRepo.CountByStatus(ctx, tenantID, eventID)
	if err != nil {
		return 0, fmt.Errorf("count by status: %w", err)
	}
	var totalAvailable int64
	for _, c := range counts {
		totalAvailable += int64(c.Available)
	}
	if totalAvailable == 0 {
		return 0, nil
	}

	admitCount := min64(queueLen, totalAvailable, s.maxAdmitPerTick)
	if admitCount <= 0 {
		return 0, nil
	}

	admitted, err := s.inventory.AdmitNext(ctx, eventID, admitCount)
	if err != nil {
		return 0, fmt.Errorf("admit next: %w", err)
	}
	if len(admitted) > 0 {
		s.log.Info("admission_batch",
			zap.String("event_id", eventID.String()), zap.Int("admitted", len(admitted)),
			zap.Int64("queue_length_before", queueLen), zap.Int64("available", totalAvailable))
	}
	return len(admitted), nil
}

func min64(vals ...int64) int64 {
	m := vals[0]
	for _, v := range vals[1:] {
		if v < m {
			m = v
		}
	}
	return m
}
