package http

import (
	"encoding/json"
	"net/http"

	"github.com/go-playground/validator/v10"
	"go.uber.org/zap"

	"github.com/gavinarori/ticketing-backend/internal/domain"
	appmw "github.com/gavinarori/ticketing-backend/internal/handler/middleware"
	"github.com/gavinarori/ticketing-backend/internal/pkg/response"
)

// AdminVenueHandler manages venues for the authenticated admin's own
// tenant. Every method here gets its tenant from
// appmw.TenantIDFromContext (the admin's JWT claim) — never from a
// header or request body — so an admin token can only ever act on the
// one club it was issued for. See middleware/tenant.go's doc comment for
// why that's a hard security boundary, not a style choice.
type AdminVenueHandler struct {
	venues   domain.VenueRepository
	validate *validator.Validate
	log      *zap.Logger
}

func NewAdminVenueHandler(venues domain.VenueRepository, log *zap.Logger) *AdminVenueHandler {
	return &AdminVenueHandler{venues: venues, validate: validator.New(), log: log}
}

type createVenueRequest struct {
	Name     string `json:"name" validate:"required"`
	Address  string `json:"address"`
	City     string `json:"city"`
	Country  string `json:"country"`
	Timezone string `json:"timezone"`
	Capacity *int   `json:"capacity"`
}

func venueResponse(v *domain.Venue) map[string]any {
	return map[string]any{
		"id": v.ID, "name": v.Name, "address": v.Address, "city": v.City,
		"country": v.Country, "timezone": v.Timezone, "capacity": v.Capacity,
	}
}

func (h *AdminVenueHandler) Create(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := appmw.TenantIDFromContext(r.Context())
	if !ok {
		response.Error(w, http.StatusForbidden, "no-tenant", "admin account has no associated tenant")
		return
	}

	var req createVenueRequest
	if !decodeAndValidate(w, r, h.validate, &req) {
		return
	}

	venue := &domain.Venue{
		TenantID: tenantID, Name: req.Name, Address: req.Address,
		City: req.City, Country: req.Country, Timezone: req.Timezone, Capacity: req.Capacity,
	}
	if err := h.venues.CreateVenue(r.Context(), venue); err != nil {
		h.log.Error("create_venue_failed", zap.Error(err))
		response.Error(w, http.StatusInternalServerError, "internal-error", "could not create venue")
		return
	}

	response.JSON(w, http.StatusCreated, venueResponse(venue))
}

func (h *AdminVenueHandler) List(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := appmw.TenantIDFromContext(r.Context())
	if !ok {
		response.Error(w, http.StatusForbidden, "no-tenant", "admin account has no associated tenant")
		return
	}

	venues, err := h.venues.ListVenues(r.Context(), tenantID, 50, 0)
	if err != nil {
		h.log.Error("list_venues_failed", zap.Error(err))
		response.Error(w, http.StatusInternalServerError, "internal-error", "could not list venues")
		return
	}

	out := make([]map[string]any, 0, len(venues))
	for _, v := range venues {
		out = append(out, venueResponse(v))
	}
	response.JSON(w, http.StatusOK, out)
}

// decodeJSONBody is a tiny local helper for endpoints with no validation
// tags worth enforcing beyond "is this valid JSON" — most handlers use
// the shared decodeAndValidate from auth.go instead.
func decodeJSONBody(w http.ResponseWriter, r *http.Request, dst any) bool {
	if err := json.NewDecoder(r.Body).Decode(dst); err != nil {
		response.Error(w, http.StatusBadRequest, "invalid-body", "could not parse request body")
		return false
	}
	return true
}
