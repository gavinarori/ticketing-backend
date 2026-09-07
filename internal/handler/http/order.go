package http

import (
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/gavinarori/ticketing-backend/internal/domain"
	"github.com/gavinarori/ticketing-backend/internal/pkg/response"
	ordersvc "github.com/gavinarori/ticketing-backend/internal/service/order"
)

type OrderHandler struct {
	orders domain.OrderRepository
	svc    *ordersvc.Service
	log    *zap.Logger
}

func NewOrderHandler(orders domain.OrderRepository, svc *ordersvc.Service, log *zap.Logger) *OrderHandler {
	return &OrderHandler{orders: orders, svc: svc, log: log}
}

type createOrderItemRequest struct {
	InventoryID string `json:"inventory_id" validate:"required,uuid"`
	HoldToken   string `json:"hold_token" validate:"required,uuid"`
}

type createOrderRequest struct {
	IdempotencyKey string                   `json:"idempotency_key" validate:"required"`
	Items          []createOrderItemRequest `json:"items" validate:"required,min=1,dive"`
}

func orderResponse(o *domain.Order) map[string]any {
	return map[string]any{
		"id": o.ID, "status": o.Status, "subtotal_cents": o.Subtotal.Cents,
		"fees_cents": o.Fees.Cents, "total_cents": o.Total.Cents, "currency": o.Total.Currency,
		"expires_at": o.ExpiresAt, "created_at": o.CreatedAt,
	}
}

// Create handles POST /orders — converts a set of active seat holds into
// an order. See internal/service/order's package doc for why this does
// NOT confirm any sale or charge anything yet; that's AuthorizePayment
// and the webhook, deliberately separate steps.
func (h *OrderHandler) Create(w http.ResponseWriter, r *http.Request) {
	userID, tenantID, ok := userAndTenant(w, r)
	if !ok {
		return
	}

	var req createOrderRequest
	if !decodeJSONBody(w, r, &req) {
		return
	}
	if req.IdempotencyKey == "" || len(req.Items) == 0 {
		response.Error(w, http.StatusBadRequest, "invalid-request", "idempotency_key and at least one item are required")
		return
	}

	items := make([]ordersvc.HeldItem, 0, len(req.Items))
	for _, it := range req.Items {
		invID, err := uuid.Parse(it.InventoryID)
		if err != nil {
			response.Error(w, http.StatusBadRequest, "invalid-inventory-id", "each item's inventory_id must be a valid UUID")
			return
		}
		holdToken, err := uuid.Parse(it.HoldToken)
		if err != nil {
			response.Error(w, http.StatusBadRequest, "invalid-hold-token", "each item's hold_token must be a valid UUID")
			return
		}
		items = append(items, ordersvc.HeldItem{InventoryID: invID, HoldToken: holdToken})
	}

	order, err := h.svc.CreateOrder(r.Context(), tenantID, userID, req.IdempotencyKey, items)
	if err != nil {
		if errors.Is(err, domain.ErrExpired) {
			response.Error(w, http.StatusConflict, "hold-invalid", "one or more holds are no longer valid — they may have expired")
			return
		}
		if errors.Is(err, domain.ErrInvalidInput) {
			response.Error(w, http.StatusBadRequest, "invalid-order", err.Error())
			return
		}
		h.log.Error("create_order_failed", zap.Error(err))
		response.Error(w, http.StatusInternalServerError, "internal-error", "could not create order")
		return
	}

	response.JSON(w, http.StatusCreated, orderResponse(order))
}

// Authorize handles POST /orders/{id}/authorize — creates the payment
// intent and returns the client secret the frontend needs to actually
// collect payment (e.g. Stripe Elements). Confirmation happens later, via
// the webhook (see internal/handler/http/webhook.go), not this call.
func (h *OrderHandler) Authorize(w http.ResponseWriter, r *http.Request) {
	_, tenantID, ok := userAndTenant(w, r)
	if !ok {
		return
	}
	orderID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		response.Error(w, http.StatusBadRequest, "invalid-order-id", "order id must be a valid UUID")
		return
	}

	clientSecret, err := h.svc.AuthorizePayment(r.Context(), tenantID, orderID)
	if err != nil {
		if errors.Is(err, domain.ErrInvalidInput) {
			response.Error(w, http.StatusConflict, "wrong-order-status", "order is not in a payable state")
			return
		}
		h.log.Error("authorize_payment_failed", zap.Error(err))
		response.Error(w, http.StatusInternalServerError, "internal-error", "could not authorize payment")
		return
	}

	response.JSON(w, http.StatusOK, map[string]string{"client_secret": clientSecret})
}

func (h *OrderHandler) List(w http.ResponseWriter, r *http.Request) {
	userID, tenantID, ok := userAndTenant(w, r)
	if !ok {
		return
	}

	orders, err := h.orders.ListByUser(r.Context(), tenantID, userID, 50, 0)
	if err != nil {
		h.log.Error("list_orders_failed", zap.Error(err))
		response.Error(w, http.StatusInternalServerError, "internal-error", "could not list orders")
		return
	}

	out := make([]map[string]any, 0, len(orders))
	for _, o := range orders {
		out = append(out, orderResponse(o))
	}
	response.JSON(w, http.StatusOK, out)
}

func (h *OrderHandler) Get(w http.ResponseWriter, r *http.Request) {
	_, tenantID, ok := userAndTenant(w, r)
	if !ok {
		return
	}
	orderID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		response.Error(w, http.StatusBadRequest, "invalid-order-id", "order id must be a valid UUID")
		return
	}

	order, err := h.orders.GetByID(r.Context(), tenantID, orderID)
	if err != nil {
		response.Error(w, http.StatusNotFound, "order-not-found", "order not found")
		return
	}
	response.JSON(w, http.StatusOK, orderResponse(order))
}
