package payment

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"testing"

	"github.com/gavinarori/ticketing-backend/internal/domain"
)

// computeHMACHex replicates MockGateway.VerifyWebhookSignature's own
// algorithm independently, so the test proves round-trip correctness
// rather than just calling the same code twice.
func computeHMACHex(secret string, payload []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(payload)
	return hex.EncodeToString(mac.Sum(nil))
}

func TestMockGateway_CreateIntent(t *testing.T) {
	g := NewMockGateway("test-secret")
	amount, _ := domain.NewMoney(1000, "KES")

	id, secret, err := g.CreateIntent(context.Background(), amount, "idem-1", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if id == "" || secret == "" {
		t.Error("expected non-empty provider payment id and client secret")
	}
}

func TestMockGateway_Refund_UnknownIntent(t *testing.T) {
	g := NewMockGateway("test-secret")
	amount, _ := domain.NewMoney(100, "KES")
	if _, err := g.Refund(context.Background(), "pi_never_created", amount, "test"); err == nil {
		t.Error("expected an error refunding an intent that was never created")
	}
}

func TestMockGateway_Refund_KnownIntent(t *testing.T) {
	g := NewMockGateway("test-secret")
	amount, _ := domain.NewMoney(1000, "KES")

	id, _, err := g.CreateIntent(context.Background(), amount, "idem-2", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := g.Refund(context.Background(), id, amount, "test"); err != nil {
		t.Errorf("expected refund of a known intent to succeed, got %v", err)
	}
}

func TestMockGateway_VerifyWebhookSignature(t *testing.T) {
	secret := "shared-secret"
	g := NewMockGateway(secret)
	payload := []byte(`{"type":"payment_intent.succeeded"}`)

	validSig := computeHMACHex(secret, payload)
	if err := g.VerifyWebhookSignature(payload, validSig); err != nil {
		t.Errorf("expected a correctly computed signature to verify, got %v", err)
	}

	if err := g.VerifyWebhookSignature(payload, "not-a-real-signature"); err == nil {
		t.Error("expected an invalid signature to be rejected")
	}

	if err := g.VerifyWebhookSignature([]byte(`{"tampered":true}`), validSig); err == nil {
		t.Error("expected a signature computed for a different payload to be rejected")
	}
}
