package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gavinarori/ticketing-backend/internal/domain"
)

type VenueRepo struct {
	pool *pgxpool.Pool
}

func NewVenueRepo(pool *pgxpool.Pool) *VenueRepo { return &VenueRepo{pool: pool} }

var _ domain.VenueRepository = (*VenueRepo)(nil)

const venueColumns = `id, tenant_id, name, address, city, country, timezone, capacity, created_at, updated_at`

func scanVenue(row pgx.Row) (*domain.Venue, error) {
	var v domain.Venue
	if err := row.Scan(&v.ID, &v.TenantID, &v.Name, &v.Address, &v.City, &v.Country, &v.Timezone, &v.Capacity, &v.CreatedAt, &v.UpdatedAt); err != nil {
		return nil, err
	}
	return &v, nil
}

func (r *VenueRepo) CreateVenue(ctx context.Context, v *domain.Venue) error {
	if v.ID == uuid.Nil {
		v.ID = uuid.New()
	}
	if v.Timezone == "" {
		v.Timezone = "Africa/Nairobi"
	}
	_, err := db(ctx, r.pool).Exec(ctx,
		`INSERT INTO venues (id, tenant_id, name, address, city, country, timezone, capacity) VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`,
		v.ID, v.TenantID, v.Name, v.Address, v.City, v.Country, v.Timezone, v.Capacity,
	)
	if err != nil {
		return fmt.Errorf("postgres: create venue: %w", err)
	}
	return nil
}

func (r *VenueRepo) GetVenue(ctx context.Context, tenantID, id uuid.UUID) (*domain.Venue, error) {
	row := db(ctx, r.pool).QueryRow(ctx, `SELECT `+venueColumns+` FROM venues WHERE id = $1 AND tenant_id = $2`, id, tenantID)
	v, err := scanVenue(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, domain.NewError("postgres.VenueRepo.GetVenue", domain.ErrNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("postgres: get venue: %w", err)
	}
	return v, nil
}

func (r *VenueRepo) ListVenues(ctx context.Context, tenantID uuid.UUID, limit, offset int) ([]*domain.Venue, error) {
	rows, err := db(ctx, r.pool).Query(ctx,
		`SELECT `+venueColumns+` FROM venues WHERE tenant_id = $1 ORDER BY name LIMIT $2 OFFSET $3`, tenantID, limit, offset)
	if err != nil {
		return nil, fmt.Errorf("postgres: list venues: %w", err)
	}
	defer rows.Close()

	var out []*domain.Venue
	for rows.Next() {
		v, err := scanVenue(rows)
		if err != nil {
			return nil, fmt.Errorf("postgres: scan venue: %w", err)
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

func (r *VenueRepo) UpdateVenue(ctx context.Context, v *domain.Venue) error {
	tag, err := db(ctx, r.pool).Exec(ctx,
		`UPDATE venues SET name=$1, address=$2, city=$3, country=$4, timezone=$5, capacity=$6 WHERE id=$7 AND tenant_id=$8`,
		v.Name, v.Address, v.City, v.Country, v.Timezone, v.Capacity, v.ID, v.TenantID,
	)
	if err != nil {
		return fmt.Errorf("postgres: update venue: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return domain.NewError("postgres.VenueRepo.UpdateVenue", domain.ErrNotFound)
	}
	return nil
}

func (r *VenueRepo) CreateSection(ctx context.Context, s *domain.VenueSection) error {
	if s.ID == uuid.Nil {
		s.ID = uuid.New()
	}
	if s.SeatingType == "" {
		s.SeatingType = domain.SeatingTypeReserved
	}
	_, err := db(ctx, r.pool).Exec(ctx,
		`INSERT INTO venue_sections (id, tenant_id, venue_id, name, code, seating_type, capacity) VALUES ($1,$2,$3,$4,$5,$6,$7)`,
		s.ID, s.TenantID, s.VenueID, s.Name, s.Code, string(s.SeatingType), s.Capacity,
	)
	if err != nil {
		if pgErrorCode(err) == pgCodeUniqueViolation {
			return domain.NewError("postgres.VenueRepo.CreateSection", domain.ErrConflict)
		}
		return fmt.Errorf("postgres: create section: %w", err)
	}
	return nil
}

func (r *VenueRepo) ListSectionsByVenue(ctx context.Context, tenantID, venueID uuid.UUID) ([]*domain.VenueSection, error) {
	rows, err := db(ctx, r.pool).Query(ctx,
		`SELECT id, tenant_id, venue_id, name, code, seating_type, capacity, created_at, updated_at
		 FROM venue_sections WHERE tenant_id = $1 AND venue_id = $2 ORDER BY name`, tenantID, venueID)
	if err != nil {
		return nil, fmt.Errorf("postgres: list sections: %w", err)
	}
	defer rows.Close()

	var out []*domain.VenueSection
	for rows.Next() {
		var s domain.VenueSection
		var seatingType string
		if err := rows.Scan(&s.ID, &s.TenantID, &s.VenueID, &s.Name, &s.Code, &seatingType, &s.Capacity, &s.CreatedAt, &s.UpdatedAt); err != nil {
			return nil, fmt.Errorf("postgres: scan section: %w", err)
		}
		s.SeatingType = domain.SeatingType(seatingType)
		out = append(out, &s)
	}
	return out, rows.Err()
}

func (r *VenueRepo) CreateSeatsBulk(ctx context.Context, seats []*domain.Seat) error {
	if len(seats) == 0 {
		return nil
	}
	batch := &pgx.Batch{}
	for _, s := range seats {
		id := s.ID
		if id == uuid.Nil {
			id = uuid.New()
		}
		batch.Queue(
			`INSERT INTO seats (id, tenant_id, section_id, row_label, seat_number, is_accessible, pos_x, pos_y) VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`,
			id, s.TenantID, s.SectionID, s.RowLabel, s.SeatNumber, s.IsAccessible, s.PosX, s.PosY,
		)
	}
	br := db(ctx, r.pool).SendBatch(ctx, batch)
	defer br.Close()
	for range seats {
		if _, err := br.Exec(); err != nil {
			return fmt.Errorf("postgres: bulk create seats: %w", err)
		}
	}
	return nil
}

func (r *VenueRepo) ListSeatsBySection(ctx context.Context, tenantID, sectionID uuid.UUID) ([]*domain.Seat, error) {
	rows, err := db(ctx, r.pool).Query(ctx,
		`SELECT id, tenant_id, section_id, row_label, seat_number, is_accessible, pos_x, pos_y, created_at
		 FROM seats WHERE tenant_id = $1 AND section_id = $2 ORDER BY row_label, seat_number`, tenantID, sectionID)
	if err != nil {
		return nil, fmt.Errorf("postgres: list seats: %w", err)
	}
	defer rows.Close()

	var out []*domain.Seat
	for rows.Next() {
		var s domain.Seat
		if err := rows.Scan(&s.ID, &s.TenantID, &s.SectionID, &s.RowLabel, &s.SeatNumber, &s.IsAccessible, &s.PosX, &s.PosY, &s.CreatedAt); err != nil {
			return nil, fmt.Errorf("postgres: scan seat: %w", err)
		}
		out = append(out, &s)
	}
	return out, rows.Err()
}
