package http

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/gavinarori/ticketing-backend/internal/domain"
	"github.com/gavinarori/ticketing-backend/internal/pkg/response"
	ordersvc "github.com/gavinarori/ticketing-backend/internal/service/order"
)

// maxWebhookBodyBytes caps how much of the request body we'll read.
// Stripe payloads are small (a few KB); this is defense against a
// misdirected or malicious request, not a real-world limit.
const maxWebhookBodyBytes = 1 << 20 // 1MB

// WebhookHandler receives and processes payment-provider webhooks. It's
// deliberately mounted outside any JWT auth middleware — a webhook is
// authenticated by its signature (VerifyWebhookSignature), not a bearer
// token, since the caller is Stripe's servers, not a logged-in fan.
type WebhookHandler struct {
	gateway  domain.PaymentGateway
	orders   domain.OrderRepository
	payments domain.PaymentRepository
	orderSvc *ordersvc.Service
	log      *zap.Logger
}

func NewWebhookHandler(gateway domain.PaymentGateway, orders domain.OrderRepository, payments domain.PaymentRepository, orderSvc *ordersvc.Service, log *zap.Logger) *WebhookHandler {
	return &WebhookHandler{gateway: gateway, orders: orders, payments: payments, orderSvc: orderSvc, log: log}
}

// stripeWebhookEvent captures only the fields this handler needs from a
// Stripe event payload — id and metadata off the PaymentIntent object,
// and the event type. Everything else in Stripe's much larger payload is
// ignored rather than modeled.
type stripeWebhookEvent struct {
	Type string `json:"type"`
	Data struct {
		Object struct {
			ID       string            `json:"id"`
			Metadata map[string]string `json:"metadata"`
		} `json:"object"`
	} `json:"data"`
}

// Stripe handles POST /webhooks/stripe.
//
// Flow: verify the signature against the RAW body (must happen before any
// JSON parsing — Stripe signs the exact bytes sent, and re-encoding then
// re-verifying would not catch a payload that was tampered with in a way
// that survives a JSON round-trip). Then, for a successful capture, use
// the order_id/tenant_id this handler itself put into the PaymentIntent's
// metadata back in AuthorizePayment to look up the order, resolve the
// matching payment via its shared idempotency key, and hand off to
// order.Service.ConfirmPayment — the same method a direct test call
// exercises, so this handler adds no business logic of its own.
func (h *WebhookHandler) Stripe(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, maxWebhookBodyBytes))
	if err != nil {
		response.Error(w, http.StatusBadRequest, "invalid-body", "could not read request body")
		return
	}

	if err := h.gateway.VerifyWebhookSignature(body, r.Header.Get("Stripe-Signature")); err != nil {
		h.log.Warn("webhook_signature_invalid", zap.Error(err))
		response.Error(w, http.StatusBadRequest, "invalid-signature", "webhook signature verification failed")
		return
	}

	var evt stripeWebhookEvent
	if err := json.Unmarshal(body, &evt); err != nil {
		response.Error(w, http.StatusBadRequest, "invalid-payload", "could not parse webhook payload")
		return
	}

	// Only a successful capture triggers order fulfillment. Every other
	// event type (created, canceled, requires_action, ...) is
	// acknowledged with 200 so Stripe stops retrying it — there's nothing
	// for this handler to do with those yet.
	if evt.Type != "payment_intent.succeeded" {
		response.JSON(w, http.StatusOK, map[string]string{"status": "ignored"})
		return
	}

	tenantID, err := uuid.Parse(evt.Data.Object.Metadata["tenant_id"])
	if err != nil {
		response.Error(w, http.StatusBadRequest, "invalid-metadata", "missing or invalid tenant_id in payment intent metadata")
		return
	}
	orderID, err := uuid.Parse(evt.Data.Object.Metadata["order_id"])
	if err != nil {
		response.Error(w, http.StatusBadRequest, "invalid-metadata", "missing or invalid order_id in payment intent metadata")
		return
	}

	ctx := r.Context()

	order, err := h.orders.GetByID(ctx, tenantID, orderID)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			// Possible race: this webhook arrived before our own
			// AuthorizePayment write committed. 409 signals Stripe to
			// retry (Stripe retries non-2xx responses on a backoff)
			// rather than silently dropping a real event.
			response.Error(w, http.StatusConflict, "order-not-found-yet", "order not found, please retry")
			return
		}
		h.log.Error("webhook_lookup_order_failed", zap.Error(err))
		response.Error(w, http.StatusInternalServerError, "internal-error", "failed to look up order")
		return
	}

	// AuthorizePayment sets payment.IdempotencyKey = order.IdempotencyKey
	// (see internal/service/order), so this lookup — rather than needing
	// a dedicated GetByProviderPaymentID on domain.PaymentRepository —
	// finds the exact payment this order's checkout created.
	payment, err := h.payments.GetByIdempotencyKey(ctx, tenantID, order.IdempotencyKey)
	if err != nil {
		h.log.Error("webhook_lookup_payment_failed", zap.Error(err))
		response.Error(w, http.StatusInternalServerError, "internal-error", "failed to look up payment")
		return
	}

	if err := h.orderSvc.ConfirmPayment(ctx, tenantID, orderID, payment.ID); err != nil {
		if errors.Is(err, domain.ErrExpired) {
			// ConfirmPayment already refunded the payment and marked the
			// order 'expired' in this branch — that outcome is final and
			// correctly resolved, just not in the fan's favor. Stripe
			// doesn't need to retry an event we've already handled.
			h.log.Warn("webhook_confirm_payment_expired", zap.String("order_id", orderID.String()), zap.Error(err))
			response.JSON(w, http.StatusOK, map[string]string{"status": "refunded"})
			return
		}
		h.log.Error("webhook_confirm_payment_failed", zap.Error(err))
		response.Error(w, http.StatusInternalServerError, "internal-error", "failed to confirm payment")
		return
	}

	response.JSON(w, http.StatusOK, map[string]string{"status": "confirmed"})
}
