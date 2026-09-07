package http

import (
	"errors"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-playground/validator/v10"
	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/gavinarori/ticketing-backend/internal/domain"
	appmw "github.com/gavinarori/ticketing-backend/internal/handler/middleware"
	"github.com/gavinarori/ticketing-backend/internal/pkg/response"
)

type AdminEventHandler struct {
	events         domain.EventRepository
	seatCategories domain.SeatCategoryRepository
	inventory      domain.InventoryRepository
	validate       *validator.Validate
	log            *zap.Logger
}

func NewAdminEventHandler(events domain.EventRepository, seatCategories domain.SeatCategoryRepository, inventory domain.InventoryRepository, log *zap.Logger) *AdminEventHandler {
	return &AdminEventHandler{events: events, seatCategories: seatCategories, inventory: inventory, validate: validator.New(), log: log}
}

type createSeatCategoryRequest struct {
	Name  string `json:"name" validate:"required"`
	Color string `json:"color"`
}

func (h *AdminEventHandler) CreateSeatCategory(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := appmw.TenantIDFromContext(r.Context())
	if !ok {
		response.Error(w, http.StatusForbidden, "no-tenant", "admin account has no associated tenant")
		return
	}
	var req createSeatCategoryRequest
	if !decodeAndValidate(w, r, h.validate, &req) {
		return
	}

	sc := &domain.SeatCategory{TenantID: tenantID, Name: req.Name, Color: req.Color}
	if err := h.seatCategories.Create(r.Context(), sc); err != nil {
		if errors.Is(err, domain.ErrConflict) {
			response.Error(w, http.StatusConflict, "name-taken", "a seat category with this name already exists")
			return
		}
		h.log.Error("create_seat_category_failed", zap.Error(err))
		response.Error(w, http.StatusInternalServerError, "internal-error", "could not create seat category")
		return
	}

	response.JSON(w, http.StatusCreated, map[string]any{"id": sc.ID, "name": sc.Name, "color": sc.Color})
}

type createEventRequest struct {
	VenueID      string     `json:"venue_id" validate:"required,uuid"`
	Name         string     `json:"name" validate:"required"`
	Competition  string     `json:"competition"`
	HomeTeam     string     `json:"home_team"`
	AwayTeam     string     `json:"away_team"`
	StartsAt     time.Time  `json:"starts_at" validate:"required"`
	DoorsOpenAt  *time.Time `json:"doors_open_at"`
	SalesStartAt time.Time  `json:"sales_start_at" validate:"required"`
	SalesEndAt   time.Time  `json:"sales_end_at" validate:"required"`
}

func eventResponse(e *domain.Event) map[string]any {
	return map[string]any{
		"id": e.ID, "venue_id": e.VenueID, "name": e.Name, "competition": e.Competition,
		"home_team": e.HomeTeam, "away_team": e.AwayTeam, "starts_at": e.StartsAt,
		"sales_start_at": e.SalesStartAt, "sales_end_at": e.SalesEndAt, "status": e.Status,
	}
}

func (h *AdminEventHandler) CreateEvent(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := appmw.TenantIDFromContext(r.Context())
	if !ok {
		response.Error(w, http.StatusForbidden, "no-tenant", "admin account has no associated tenant")
		return
	}
	var req createEventRequest
	if !decodeAndValidate(w, r, h.validate, &req) {
		return
	}
	venueID, err := uuid.Parse(req.VenueID)
	if err != nil {
		response.Error(w, http.StatusBadRequest, "invalid-venue-id", "venue_id must be a valid UUID")
		return
	}

	event := &domain.Event{
		TenantID: tenantID, VenueID: venueID, Name: req.Name, Competition: req.Competition,
		HomeTeam: req.HomeTeam, AwayTeam: req.AwayTeam, StartsAt: req.StartsAt,
		DoorsOpenAt: req.DoorsOpenAt, SalesStartAt: req.SalesStartAt, SalesEndAt: req.SalesEndAt,
		Status: domain.EventStatusDraft,
	}
	if err := h.events.Create(r.Context(), event); err != nil {
		h.log.Error("create_event_failed", zap.Error(err))
		response.Error(w, http.StatusInternalServerError, "internal-error", "could not create event")
		return
	}

	response.JSON(w, http.StatusCreated, eventResponse(event))
}

type createTicketCategoryRequest struct {
	SeatCategoryID string `json:"seat_category_id" validate:"required,uuid"`
	PriceCents     int64  `json:"price_cents" validate:"required,gte=0"`
	Currency       string `json:"currency" validate:"required,len=3"`
	MaxPerOrder    int    `json:"max_per_order"`
}

