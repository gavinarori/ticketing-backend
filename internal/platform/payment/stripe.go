package payment

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/gavinarori/ticketing-backend/internal/domain"
)

// StripeGateway implements domain.PaymentGateway against Stripe's REST
// API directly over net/http, rather than pulling in the stripe-go SDK.
// This codebase only needs three Stripe operations — create a
// PaymentIntent, refund one, verify a webhook signature — and a small,
// directly-auditable HTTP client covers that without adding a large SDK
// dependency to go.mod. Revisit this trade-off (switch to stripe-go) if
// the payment surface grows significantly (subscriptions, disputes,
// Connect, etc.) — hand-rolling the client stops paying off well before
// that point.
type StripeGateway struct {
	secretKey     string
	webhookSecret string
	httpClient    *http.Client
	baseURL       string // overridable in tests; defaults to https://api.stripe.com
}

func NewStripeGateway(secretKey, webhookSecret string) *StripeGateway {
	return &StripeGateway{
		secretKey:     secretKey,
		webhookSecret: webhookSecret,
		httpClient:    &http.Client{Timeout: 15 * time.Second},
		baseURL:       "https://api.stripe.com",
	}
}

var _ domain.PaymentGateway = (*StripeGateway)(nil)

type stripeErrorBody struct {
	Message string `json:"message"`
	Type    string `json:"type"`
}

// stripeResponse is implemented by every response type do() decodes into,
// so a Stripe-reported error (e.g. a declined card, a bad parameter)
// surfaces as a Go error instead of a silently empty/zero-value result.
type stripeResponse interface {
	stripeError() *stripeErrorBody
}

type stripePaymentIntentResponse struct {
	ID           string           `json:"id"`
	ClientSecret string           `json:"client_secret"`
	Error        *stripeErrorBody `json:"error"`
}

func (r *stripePaymentIntentResponse) stripeError() *stripeErrorBody { return r.Error }

type stripeRefundResponse struct {
	ID    string           `json:"id"`
	Error *stripeErrorBody `json:"error"`
}

func (r *stripeRefundResponse) stripeError() *stripeErrorBody { return r.Error }

// CreateIntent creates a Stripe PaymentIntent for amount, using
// idempotencyKey as Stripe's own Idempotency-Key header. Stripe
// deduplicates on that header server-side, so a retried request (e.g.
// after a client timeout on our end) can never create two intents for
// what was really one attempt.
func (g *StripeGateway) CreateIntent(ctx context.Context, amount domain.Money, idempotencyKey string, metadata map[string]string) (providerPaymentID, clientSecret string, err error) {
	form := url.Values{}
	form.Set("amount", strconv.FormatInt(amount.Cents, 10))
	form.Set("currency", strings.ToLower(amount.Currency))
	form.Set("automatic_payment_methods[enabled]", "true")
	for k, v := range metadata {
		form.Set("metadata["+k+"]", v)
	}

	parsed := &stripePaymentIntentResponse{}
	if err := g.do(ctx, "/v1/payment_intents", form, idempotencyKey, parsed); err != nil {
		return "", "", err
	}
	return parsed.ID, parsed.ClientSecret, nil
}

// Refund reverses a previously captured PaymentIntent. See
// domain.PaymentGateway.Refund for why internal/service/order needs this:
// payment can succeed while the seat it was for has, in the meantime,
// become unconfirmable.
func (g *StripeGateway) Refund(ctx context.Context, providerPaymentID string, amount domain.Money, reason string) (string, error) {
	form := url.Values{}
	form.Set("payment_intent", providerPaymentID)
	form.Set("amount", strconv.FormatInt(amount.Cents, 10))
	if reason != "" {
		form.Set("metadata[reason]", reason)
	}

	parsed := &stripeRefundResponse{}
	if err := g.do(ctx, "/v1/refunds", form, "", parsed); err != nil {
		return "", err
	}
	return parsed.ID, nil
}

// do posts form to Stripe's API and decodes the JSON response into out.
func (g *StripeGateway) do(ctx context.Context, path string, form url.Values, idempotencyKey string, out stripeResponse) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, g.baseURL+path, bytes.NewBufferString(form.Encode()))
	if err != nil {
		return fmt.Errorf("payment: build stripe request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Authorization", "Bearer "+g.secretKey)
	if idempotencyKey != "" {
		req.Header.Set("Idempotency-Key", idempotencyKey)
	}

	resp, err := g.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("payment: stripe request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("payment: read stripe response: %w", err)
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("payment: parse stripe response: %w", err)
	}
	if stripeErr := out.stripeError(); stripeErr != nil {
		return fmt.Errorf("payment: stripe error (%s): %s", stripeErr.Type, stripeErr.Message)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("payment: stripe returned status %d", resp.StatusCode)
	}
	return nil
}

// VerifyWebhookSignature implements Stripe's documented signature scheme
// (https://stripe.com/docs/webhooks/signatures): the Stripe-Signature
// header carries a timestamp and one or more v1 HMAC-SHA256 signatures
// computed over "{timestamp}.{payload}" with the webhook signing secret.
// Comparison uses hmac.Equal (constant-time) rather than ==, since this
// is exactly the kind of comparison timing attacks target.
func (g *StripeGateway) VerifyWebhookSignature(payload []byte, signatureHeader string) error {
	timestamp, signatures, err := parseStripeSignatureHeader(signatureHeader)
	if err != nil {
		return err
	}

	signedPayload := timestamp + "." + string(payload)
	mac := hmac.New(sha256.New, []byte(g.webhookSecret))
	mac.Write([]byte(signedPayload))
	expected := hex.EncodeToString(mac.Sum(nil))

	for _, sig := range signatures {
		if hmac.Equal([]byte(sig), []byte(expected)) {
			return nil
		}
	}
	return errors.New("payment: stripe webhook signature mismatch")
}

func parseStripeSignatureHeader(header string) (timestamp string, signatures []string, err error) {
	for _, part := range strings.Split(header, ",") {
		kv := strings.SplitN(part, "=", 2)
		if len(kv) != 2 {
			continue
		}
		switch kv[0] {
		case "t":
			timestamp = kv[1]
		case "v1":
			signatures = append(signatures, kv[1])
		}
	}
	if timestamp == "" || len(signatures) == 0 {
		return "", nil, errors.New("payment: malformed Stripe-Signature header")
	}
	return timestamp, signatures, nil
}
