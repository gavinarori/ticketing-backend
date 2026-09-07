package http

import (
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/gavinarori/ticketing-backend/internal/domain"
	appmw "github.com/gavinarori/ticketing-backend/internal/handler/middleware"
	"github.com/gavinarori/ticketing-backend/internal/pkg/response"
	invsvc "github.com/gavinarori/ticketing-backend/internal/service/inventory"
)

// InventoryHandler exposes the seat-purchase hot path built in
// internal/service/inventory over HTTP: joining the waiting room,
// checking admission status, and holding/releasing a specific seat. Every
// method requires both an authenticated fan (RequireAuth) and a resolved
// tenant (RequireTenantHeader) — a hold is meaningless without knowing
// which fan and which club's inventory it's against.
type InventoryHandler struct {
	inventory *invsvc.Service
	log       *zap.Logger
}

func NewInventoryHandler(inventory *invsvc.Service, log *zap.Logger) *InventoryHandler {
	return &InventoryHandler{inventory: inventory, log: log}
}

func (h *InventoryHandler) JoinQueue(w http.ResponseWriter, r *http.Request) {
	userID, ok := appmw.UserIDFromContext(r.Context())
	if !ok {
		response.Error(w, http.StatusUnauthorized, "missing-token", "not authenticated")
		return
	}
	eventID, err := uuid.Parse(chi.URLParam(r, "eventID"))
	if err != nil {
		response.Error(w, http.StatusBadRequest, "invalid-event-id", "event id must be a valid UUID")
		return
	}

	position, err := h.inventory.JoinQueue(r.Context(), eventID, userID)
	if err != nil {
		h.log.Error("join_queue_failed", zap.Error(err))
		response.Error(w, http.StatusInternalServerError, "internal-error", "could not join queue")
		return
	}
	response.JSON(w, http.StatusOK, map[string]any{"position": position})
}

func (h *InventoryHandler) QueueStatus(w http.ResponseWriter, r *http.Request) {
	userID, ok := appmw.UserIDFromContext(r.Context())
	if !ok {
		response.Error(w, http.StatusUnauthorized, "missing-token", "not authenticated")
		return
	}
	eventID, err := uuid.Parse(chi.URLParam(r, "eventID"))
	if err != nil {
		response.Error(w, http.StatusBadRequest, "invalid-event-id", "event id must be a valid UUID")
		return
	}

	status, err := h.inventory.QueueStatus(r.Context(), eventID, userID)
	if err != nil {
		if errors.Is(err, invsvc.ErrNotQueued) {
			response.Error(w, http.StatusNotFound, "not-queued", "you have not joined the queue for this event")
			return
		}
		h.log.Error("queue_status_failed", zap.Error(err))
		response.Error(w, http.StatusInternalServerError, "internal-error", "could not check queue status")
		return
	}
	response.JSON(w, http.StatusOK, map[string]any{"admitted": status.Admitted, "position": status.Position})
}

type holdSeatRequest struct {
	EventID string `json:"event_id" validate:"required,uuid"`
}

// Hold handles POST /inventory/{id}/hold. See
// internal/service/inventory's HoldSeat for the full gating chain
// (waiting room -> rate limit -> Redis lock -> Postgres CAS) this thinly
// wraps; every error branch below maps one of that method's documented
// outcomes onto an HTTP status, nothing more.
func (h *InventoryHandler) Hold(w http.ResponseWriter, r *http.Request) {
	userID, tenantID, ok := userAndTenant(w, r)
	if !ok {
		return
	}
	inventoryID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		response.Error(w, http.StatusBadRequest, "invalid-inventory-id", "inventory id must be a valid UUID")
		return
	}
	var req holdSeatRequest
	if !decodeJSONBody(w, r, &req) {
		return
	}
	eventID, err := uuid.Parse(req.EventID)
	if err != nil {
		response.Error(w, http.StatusBadRequest, "invalid-event-id", "event_id must be a valid UUID")
		return
	}

	result, err := h.inventory.HoldSeat(r.Context(), tenantID, eventID, inventoryID, userID)
	if err != nil {
		switch {
		case errors.Is(err, invsvc.ErrNotAdmitted):
			response.Error(w, http.StatusForbidden, "not-admitted", "join the waiting room and wait to be admitted before holding a seat")
		case errors.Is(err, invsvc.ErrRateLimited):
			response.Error(w, http.StatusTooManyRequests, "rate-limited", "too many hold attempts, slow down")
		case errors.Is(err, invsvc.ErrLockContention), errors.Is(err, domain.ErrUnavailable):
			response.Error(w, http.StatusConflict, "unavailable", "this seat is no longer available — try another")
		default:
			h.log.Error("hold_seat_failed", zap.Error(err))
			response.Error(w, http.StatusInternalServerError, "internal-error", "could not hold seat")
		}
		return
	}

	response.JSON(w, http.StatusOK, map[string]any{
		"hold_token": result.HoldToken, "expires_at": result.ExpiresAt,
	})
}

type releaseHoldRequest struct {
	HoldToken string `json:"hold_token" validate:"required,uuid"`
}

func (h *InventoryHandler) Release(w http.ResponseWriter, r *http.Request) {
	_, tenantID, ok := userAndTenant(w, r)
	if !ok {
		return
	}
	inventoryID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		response.Error(w, http.StatusBadRequest, "invalid-inventory-id", "inventory id must be a valid UUID")
		return
	}
	var req releaseHoldRequest
	if !decodeJSONBody(w, r, &req) {
		return
	}
	holdToken, err := uuid.Parse(req.HoldToken)
	if err != nil {
		response.Error(w, http.StatusBadRequest, "invalid-hold-token", "hold_token must be a valid UUID")
		return
	}

	if err := h.inventory.ReleaseHold(r.Context(), tenantID, inventoryID, holdToken); err != nil {
		if errors.Is(err, domain.ErrExpired) {
			response.Error(w, http.StatusConflict, "already-expired", "this hold has already expired or was already released")
			return
		}
		h.log.Error("release_hold_failed", zap.Error(err))
		response.Error(w, http.StatusInternalServerError, "internal-error", "could not release hold")
		return
	}
	response.JSON(w, http.StatusOK, map[string]string{"status": "released"})
}

// userAndTenant is the common precondition for every fan-facing purchase
// endpoint: an authenticated user AND a resolved tenant. Shared here
// (used by inventory.go and order.go) since every one of these handlers
// needs exactly this pair of checks first.
func userAndTenant(w http.ResponseWriter, r *http.Request) (userID, tenantID uuid.UUID, ok bool) {
	userID, ok = appmw.UserIDFromContext(r.Context())
	if !ok {
		response.Error(w, http.StatusUnauthorized, "missing-token", "not authenticated")
		return uuid.Nil, uuid.Nil, false
	}
	tenantID, ok = appmw.ResolvedTenantIDFromContext(r.Context())
	if !ok {
		response.Error(w, http.StatusBadRequest, "missing-tenant", "X-Tenant-ID header is required")
		return uuid.Nil, uuid.Nil, false
	}
	return userID, tenantID, true
}