func (h *AdminEventHandler) CreateTicketCategory(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := appmw.TenantIDFromContext(r.Context())
	if !ok {
		response.Error(w, http.StatusForbidden, "no-tenant", "admin account has no associated tenant")
		return
	}
	eventID, err := uuid.Parse(chi.URLParam(r, "eventID"))
	if err != nil {
		response.Error(w, http.StatusBadRequest, "invalid-event-id", "event id must be a valid UUID")
		return
	}

	var req createTicketCategoryRequest
	if !decodeAndValidate(w, r, h.validate, &req) {
		return
	}
	seatCategoryID, err := uuid.Parse(req.SeatCategoryID)
	if err != nil {
		response.Error(w, http.StatusBadRequest, "invalid-seat-category-id", "seat_category_id must be a valid UUID")
		return
	}

	price, err := domain.NewMoney(req.PriceCents, req.Currency)
	if err != nil {
		response.Error(w, http.StatusBadRequest, "invalid-price", err.Error())
		return
	}

	etc := &domain.EventTicketCategory{
		TenantID: tenantID, EventID: eventID, SeatCategoryID: seatCategoryID,
		Price: price, MaxPerOrder: req.MaxPerOrder,
	}
	if err := h.events.CreateTicketCategory(r.Context(), etc); err != nil {
		h.log.Error("create_ticket_category_failed", zap.Error(err))
		response.Error(w, http.StatusInternalServerError, "internal-error", "could not create ticket category")
		return
	}

	response.JSON(w, http.StatusCreated, map[string]any{
		"id": etc.ID, "event_id": etc.EventID, "seat_category_id": etc.SeatCategoryID,
		"price_cents": etc.Price.Cents, "currency": etc.Price.Currency, "max_per_order": etc.MaxPerOrder,
	})
}

type publishEventRequest struct {
	EventTicketCategoryID string `json:"event_ticket_category_id" validate:"required,uuid"`
	Quantity              int    `json:"quantity" validate:"required,gt=0,lte=100000"`
}

// PublishEvent generates sellable general-admission inventory for one
// ticket category and (on the first successful publish) moves the event
// to 'on_sale'.
//
// Scope note, stated plainly: this is GA-only. The schema has no
// seat-to-seat_category mapping (venue_sections doesn't reference
// seat_category_id), so there's no way to derive "which physical seats
// belong to the VIP category" for reserved-seating publish. That's a
// real, known gap, not an oversight papered over — reserved-seat publish
// needs a schema addition this round doesn't make. See
// docs/fan-and-admin-api.md.
func (h *AdminEventHandler) PublishEvent(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := appmw.TenantIDFromContext(r.Context())
	if !ok {
		response.Error(w, http.StatusForbidden, "no-tenant", "admin account has no associated tenant")
		return
	}
	eventID, err := uuid.Parse(chi.URLParam(r, "eventID"))
	if err != nil {
		response.Error(w, http.StatusBadRequest, "invalid-event-id", "event id must be a valid UUID")
		return
	}

	var req publishEventRequest
	if !decodeAndValidate(w, r, h.validate, &req) {
		return
	}
	etcID, err := uuid.Parse(req.EventTicketCategoryID)
	if err != nil {
		response.Error(w, http.StatusBadRequest, "invalid-ticket-category-id", "event_ticket_category_id must be a valid UUID")
		return
	}

	event, err := h.events.GetByID(r.Context(), tenantID, eventID)
	if err != nil {
		response.Error(w, http.StatusNotFound, "event-not-found", "event not found")
		return
	}

	rows := make([]*domain.EventSeatInventory, 0, req.Quantity)
	for i := 0; i < req.Quantity; i++ {
		rows = append(rows, &domain.EventSeatInventory{
			ID: uuid.New(), TenantID: tenantID, EventID: eventID, EventTicketCategoryID: etcID,
			Status: domain.InventoryStatusAvailable,
		})
	}
	if err := h.inventory.BulkCreate(r.Context(), rows); err != nil {
		h.log.Error("publish_event_bulk_create_failed", zap.Error(err))
		response.Error(w, http.StatusInternalServerError, "internal-error", "could not generate inventory")
		return
	}

	if event.Status == domain.EventStatusDraft || event.Status == domain.EventStatusScheduled {
		event.Status = domain.EventStatusOnSale
		if err := h.events.Update(r.Context(), event); err != nil {
			h.log.Error("publish_event_status_update_failed", zap.Error(err))
			response.Error(w, http.StatusInternalServerError, "internal-error", "inventory created but event status update failed")
			return
		}
	}

	response.JSON(w, http.StatusCreated, map[string]any{"generated": len(rows), "event_status": event.Status})
}
