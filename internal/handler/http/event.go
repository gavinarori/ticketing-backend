package http

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/gavinarori/ticketing-backend/internal/domain"
	appmw "github.com/gavinarori/ticketing-backend/internal/handler/middleware"
	"github.com/gavinarori/ticketing-backend/internal/pkg/response"
)

// EventHandler serves fan-facing event browsing. Unlike admin handlers,
// these are public (no RequireAuth) — any fan, or nobody logged in at
// all, can browse a club's on-sale events — but they still need
// RequireTenantHeader to know which club's events to show, since a fan
// account isn't tied to one tenant.
type EventHandler struct {
	events    domain.EventRepository
	inventory domain.InventoryRepository
	log       *zap.Logger
}

func NewEventHandler(events domain.EventRepository, inventory domain.InventoryRepository, log *zap.Logger) *EventHandler {
	return &EventHandler{events: events, inventory: inventory, log: log}
}

// List handles GET /events — on-sale events only, deliberately: this is
// the public browse surface, not an admin listing, so draft/cancelled
// events never appear here regardless of who's asking.
func (h *EventHandler) List(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := appmw.ResolvedTenantIDFromContext(r.Context())
	if !ok {
		response.Error(w, http.StatusBadRequest, "missing-tenant", "X-Tenant-ID header is required")
		return
	}

	onSale := domain.EventStatusOnSale
	events, err := h.events.List(r.Context(), domain.EventFilter{TenantID: tenantID, Status: &onSale}, 50, 0)
	if err != nil {
		h.log.Error("list_events_failed", zap.Error(err))
		response.Error(w, http.StatusInternalServerError, "internal-error", "could not list events")
		return
	}

	out := make([]map[string]any, 0, len(events))
	for _, e := range events {
		out = append(out, eventResponse(e))
	}
	response.JSON(w, http.StatusOK, out)
}

// Get handles GET /events/{id} — event detail plus its ticket categories
// and live availability counts, everything a fan's "pick your ticket
// type" screen needs in one call.
func (h *EventHandler) Get(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := appmw.ResolvedTenantIDFromContext(r.Context())
	if !ok {
		response.Error(w, http.StatusBadRequest, "missing-tenant", "X-Tenant-ID header is required")
		return
	}
	eventID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		response.Error(w, http.StatusBadRequest, "invalid-event-id", "event id must be a valid UUID")
		return
	}

	event, err := h.events.GetByID(r.Context(), tenantID, eventID)
	if err != nil {
		response.Error(w, http.StatusNotFound, "event-not-found", "event not found")
		return
	}

	categories, err := h.events.ListTicketCategories(r.Context(), tenantID, eventID)
	if err != nil {
		h.log.Error("list_ticket_categories_failed", zap.Error(err))
		response.Error(w, http.StatusInternalServerError, "internal-error", "could not load ticket categories")
		return
	}
	counts, err := h.inventory.CountByStatus(r.Context(), tenantID, eventID)
	if err != nil {
		h.log.Error("count_inventory_failed", zap.Error(err))
		response.Error(w, http.StatusInternalServerError, "internal-error", "could not load availability")
		return
	}

	categoriesOut := make([]map[string]any, 0, len(categories))
	for _, c := range categories {
		available := counts[c.ID].Available
		categoriesOut = append(categoriesOut, map[string]any{
			"id": c.ID, "seat_category_id": c.SeatCategoryID,
			"price_cents": c.Price.Cents, "currency": c.Price.Currency,
			"max_per_order": c.MaxPerOrder, "available": available,
		})
	}

	resp := eventResponse(event)
	resp["ticket_categories"] = categoriesOut
	response.JSON(w, http.StatusOK, resp)
}
