// Package order turns a set of active seat holds into a paid order.
//
// Design decision, stated up front because it shapes every method below:
// confirming a seat's sale (event_seat_inventory 'held' -> 'sold') is
// deferred until AFTER the payment gateway has captured funds, not done
// alongside order creation. The trade-off:
//
//   - Seat-first (confirm the sale, then charge): a payment failure after
//     the seat is marked 'sold' leaves a sold-but-unpaid ticket that must
//     be explicitly voided or released — an extra failure mode on the
//     *common* path, since most payments succeed.
//   - Payment-first (what's implemented here): the seat never leaves
//     'held' until money has actually moved, so a failed payment needs no
//     inventory cleanup at all — the existing hold just expires normally
//     via internal/service/inventory.SweepExpiredHolds. The cost lands on
//     the rarer path instead: if payment succeeds but the hold has since
//     expired (e.g. an unusually slow 3-D Secure flow outlasted
//     HoldDuration), the seat may already be gone, and that money now has
//     to be refunded — see ConfirmPayment.
//
// Given a ticket drop is exactly the scenario where "seat sold out from
// under a paid fan" is possible at the margins no matter which order you
// pick, and given refunding is a well-trodden, auditable path while
// "silently holding a sold-but-unpaid seat" is not, payment-first was
// chosen.
package order

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gavinarori/ticketing-backend/internal/domain"
	"github.com/gavinarori/ticketing-backend/internal/repository/postgres"
)

// Service orchestrates order creation and payment confirmation. It
// depends on domain repository interfaces for everything except
// transaction boundaries, where it needs the concrete *pgxpool.Pool to
// call postgres.WithTx — the one place in this service that's aware it's
// talking to Postgres specifically, because "run these operations in one
// transaction" is inherently Postgres-shaped in a way the domain-level
// repository interfaces don't (and shouldn't) try to express.
type Service struct {
	pool      *pgxpool.Pool
	orders    domain.OrderRepository
	inventory domain.InventoryRepository
	payments  domain.PaymentRepository
	events    domain.EventRepository
	gateway   domain.PaymentGateway
}

func NewService(
	pool *pgxpool.Pool,
	orders domain.OrderRepository,
	inventory domain.InventoryRepository,
	payments domain.PaymentRepository,
	events domain.EventRepository,
	gateway domain.PaymentGateway,
) *Service {
	return &Service{pool: pool, orders: orders, inventory: inventory, payments: payments, events: events, gateway: gateway}
}

// HeldItem is one seat the caller currently holds — from a prior
// inventory.Service.HoldSeat call — that they want included in this
// order.
type HeldItem struct {
	InventoryID uuid.UUID
	HoldToken   uuid.UUID
}

