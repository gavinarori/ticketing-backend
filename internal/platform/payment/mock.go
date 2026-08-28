// Package payment implements domain.PaymentGateway per provider.
package payment

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"

	"github.com/google/uuid"

	"github.com/gavinarori/ticketing-backend/internal/domain"
)

// MockGateway is an in-memory domain.PaymentGateway for local development
// and automated tests, where hitting a real Stripe/Adyen sandbox isn't
// practical or desirable (no real card, no network dependency in CI).
// It implements exactly the interface real providers do, so
// internal/service/order needs zero changes to swap between this and
// StripeGateway — that's the entire point of PaymentGateway being a
// domain interface rather than a concrete Stripe type.
type MockGateway struct {
	mu      sync.Mutex
	secret  string
	intents map[string]bool // providerPaymentID -> exists, so Refund can reject an unknown ID
}

func NewMockGateway(webhookSecret string) *MockGateway {
	return &MockGateway{secret: webhookSecret, intents: map[string]bool{}}
}

var _ domain.PaymentGateway = (*MockGateway)(nil)

func (g *MockGateway) CreateIntent(ctx context.Context, amount domain.Money, idempotencyKey string, metadata map[string]string) (providerPaymentID, clientSecret string, err error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	id := "pi_mock_" + uuid.NewString()
	g.intents[id] = true
	return id, id + "_secret_" + uuid.NewString(), nil
}

func (g *MockGateway) Refund(ctx context.Context, providerPaymentID string, amount domain.Money, reason string) (string, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if !g.intents[providerPaymentID] {
		return "", fmt.Errorf("payment: mock gateway: unknown payment intent %q", providerPaymentID)
	}
	return "re_mock_" + uuid.NewString(), nil
}

// VerifyWebhookSignature uses a simple HMAC-SHA256-over-the-raw-payload
// scheme — deliberately simpler than Stripe's own timestamped scheme
// (see StripeGateway), since this gateway exists for tests/dev, not to
// exercise webhook-replay protection.
func (g *MockGateway) VerifyWebhookSignature(payload []byte, signatureHeader string) error {
	mac := hmac.New(sha256.New, []byte(g.secret))
	mac.Write(payload)
	expected := hex.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(expected), []byte(signatureHeader)) {
		return errors.New("payment: mock gateway: invalid webhook signature")
	}
	return nil
}
