package domain

import (
	"context"
	"time"

	"github.com/google/uuid"
)

type EventStatus string

const (
	EventStatusDraft     EventStatus = "draft"
	EventStatusScheduled EventStatus = "scheduled"
	EventStatusOnSale    EventStatus = "on_sale"
	EventStatusSoldOut   EventStatus = "sold_out"
	EventStatusCompleted EventStatus = "completed"
	EventStatusCancelled EventStatus = "cancelled"
)

// SeatCategory is a reusable price-tier label within a tenant, e.g. "VIP",
// "Category 1", "Terraces". It intentionally carries no price — see
// EventTicketCategory for why.
type SeatCategory struct {
	ID        uuid.UUID
	TenantID  uuid.UUID
	Name      string
	Color     string // hex color for seat-map UI rendering
	CreatedAt time.Time
	UpdatedAt time.Time
}

type Event struct {
	ID           uuid.UUID
	TenantID     uuid.UUID
	VenueID      uuid.UUID
	Name         string
	Competition  string
	HomeTeam     string
	AwayTeam     string
	StartsAt     time.Time
	DoorsOpenAt  *time.Time
	SalesStartAt time.Time
	SalesEndAt   time.Time
	Status       EventStatus
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// IsOnSale reports whether the event should currently accept purchase
// attempts, considering both its stored status and its sales window.
// Callers should use this rather than comparing timestamps themselves, so
// the "is this event actually on sale right now" rule lives in exactly
// one place.
func (e *Event) IsOnSale(now time.Time) bool {
	return e.Status == EventStatusOnSale && !now.Before(e.SalesStartAt) && now.Before(e.SalesEndAt)
}

// EventTicketCategory is the price and per-order limit for one
// SeatCategory within one specific Event. Price lives here, not on
// SeatCategory, because the same "VIP" category prices very differently
// for a relegation six-pointer than for a pre-season friendly.
type EventTicketCategory struct {
	ID             uuid.UUID
	TenantID       uuid.UUID
	EventID        uuid.UUID
	SeatCategoryID uuid.UUID
	Price          Money
	MaxPerOrder    int
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// EventFilter narrows EventRepository.List. All fields besides TenantID
// are optional; nil/zero means "don't filter on this".
type EventFilter struct {
	TenantID uuid.UUID
	Status   *EventStatus
	VenueID  *uuid.UUID
	// From/To narrow by StartsAt, e.g. for an "upcoming matches" listing.
	From, To *time.Time
}

type EventRepository interface {
	Create(ctx context.Context, e *Event) error
	GetByID(ctx context.Context, tenantID, id uuid.UUID) (*Event, error)
	Update(ctx context.Context, e *Event) error
	List(ctx context.Context, filter EventFilter, limit, offset int) ([]*Event, error)

	CreateTicketCategory(ctx context.Context, etc *EventTicketCategory) error
	ListTicketCategories(ctx context.Context, tenantID, eventID uuid.UUID) ([]*EventTicketCategory, error)
}