// CreateOrder validates that every referenced hold is real — still
// 'held', token matches, not expired — prices the order from each item's
// live event_ticket_category, and persists the order + order_items
// atomically. It does NOT confirm any sale or talk to the payment
// gateway; see the package doc for why that's a deliberately separate,
// later step.
//
// idempotencyKey follows the same contract as orders.idempotency_key: a
// retried CreateOrder call with the same key returns the existing order
// instead of creating a duplicate — important because a client retrying
// after a network timeout must never end up with two orders for one
// checkout attempt.
func (s *Service) CreateOrder(ctx context.Context, tenantID, userID uuid.UUID, idempotencyKey string, items []HeldItem) (*domain.Order, error) {
	if len(items) == 0 {
		return nil, domain.NewError("order.CreateOrder", fmt.Errorf("%w: at least one item is required", domain.ErrInvalidInput))
	}

	if existing, err := s.orders.GetByIdempotencyKey(ctx, tenantID, idempotencyKey); err == nil {
		return existing, nil
	} else if !errors.Is(err, domain.ErrNotFound) {
		return nil, fmt.Errorf("order: check idempotency: %w", err)
	}

	var (
		order      *domain.Order
		orderItems []*domain.OrderItem
	)

	err := postgres.WithTx(ctx, s.pool, func(ctx context.Context) error {
		categoryCache := map[uuid.UUID][]*domain.EventTicketCategory{}
		categoryCount := map[uuid.UUID]int{} // EventTicketCategoryID -> count in this order, for MaxPerOrder enforcement

		var subtotal domain.Money
		var tightestExpiry *time.Time
		orderItems = make([]*domain.OrderItem, 0, len(items))

		for i, item := range items {
			row, err := s.inventory.GetByID(ctx, tenantID, item.InventoryID)
			if err != nil {
				return fmt.Errorf("order: load held item %s: %w", item.InventoryID, err)
			}
			if row.Status != domain.InventoryStatusHeld || row.HoldToken == nil || *row.HoldToken != item.HoldToken {
				return domain.NewError("order.CreateOrder", fmt.Errorf("%w: item %s is not held by the supplied token", domain.ErrExpired, item.InventoryID))
			}
			if row.HoldExpiresAt != nil && row.HoldExpiresAt.Before(time.Now()) {
				return domain.NewError("order.CreateOrder", fmt.Errorf("%w: item %s's hold has expired", domain.ErrExpired, item.InventoryID))
			}
			if tightestExpiry == nil || (row.HoldExpiresAt != nil && row.HoldExpiresAt.Before(*tightestExpiry)) {
				tightestExpiry = row.HoldExpiresAt
			}

			categories, ok := categoryCache[row.EventID]
			if !ok {
				categories, err = s.events.ListTicketCategories(ctx, tenantID, row.EventID)
				if err != nil {
					return fmt.Errorf("order: load ticket categories for event %s: %w", row.EventID, err)
				}
				categoryCache[row.EventID] = categories
			}

			var price domain.Money
			var maxPerOrder int
			found := false
			for _, c := range categories {
				if c.ID == row.EventTicketCategoryID {
					price, maxPerOrder, found = c.Price, c.MaxPerOrder, true
					break
				}
			}
			if !found {
				return fmt.Errorf("order: no ticket category %s found for event %s", row.EventTicketCategoryID, row.EventID)
			}

			categoryCount[row.EventTicketCategoryID]++
			if categoryCount[row.EventTicketCategoryID] > maxPerOrder {
				return domain.NewError("order.CreateOrder", fmt.Errorf("%w: exceeds max %d per order for this ticket category", domain.ErrInvalidInput, maxPerOrder))
			}

			if i == 0 {
				subtotal = price
			} else {
				subtotal, err = subtotal.Add(price)
				if err != nil {
					return fmt.Errorf("order: sum prices: %w", err)
				}
			}

			orderItems = append(orderItems, &domain.OrderItem{
				ID:                   uuid.New(),
				TenantID:             tenantID,
				EventSeatInventoryID: row.ID,
				HoldToken:            item.HoldToken,
				UnitPrice:            price,
			})
		}

		// Fee calculation (platform fees, taxes) is a policy decision
		// deliberately left for later — zero fees keeps this flow correct
		// and shippable now without guessing at a fee schedule.
		fees := domain.Money{Cents: 0, Currency: subtotal.Currency}
		total, err := subtotal.Add(fees)
		if err != nil {
			return fmt.Errorf("order: compute total: %w", err)
		}

		order = &domain.Order{
			ID:             uuid.New(),
			TenantID:       tenantID,
			UserID:         userID,
			Status:         domain.OrderStatusPending,
			IdempotencyKey: idempotencyKey,
			Subtotal:       subtotal,
			Fees:           fees,
			Total:          total,
			ExpiresAt:      tightestExpiry,
		}
		for _, it := range orderItems {
			it.OrderID = order.ID
		}

		if err := s.orders.CreateWithItems(ctx, order, orderItems); err != nil {
			return fmt.Errorf("order: create with items: %w", err)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	return order, nil
}

// AuthorizePayment creates a payment intent with the gateway for an
// existing pending order and records it. Deliberately a separate call
// from CreateOrder, and not wrapped in a database transaction: making an
// external network call while holding a DB transaction open is an
// anti-pattern regardless of provider — it holds locks and a connection
// for however long that call takes, and a slow or unavailable payment
// provider would otherwise make order creation itself fail even though
// nothing about the order was actually invalid.
func (s *Service) AuthorizePayment(ctx context.Context, tenantID, orderID uuid.UUID) (clientSecret string, err error) {
	order, err := s.orders.GetByID(ctx, tenantID, orderID)
	if err != nil {
		return "", fmt.Errorf("order: get order: %w", err)
	}
	if order.Status != domain.OrderStatusPending {
		return "", domain.NewError("order.AuthorizePayment", fmt.Errorf("%w: order is %q, not pending", domain.ErrInvalidInput, order.Status))
	}

	providerPaymentID, clientSecret, err := s.gateway.CreateIntent(ctx, order.Total, order.IdempotencyKey, map[string]string{
		"order_id":  order.ID.String(),
		"tenant_id": order.TenantID.String(),
	})
	if err != nil {
		return "", fmt.Errorf("order: create payment intent: %w", err)
	}

	payment := &domain.Payment{
		ID:                uuid.New(),
		TenantID:          tenantID,
		OrderID:           order.ID,
		Provider:          domain.PaymentProviderStripe,
		ProviderPaymentID: providerPaymentID,
		Status:            domain.PaymentStatusPending,
		Amount:            order.Total,
		IdempotencyKey:    order.IdempotencyKey,
	}
	if err := s.payments.Create(ctx, payment); err != nil {
		return "", fmt.Errorf("order: record payment: %w", err)
	}
	if err := s.orders.UpdateStatus(ctx, tenantID, order.ID, domain.OrderStatusAwaitingPayment); err != nil {
		return "", fmt.Errorf("order: update order status: %w", err)
	}

	return clientSecret, nil
}

// ConfirmPayment is called once the payment gateway confirms capture —
// in production, from a webhook handler (not built in this task; see
// docs/order-flow.md). It atomically converts every held inventory row
// backing this order into 'sold' and marks the order 'paid', or aborts
// the whole order — no partial ticket sales — if even one item's hold is
// no longer valid, automatically refunding the payment in that case.
func (s *Service) ConfirmPayment(ctx context.Context, tenantID, orderID, paymentID uuid.UUID) error {
	order, err := s.orders.GetByID(ctx, tenantID, orderID)
	if err != nil {
		return fmt.Errorf("order: get order: %w", err)
	}
	payment, err := s.payments.GetByID(ctx, tenantID, paymentID)
	if err != nil {
		return fmt.Errorf("order: get payment: %w", err)
	}
	if payment.OrderID != order.ID {
		return domain.NewError("order.ConfirmPayment", fmt.Errorf("%w: payment does not belong to order", domain.ErrInvalidInput))
	}

	items, err := s.orders.ListItems(ctx, tenantID, order.ID)
	if err != nil {
		return fmt.Errorf("order: list order items: %w", err)
	}

	txErr := postgres.WithTx(ctx, s.pool, func(ctx context.Context) error {
		for _, item := range items {
			ok, err := s.inventory.ConfirmSale(ctx, tenantID, item.EventSeatInventoryID, item.HoldToken)
			if err != nil {
				return fmt.Errorf("order: confirm sale for item %s: %w", item.ID, err)
			}
			if !ok {
				return domain.NewError("order.ConfirmPayment", fmt.Errorf("%w: item %s's hold is no longer valid", domain.ErrExpired, item.ID))
			}
		}
		if err := s.orders.UpdateStatus(ctx, tenantID, order.ID, domain.OrderStatusPaid); err != nil {
			return fmt.Errorf("order: mark order paid: %w", err)
		}
		return nil
	})

	if txErr != nil {
		// The transaction above rolled back cleanly. If it failed because
		// a hold genuinely expired between authorization and now, money
		// has already moved at the gateway but the order can't be
		// fulfilled — it must be refunded and the order marked failed
		// rather than left silently 'awaiting_payment' forever. Any other
		// failure (e.g. a transient DB error) is left as-is: the order
		// stays 'awaiting_payment' and ConfirmPayment can safely be
		// retried later, since nothing partial was committed.
		if errors.Is(txErr, domain.ErrExpired) {
			if refundErr := s.refundAndFail(ctx, tenantID, order, payment); refundErr != nil {
				return fmt.Errorf("order: confirm failed AND refund failed (needs manual intervention): confirm=%v refund=%w", txErr, refundErr)
			}
			return fmt.Errorf("order: could not confirm sale, payment refunded: %w", txErr)
		}
		return txErr
	}

	if err := s.payments.UpdateStatus(ctx, tenantID, payment.ID, domain.PaymentStatusCaptured, "", nil); err != nil {
		return fmt.Errorf("order: mark payment captured: %w", err)
	}
	return nil
}

// refundAndFail is the compensating action for the "payment succeeded but
// inventory could no longer confirm" case. Deliberately runs OUTSIDE any
// database transaction — like AuthorizePayment, this makes an external
// network call, which must never happen with a DB transaction held open.
func (s *Service) refundAndFail(ctx context.Context, tenantID uuid.UUID, order *domain.Order, payment *domain.Payment) error {
	refundID, err := s.gateway.Refund(ctx, payment.ProviderPaymentID, payment.Amount, "inventory hold expired before fulfillment could be confirmed")
	if err != nil {
		return fmt.Errorf("gateway refund: %w", err)
	}

	// Record the refund ID in raw_response rather than overwriting
	// provider_payment_id (which must keep pointing at the original
	// charge, not the refund) — see PaymentRepo.UpdateStatus's COALESCE
	// semantics for why passing "" for providerPaymentID here is safe.
	rawResponse := []byte(fmt.Sprintf(`{"refund_id":%q}`, refundID))
	if err := s.payments.UpdateStatus(ctx, tenantID, payment.ID, domain.PaymentStatusRefunded, "", rawResponse); err != nil {
		return fmt.Errorf("mark payment refunded: %w", err)
	}
	if err := s.orders.UpdateStatus(ctx, tenantID, order.ID, domain.OrderStatusExpired); err != nil {
		return fmt.Errorf("mark order expired: %w", err)
	}
	return nil
}
