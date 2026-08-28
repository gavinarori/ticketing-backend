package domain

import (
	"context"
	"time"

	"github.com/google/uuid"
)

type OrderStatus string

const (
	OrderStatusPending         OrderStatus = "pending"
	OrderStatusAwaitingPayment OrderStatus = "awaiting_payment"
	OrderStatusPaid            OrderStatus = "paid"
	OrderStatusCancelled       OrderStatus = "cancelled"
	OrderStatusExpired         OrderStatus = "expired"
	OrderStatusRefunded        OrderStatus = "refunded"
)

type Order struct {
	ID             uuid.UUID
	TenantID       uuid.UUID
	UserID         uuid.UUID
	Status         OrderStatus
	IdempotencyKey string
	Subtotal       Money
	Fees           Money
	Total          Money
	// ExpiresAt mirrors the tightest HoldExpiresAt among this order's
	// items — the order (and its held inventory) is abandoned if payment
	// doesn't land before this.
	ExpiresAt *time.Time
	CreatedAt time.Time
	UpdatedAt time.Time
}

type OrderItem struct {
	ID                   uuid.UUID
	TenantID             uuid.UUID
	OrderID              uuid.UUID
	EventSeatInventoryID uuid.UUID
	// HoldToken is the token this specific item's hold was created with —
	// NOT necessarily the token currently sitting on the
	// event_seat_inventory row. Confirming a sale (see
	// domain.InventoryRepository.ConfirmSale) is gated on hold_token
	// matching, and if confirmation is deferred until after a payment
	// gateway capture succeeds (see internal/service/order), the row's
	// *current* hold_token by then might belong to a completely different
	// holder — this row could have expired and been re-held by someone
	// else in the meantime. Only the token this order_item was actually
	// created with is safe to use for ConfirmSale at that later point, so
	// it must be persisted here rather than re-read from the inventory
	// row when needed.
	HoldToken uuid.UUID
	UnitPrice Money
	CreatedAt time.Time
}

// OrderRepository. CreateWithItems takes the order and its items
// together. Unlike a typical repository method, it deliberately does NOT
// open its own transaction — see internal/repository/postgres's
// implementation and internal/repository/postgres/tx.go's WithTx. The
// caller (internal/service/order) is responsible for wrapping this call
// in postgres.WithTx, which is what actually guarantees an order with
// zero items, or items pointing at inventory rows that were never
// genuinely held by this order, is never observable by any other reader.
type OrderRepository interface {
	CreateWithItems(ctx context.Context, order *Order, items []*OrderItem) error
	GetByID(ctx context.Context, tenantID, id uuid.UUID) (*Order, error)
	GetByIdempotencyKey(ctx context.Context, tenantID uuid.UUID, key string) (*Order, error)
	ListItems(ctx context.Context, tenantID, orderID uuid.UUID) ([]*OrderItem, error)
	UpdateStatus(ctx context.Context, tenantID, id uuid.UUID, status OrderStatus) error
	ListByUser(ctx context.Context, tenantID, userID uuid.UUID, limit, offset int) ([]*Order, error)
}
