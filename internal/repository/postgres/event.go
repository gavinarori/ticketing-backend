package postgres

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gavinarori/ticketing-backend/internal/domain"
)

type EventRepo struct {
	pool *pgxpool.Pool
}

func NewEventRepo(pool *pgxpool.Pool) *EventRepo { return &EventRepo{pool: pool} }

var _ domain.EventRepository = (*EventRepo)(nil)

const eventColumns = `id, tenant_id, venue_id, name, competition, home_team, away_team, starts_at, doors_open_at, sales_start_at, sales_end_at, status, created_at, updated_at`

func scanEvent(row pgx.Row) (*domain.Event, error) {
	var e domain.Event
	var status string
	if err := row.Scan(
		&e.ID, &e.TenantID, &e.VenueID, &e.Name, &e.Competition, &e.HomeTeam, &e.AwayTeam,
		&e.StartsAt, &e.DoorsOpenAt, &e.SalesStartAt, &e.SalesEndAt, &status, &e.CreatedAt, &e.UpdatedAt,
	); err != nil {
		return nil, err
	}
	e.Status = domain.EventStatus(status)
	return &e, nil
}

func (r *EventRepo) Create(ctx context.Context, e *domain.Event) error {
	if e.ID == uuid.Nil {
		e.ID = uuid.New()
	}
	_, err := db(ctx, r.pool).Exec(ctx,
		`INSERT INTO events (id, tenant_id, venue_id, name, competition, home_team, away_team, starts_at, doors_open_at, sales_start_at, sales_end_at, status)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)`,
		e.ID, e.TenantID, e.VenueID, e.Name, e.Competition, e.HomeTeam, e.AwayTeam,
		e.StartsAt, e.DoorsOpenAt, e.SalesStartAt, e.SalesEndAt, string(e.Status),
	)
	if err != nil {
		return fmt.Errorf("postgres: create event: %w", err)
	}
	return nil
}

func (r *EventRepo) GetByID(ctx context.Context, tenantID, id uuid.UUID) (*domain.Event, error) {
	row := db(ctx, r.pool).QueryRow(ctx, `SELECT `+eventColumns+` FROM events WHERE id = $1 AND tenant_id = $2`, id, tenantID)
	e, err := scanEvent(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, domain.NewError("postgres.EventRepo.GetByID", domain.ErrNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("postgres: get event: %w", err)
	}
	return e, nil
}

func (r *EventRepo) Update(ctx context.Context, e *domain.Event) error {
	tag, err := db(ctx, r.pool).Exec(ctx,
		`UPDATE events
		 SET name=$1, competition=$2, home_team=$3, away_team=$4, starts_at=$5, doors_open_at=$6, sales_start_at=$7, sales_end_at=$8, status=$9
		 WHERE id = $10 AND tenant_id = $11`,
		e.Name, e.Competition, e.HomeTeam, e.AwayTeam, e.StartsAt, e.DoorsOpenAt, e.SalesStartAt, e.SalesEndAt, string(e.Status), e.ID, e.TenantID,
	)
	if err != nil {
		return fmt.Errorf("postgres: update event: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return domain.NewError("postgres.EventRepo.Update", domain.ErrNotFound)
	}
	return nil
}

// List builds its WHERE clause dynamically from the optional EventFilter
// fields — tenant_id is always required, everything else is appended only
// when set, so a filter with nothing but TenantID produces a plain
// "WHERE tenant_id = $1" rather than a query full of "AND x = NULL"
// no-ops.
func (r *EventRepo) List(ctx context.Context, filter domain.EventFilter, limit, offset int) ([]*domain.Event, error) {
	var sb strings.Builder
	sb.WriteString(`SELECT ` + eventColumns + ` FROM events WHERE tenant_id = $1`)
	args := []any{filter.TenantID}

	if filter.Status != nil {
		args = append(args, string(*filter.Status))
		fmt.Fprintf(&sb, " AND status = $%d", len(args))
	}
	if filter.VenueID != nil {
		args = append(args, *filter.VenueID)
		fmt.Fprintf(&sb, " AND venue_id = $%d", len(args))
	}
	if filter.From != nil {
		args = append(args, *filter.From)
		fmt.Fprintf(&sb, " AND starts_at >= $%d", len(args))
	}
	if filter.To != nil {
		args = append(args, *filter.To)
		fmt.Fprintf(&sb, " AND starts_at < $%d", len(args))
	}

	args = append(args, limit)
	fmt.Fprintf(&sb, " ORDER BY starts_at LIMIT $%d", len(args))
	args = append(args, offset)
	fmt.Fprintf(&sb, " OFFSET $%d", len(args))

	rows, err := db(ctx, r.pool).Query(ctx, sb.String(), args...)
	if err != nil {
		return nil, fmt.Errorf("postgres: list events: %w", err)
	}
	defer rows.Close()

	var out []*domain.Event
	for rows.Next() {
		e, err := scanEvent(rows)
		if err != nil {
			return nil, fmt.Errorf("postgres: scan event: %w", err)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func (r *EventRepo) CreateTicketCategory(ctx context.Context, etc *domain.EventTicketCategory) error {
	if etc.ID == uuid.Nil {
		etc.ID = uuid.New()
	}
	// Mirrors the table's DEFAULT 6 for callers that leave MaxPerOrder
	// unset: we specify the column explicitly below (rather than omitting
	// it and relying on the DB default), so a zero-value MaxPerOrder would
	// otherwise violate the CHECK (max_per_order > 0) constraint instead
	// of quietly falling back to the sensible default.
	if etc.MaxPerOrder <= 0 {
		etc.MaxPerOrder = 6
	}
	_, err := db(ctx, r.pool).Exec(ctx,
		`INSERT INTO event_ticket_categories (id, tenant_id, event_id, seat_category_id, price_cents, currency, max_per_order)
		 VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		etc.ID, etc.TenantID, etc.EventID, etc.SeatCategoryID, etc.Price.Cents, etc.Price.Currency, etc.MaxPerOrder,
	)
	if err != nil {
		if pgErrorCode(err) == pgCodeUniqueViolation {
			return domain.NewError("postgres.EventRepo.CreateTicketCategory", domain.ErrConflict)
		}
		return fmt.Errorf("postgres: create event ticket category: %w", err)
	}
	return nil
}

func (r *EventRepo) ListTicketCategories(ctx context.Context, tenantID, eventID uuid.UUID) ([]*domain.EventTicketCategory, error) {
	rows, err := db(ctx, r.pool).Query(ctx,
		`SELECT id, tenant_id, event_id, seat_category_id, price_cents, currency, max_per_order, created_at, updated_at
		 FROM event_ticket_categories WHERE tenant_id = $1 AND event_id = $2`,
		tenantID, eventID,
	)
	if err != nil {
		return nil, fmt.Errorf("postgres: list ticket categories: %w", err)
	}
	defer rows.Close()

	var out []*domain.EventTicketCategory
	for rows.Next() {
		var etc domain.EventTicketCategory
		var cents int64
		var currency string
		if err := rows.Scan(&etc.ID, &etc.TenantID, &etc.EventID, &etc.SeatCategoryID, &cents, &currency, &etc.MaxPerOrder, &etc.CreatedAt, &etc.UpdatedAt); err != nil {
			return nil, fmt.Errorf("postgres: scan ticket category: %w", err)
		}
		etc.Price = domain.Money{Cents: cents, Currency: currency}
		out = append(out, &etc)
	}
	return out, rows.Err()
}
