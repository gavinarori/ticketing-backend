package domain

import (
	"context"
	"time"

	"github.com/google/uuid"
)

type PaymentProvider string

const (
	PaymentProviderStripe PaymentProvider = "stripe"
	PaymentProviderAdyen  PaymentProvider = "adyen"
)

type PaymentStatus string

const (
	PaymentStatusPending           PaymentStatus = "pending"
	PaymentStatusAuthorized        PaymentStatus = "authorized"
	PaymentStatusCaptured          PaymentStatus = "captured"
	PaymentStatusFailed            PaymentStatus = "failed"
	PaymentStatusRefunded          PaymentStatus = "refunded"
	PaymentStatusPartiallyRefunded PaymentStatus = "partially_refunded"
)

type Payment struct {
	ID                uuid.UUID
	TenantID          uuid.UUID
	OrderID           uuid.UUID
	Provider          PaymentProvider
	ProviderPaymentID string
	Status            PaymentStatus
	Amount            Money
	IdempotencyKey    string
	RawResponse       []byte // raw JSON from the provider, stored as-is for support/debugging
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

type PaymentRepository interface {
	Create(ctx context.Context, p *Payment) error
	GetByID(ctx context.Context, tenantID, id uuid.UUID) (*Payment, error)
	GetByIdempotencyKey(ctx context.Context, tenantID uuid.UUID, key string) (*Payment, error)
	UpdateStatus(ctx context.Context, tenantID, id uuid.UUID, status PaymentStatus, providerPaymentID string, rawResponse []byte) error
}

// PaymentGateway abstracts over Stripe/Adyen themselves. Unlike the
// *Repository interfaces above, it isn't backed by Postgres — it's
// implemented per-provider in internal/platform/payment. It lives here in
// domain so service code depends on this interface rather than a concrete
// Stripe or Adyen SDK type ("accept interfaces, return structs" applies
// to third-party integrations too, not just our own repositories).
type PaymentGateway interface {
	// CreateIntent starts a payment for the given amount and returns the
	// provider's payment/intent ID plus any client-side data (e.g. a
	// client secret) the frontend needs to complete payment.
	CreateIntent(ctx context.Context, amount Money, idempotencyKey string, metadata map[string]string) (providerPaymentID, clientSecret string, err error)

	// VerifyWebhookSignature validates that a webhook payload genuinely
	// came from the provider before any of its contents are trusted.
	VerifyWebhookSignature(payload []byte, signatureHeader string) error
}
