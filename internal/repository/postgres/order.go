package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gavinarori/ticketing-backend/internal/domain"
)

// OrderRepo implements domain.OrderRepository against the orders and
// order_items tables (migrations/000007, extended by 000012 for
// order_items.hold_token).
type OrderRepo struct {
	pool *pgxpool.Pool
}

func NewOrderRepo(pool *pgxpool.Pool) *OrderRepo { return &OrderRepo{pool: pool} }

var _ domain.OrderRepository = (*OrderRepo)(nil)

const orderColumns = `id, tenant_id, user_id, status, idempotency_key, currency, subtotal_cents, fees_cents, total_cents, expires_at, created_at, updated_at`

func scanOrder(row pgx.Row) (*domain.Order, error) {
	var o domain.Order
	var status, currency string
	var subtotalCents, feesCents, totalCents int64
	if err := row.Scan(
		&o.ID, &o.TenantID, &o.UserID, &status, &o.IdempotencyKey, &currency,
		&subtotalCents, &feesCents, &totalCents, &o.ExpiresAt, &o.CreatedAt, &o.UpdatedAt,
	); err != nil {
		return nil, err
	}
	o.Status = domain.OrderStatus(status)
	o.Subtotal = domain.Money{Cents: subtotalCents, Currency: currency}
	o.Fees = domain.Money{Cents: feesCents, Currency: currency}
	o.Total = domain.Money{Cents: totalCents, Currency: currency}
	return &o, nil
}

// CreateWithItems inserts the order row and every order item. It
// deliberately does NOT open its own transaction — see
// domain.OrderRepository's doc comment: the caller
// (internal/service/order) is responsible for wrapping this call in
// postgres.WithTx so the order insert and the order_items batch insert
// commit or roll back together. Called outside WithTx it still works,
// just without that atomicity guarantee.
func (r *OrderRepo) CreateWithItems(ctx context.Context, order *domain.Order, items []*domain.OrderItem) error {
	if order.ID == uuid.Nil {
		order.ID = uuid.New()
	}

	_, err := db(ctx, r.pool).Exec(ctx,
		`INSERT INTO orders (id, tenant_id, user_id, status, idempotency_key, currency, subtotal_cents, fees_cents, total_cents, expires_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`,
		order.ID, order.TenantID, order.UserID, string(order.Status), order.IdempotencyKey,
		order.Total.Currency, order.Subtotal.Cents, order.Fees.Cents, order.Total.Cents, order.ExpiresAt,
	)
	if err != nil {
		if pgErrorCode(err) == pgCodeUniqueViolation {
			return domain.NewError("postgres.OrderRepo.CreateWithItems", domain.ErrConflict)
		}
		return fmt.Errorf("postgres: create order: %w", err)
	}

	if len(items) == 0 {
		return nil
	}

	batch := &pgx.Batch{}
	for _, item := range items {
		id := item.ID
		if id == uuid.Nil {
			id = uuid.New()
		}
		batch.Queue(
			`INSERT INTO order_items (id, tenant_id, order_id, event_seat_inventory_id, hold_token, unit_price_cents, currency)
			 VALUES ($1, $2, $3, $4, $5, $6, $7)`,
			id, order.TenantID, order.ID, item.EventSeatInventoryID, item.HoldToken, item.UnitPrice.Cents, item.UnitPrice.Currency,
		)
	}
	br := db(ctx, r.pool).SendBatch(ctx, batch)
	defer br.Close()
	for range items {
		if _, err := br.Exec(); err != nil {
			return fmt.Errorf("postgres: create order item: %w", err)
		}
	}
	return nil
}

func (r *OrderRepo) GetByID(ctx context.Context, tenantID, id uuid.UUID) (*domain.Order, error) {
	row := db(ctx, r.pool).QueryRow(ctx, `SELECT `+orderColumns+` FROM orders WHERE id = $1 AND tenant_id = $2`, id, tenantID)
	o, err := scanOrder(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, domain.NewError("postgres.OrderRepo.GetByID", domain.ErrNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("postgres: get order: %w", err)
	}
	return o, nil
}

func (r *OrderRepo) GetByIdempotencyKey(ctx context.Context, tenantID uuid.UUID, key string) (*domain.Order, error) {
	row := db(ctx, r.pool).QueryRow(ctx, `SELECT `+orderColumns+` FROM orders WHERE tenant_id = $1 AND idempotency_key = $2`, tenantID, key)
	o, err := scanOrder(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, domain.NewError("postgres.OrderRepo.GetByIdempotencyKey", domain.ErrNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("postgres: get order by idempotency key: %w", err)
	}
	return o, nil
}

func (r *OrderRepo) ListItems(ctx context.Context, tenantID, orderID uuid.UUID) ([]*domain.OrderItem, error) {
	rows, err := db(ctx, r.pool).Query(ctx,
		`SELECT id, tenant_id, order_id, event_seat_inventory_id, hold_token, unit_price_cents, currency, created_at
		 FROM order_items WHERE tenant_id = $1 AND order_id = $2 ORDER BY created_at`,
		tenantID, orderID,
	)
	if err != nil {
		return nil, fmt.Errorf("postgres: list order items: %w", err)
	}
	defer rows.Close()

	var items []*domain.OrderItem
	for rows.Next() {
		var it domain.OrderItem
		var cents int64
		var currency string
		if err := rows.Scan(&it.ID, &it.TenantID, &it.OrderID, &it.EventSeatInventoryID, &it.HoldToken, &cents, &currency, &it.CreatedAt); err != nil {
			return nil, fmt.Errorf("postgres: scan order item: %w", err)
		}
		it.UnitPrice = domain.Money{Cents: cents, Currency: currency}
		items = append(items, &it)
	}
	return items, rows.Err()
}

func (r *OrderRepo) UpdateStatus(ctx context.Context, tenantID, id uuid.UUID, status domain.OrderStatus) error {
	tag, err := db(ctx, r.pool).Exec(ctx,
		`UPDATE orders SET status = $1 WHERE id = $2 AND tenant_id = $3`,
		string(status), id, tenantID,
	)
	if err != nil {
		return fmt.Errorf("postgres: update order status: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return domain.NewError("postgres.OrderRepo.UpdateStatus", domain.ErrNotFound)
	}
	return nil
}

func (r *OrderRepo) ListByUser(ctx context.Context, tenantID, userID uuid.UUID, limit, offset int) ([]*domain.Order, error) {
	rows, err := db(ctx, r.pool).Query(ctx,
		`SELECT `+orderColumns+` FROM orders WHERE tenant_id = $1 AND user_id = $2 ORDER BY created_at DESC LIMIT $3 OFFSET $4`,
		tenantID, userID, limit, offset,
	)
	if err != nil {
		return nil, fmt.Errorf("postgres: list orders by user: %w", err)
	}
	defer rows.Close()

	var out []*domain.Order
	for rows.Next() {
		o, err := scanOrder(rows)
		if err != nil {
			return nil, fmt.Errorf("postgres: scan order: %w", err)
		}
		out = append(out, o)
	}
	return out, rows.Err()
}
