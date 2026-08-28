package payment

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/gavinarori/ticketing-backend/internal/domain"
)

func TestStripeGateway_CreateIntent(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/payment_intents" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer sk_test_123" {
			t.Errorf("unexpected Authorization header: %q", got)
		}
		if got := r.Header.Get("Idempotency-Key"); got != "idem-key-1" {
			t.Errorf("unexpected Idempotency-Key header: %q", got)
		}
		if err := r.ParseForm(); err != nil {
			t.Fatalf("parse form: %v", err)
		}
		if got := r.PostFormValue("amount"); got != "250000" {
			t.Errorf("expected amount=250000, got %q", got)
		}
		if got := r.PostFormValue("currency"); got != "kes" {
			t.Errorf("expected currency=kes (lowercased), got %q", got)
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{
			"id":            "pi_test_abc123",
			"client_secret": "pi_test_abc123_secret_xyz",
		})
	}))
	defer server.Close()

	g := NewStripeGateway("sk_test_123", "whsec_test")
	g.baseURL = server.URL

	amount, _ := domain.NewMoney(250000, "KES")
	providerPaymentID, clientSecret, err := g.CreateIntent(context.Background(), amount, "idem-key-1", map[string]string{"order_id": "abc"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if providerPaymentID != "pi_test_abc123" {
		t.Errorf("expected provider payment id 'pi_test_abc123', got %q", providerPaymentID)
	}
	if clientSecret != "pi_test_abc123_secret_xyz" {
		t.Errorf("expected client secret 'pi_test_abc123_secret_xyz', got %q", clientSecret)
	}
}

func TestStripeGateway_CreateIntent_HandlesDeclinedCard(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusPaymentRequired)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"error": map[string]string{"type": "card_error", "message": "Your card was declined."},
		})
	}))
	defer server.Close()

	g := NewStripeGateway("sk_test_123", "whsec_test")
	g.baseURL = server.URL

	amount, _ := domain.NewMoney(1000, "KES")
	if _, _, err := g.CreateIntent(context.Background(), amount, "idem-key-2", nil); err == nil {
		t.Fatal("expected an error from a declined card, got nil")
	}
}

func TestStripeGateway_Refund(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/refunds" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		if err := r.ParseForm(); err != nil {
			t.Fatalf("parse form: %v", err)
		}
		if got := r.PostFormValue("payment_intent"); got != "pi_test_abc123" {
			t.Errorf("expected payment_intent=pi_test_abc123, got %q", got)
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"id": "re_test_123"})
	}))
	defer server.Close()

	g := NewStripeGateway("sk_test_123", "whsec_test")
	g.baseURL = server.URL

	amount, _ := domain.NewMoney(250000, "KES")
	refundID, err := g.Refund(context.Background(), "pi_test_abc123", amount, "hold expired")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if refundID != "re_test_123" {
		t.Errorf("expected refund id 're_test_123', got %q", refundID)
	}
}

// signStripePayload replicates Stripe's own signing algorithm
// (https://stripe.com/docs/webhooks/signatures), independent of the
// gateway's VerifyWebhookSignature implementation, so these tests prove
// the two sides of the algorithm agree rather than testing one function
// against itself.
func signStripePayload(secret, payload string, timestamp int64) string {
	signedPayload := strconv.FormatInt(timestamp, 10) + "." + payload
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(signedPayload))
	sig := hex.EncodeToString(mac.Sum(nil))
	return "t=" + strconv.FormatInt(timestamp, 10) + ",v1=" + sig
}

func TestStripeGateway_VerifyWebhookSignature_Valid(t *testing.T) {
	secret := "whsec_test_secret"
	payload := `{"type":"payment_intent.succeeded"}`
	header := signStripePayload(secret, payload, time.Now().Unix())

	g := NewStripeGateway("sk_test", secret)
	if err := g.VerifyWebhookSignature([]byte(payload), header); err != nil {
		t.Errorf("expected valid signature to verify, got error: %v", err)
	}
}

func TestStripeGateway_VerifyWebhookSignature_WrongSecret(t *testing.T) {
	payload := `{"type":"payment_intent.succeeded"}`
	header := signStripePayload("whsec_correct", payload, time.Now().Unix())

	g := NewStripeGateway("sk_test", "whsec_wrong")
	if err := g.VerifyWebhookSignature([]byte(payload), header); err == nil {
		t.Error("expected signature verification to fail with the wrong secret")
	}
}

func TestStripeGateway_VerifyWebhookSignature_TamperedPayload(t *testing.T) {
	secret := "whsec_test_secret"
	header := signStripePayload(secret, `{"amount":100}`, time.Now().Unix())

	g := NewStripeGateway("sk_test", secret)
	if err := g.VerifyWebhookSignature([]byte(`{"amount":999999}`), header); err == nil {
		t.Error("expected signature verification to fail for a tampered payload")
	}
}

func TestStripeGateway_VerifyWebhookSignature_MalformedHeader(t *testing.T) {
	g := NewStripeGateway("sk_test", "whsec_test")
	if err := g.VerifyWebhookSignature([]byte("{}"), "not-a-valid-header"); err == nil {
		t.Error("expected an error for a malformed Stripe-Signature header")
	}
}
