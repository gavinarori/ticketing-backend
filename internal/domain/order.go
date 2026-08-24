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
	UnitPrice            Money
	CreatedAt            time.Time
}

// OrderRepository. CreateWithItems takes the order and its items together
// and must be implemented as a single database transaction — an order
// with zero items, or items pointing at inventory rows that were never
// actually held by this order, must never be observable by any other
// reader.
type OrderRepository interface {
	CreateWithItems(ctx context.Context, order *Order, items []*OrderItem) error
	GetByID(ctx context.Context, tenantID, id uuid.UUID) (*Order, error)
	GetByIdempotencyKey(ctx context.Context, tenantID uuid.UUID, key string) (*Order, error)
	ListItems(ctx context.Context, tenantID, orderID uuid.UUID) ([]*OrderItem, error)
	UpdateStatus(ctx context.Context, tenantID, id uuid.UUID, status OrderStatus) error
	ListByUser(ctx context.Context, tenantID, userID uuid.UUID, limit, offset int) ([]*Order, error)
}
