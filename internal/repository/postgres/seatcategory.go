package postgres

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gavinarori/ticketing-backend/internal/domain"
)

type SeatCategoryRepo struct {
	pool *pgxpool.Pool
}

func NewSeatCategoryRepo(pool *pgxpool.Pool) *SeatCategoryRepo { return &SeatCategoryRepo{pool: pool} }

var _ domain.SeatCategoryRepository = (*SeatCategoryRepo)(nil)

func (r *SeatCategoryRepo) Create(ctx context.Context, sc *domain.SeatCategory) error {
	if sc.ID == uuid.Nil {
		sc.ID = uuid.New()
	}
	_, err := db(ctx, r.pool).Exec(ctx,
		`INSERT INTO seat_categories (id, tenant_id, name, color) VALUES ($1, $2, $3, $4)`,
		sc.ID, sc.TenantID, sc.Name, sc.Color,
	)
	if err != nil {
		if pgErrorCode(err) == pgCodeUniqueViolation {
			return domain.NewError("postgres.SeatCategoryRepo.Create", domain.ErrConflict)
		}
		return fmt.Errorf("postgres: create seat category: %w", err)
	}
	return nil
}

func (r *SeatCategoryRepo) ListByTenant(ctx context.Context, tenantID uuid.UUID) ([]*domain.SeatCategory, error) {
	rows, err := db(ctx, r.pool).Query(ctx,
		`SELECT id, tenant_id, name, color, created_at, updated_at FROM seat_categories WHERE tenant_id = $1 ORDER BY name`,
		tenantID,
	)
	if err != nil {
		return nil, fmt.Errorf("postgres: list seat categories: %w", err)
	}
	defer rows.Close()

	var out []*domain.SeatCategory
	for rows.Next() {
		var sc domain.SeatCategory
		if err := rows.Scan(&sc.ID, &sc.TenantID, &sc.Name, &sc.Color, &sc.CreatedAt, &sc.UpdatedAt); err != nil {
			return nil, fmt.Errorf("postgres: scan seat category: %w", err)
		}
		out = append(out, &sc)
	}
	return out, rows.Err()
}
