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

type PaymentRepo struct {
	pool *pgxpool.Pool
}

func NewPaymentRepo(pool *pgxpool.Pool) *PaymentRepo { return &PaymentRepo{pool: pool} }

var _ domain.PaymentRepository = (*PaymentRepo)(nil)

const paymentColumns = `id, tenant_id, order_id, provider, provider_payment_id, status, amount_cents, currency, idempotency_key, raw_response, created_at, updated_at`

func scanPayment(row pgx.Row) (*domain.Payment, error) {
	var p domain.Payment
	var provider, status, currency string
	var providerPaymentID *string
	var amountCents int64
	if err := row.Scan(
		&p.ID, &p.TenantID, &p.OrderID, &provider, &providerPaymentID, &status,
		&amountCents, &currency, &p.IdempotencyKey, &p.RawResponse, &p.CreatedAt, &p.UpdatedAt,
	); err != nil {
		return nil, err
	}
	p.Provider = domain.PaymentProvider(provider)
	p.Status = domain.PaymentStatus(status)
	p.Amount = domain.Money{Cents: amountCents, Currency: currency}
	if providerPaymentID != nil {
		p.ProviderPaymentID = *providerPaymentID
	}
	return &p, nil
}

func (r *PaymentRepo) Create(ctx context.Context, p *domain.Payment) error {
	if p.ID == uuid.Nil {
		p.ID = uuid.New()
	}
	var providerPaymentID *string
	if p.ProviderPaymentID != "" {
		providerPaymentID = &p.ProviderPaymentID
	}
	_, err := db(ctx, r.pool).Exec(ctx,
		`INSERT INTO payments (id, tenant_id, order_id, provider, provider_payment_id, status, amount_cents, currency, idempotency_key, raw_response)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`,
		p.ID, p.TenantID, p.OrderID, string(p.Provider), providerPaymentID, string(p.Status),
		p.Amount.Cents, p.Amount.Currency, p.IdempotencyKey, p.RawResponse,
	)
	if err != nil {
		if pgErrorCode(err) == pgCodeUniqueViolation {
			return domain.NewError("postgres.PaymentRepo.Create", domain.ErrConflict)
		}
		return fmt.Errorf("postgres: create payment: %w", err)
	}
	return nil
}

func (r *PaymentRepo) GetByID(ctx context.Context, tenantID, id uuid.UUID) (*domain.Payment, error) {
	row := db(ctx, r.pool).QueryRow(ctx, `SELECT `+paymentColumns+` FROM payments WHERE id = $1 AND tenant_id = $2`, id, tenantID)
	p, err := scanPayment(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, domain.NewError("postgres.PaymentRepo.GetByID", domain.ErrNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("postgres: get payment: %w", err)
	}
	return p, nil
}

func (r *PaymentRepo) GetByIdempotencyKey(ctx context.Context, tenantID uuid.UUID, key string) (*domain.Payment, error) {
	row := db(ctx, r.pool).QueryRow(ctx, `SELECT `+paymentColumns+` FROM payments WHERE tenant_id = $1 AND idempotency_key = $2`, tenantID, key)
	p, err := scanPayment(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, domain.NewError("postgres.PaymentRepo.GetByIdempotencyKey", domain.ErrNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("postgres: get payment by idempotency key: %w", err)
	}
	return p, nil
}

// UpdateStatus updates status and, only when non-empty/non-nil, the
// provider_payment_id and raw_response columns — COALESCE preserves the
// existing value otherwise. This lets callers update just the status
// (e.g. marking 'refunded') without accidentally clobbering the original
// Stripe/Adyen payment ID with an empty value.
func (r *PaymentRepo) UpdateStatus(ctx context.Context, tenantID, id uuid.UUID, status domain.PaymentStatus, providerPaymentID string, rawResponse []byte) error {
	var ppid *string
	if providerPaymentID != "" {
		ppid = &providerPaymentID
	}
	tag, err := db(ctx, r.pool).Exec(ctx,
		`UPDATE payments
		 SET status = $1,
		     provider_payment_id = COALESCE($2, provider_payment_id),
		     raw_response = COALESCE($3, raw_response)
		 WHERE id = $4 AND tenant_id = $5`,
		string(status), ppid, rawResponse, id, tenantID,
	)
	if err != nil {
		return fmt.Errorf("postgres: update payment status: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return domain.NewError("postgres.PaymentRepo.UpdateStatus", domain.ErrNotFound)
	}
	return nil
}
