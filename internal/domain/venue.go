package domain

import (
	"context"
	"time"

	"github.com/google/uuid"
)

type SeatingType string

const (
	SeatingTypeReserved         SeatingType = "reserved"
	SeatingTypeGeneralAdmission SeatingType = "general_admission"
)

type Venue struct {
	ID        uuid.UUID
	TenantID  uuid.UUID
	Name      string
	Address   string
	City      string
	Country   string
	Timezone  string
	Capacity  *int
	CreatedAt time.Time
	UpdatedAt time.Time
}

type VenueSection struct {
	ID          uuid.UUID
	TenantID    uuid.UUID
	VenueID     uuid.UUID
	Name        string
	Code        string
	SeatingType SeatingType
	Capacity    int
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

type Seat struct {
	ID           uuid.UUID
	TenantID     uuid.UUID
	SectionID    uuid.UUID
	RowLabel     string
	SeatNumber   string
	IsAccessible bool
	// Optional coordinates for a visual seat-map renderer on the
	// frontend; nil when not yet plotted.
	PosX, PosY *float64
	CreatedAt  time.Time
}

// VenueRepository covers venues, their sections, and seats as one unit.
// Splitting these into three separate repositories would force every
// caller that needs "give me a venue with its seat map" to orchestrate
// three calls against three interfaces for what is, from the business's
// perspective, a single aggregate — a venue's physical layout rarely
// changes independently of the venue record itself.
type VenueRepository interface {
	CreateVenue(ctx context.Context, v *Venue) error
	GetVenue(ctx context.Context, tenantID, id uuid.UUID) (*Venue, error)
	ListVenues(ctx context.Context, tenantID uuid.UUID, limit, offset int) ([]*Venue, error)
	UpdateVenue(ctx context.Context, v *Venue) error

	CreateSection(ctx context.Context, s *VenueSection) error
	ListSectionsByVenue(ctx context.Context, tenantID, venueID uuid.UUID) ([]*VenueSection, error)

	// CreateSeatsBulk inserts many seats in one call — a single section
	// can have thousands of seats, and inserting them one row at a time
	// during venue setup would be needlessly slow.
	CreateSeatsBulk(ctx context.Context, seats []*Seat) error
	ListSeatsBySection(ctx context.Context, tenantID, sectionID uuid.UUID) ([]*Seat, error)
}
