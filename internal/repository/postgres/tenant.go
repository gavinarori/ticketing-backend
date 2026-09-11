package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gavinarori/ticketing-backend/internal/domain"
)

type TenantRepo struct {
	pool *pgxpool.Pool
}

func NewTenantRepo(pool *pgxpool.Pool) *TenantRepo { return &TenantRepo{pool: pool} }

var _ domain.TenantRepository = (*TenantRepo)(nil)

const tenantColumns = `id, slug, name, sport, status, contact_email, branding, created_at, updated_at`

func scanTenant(row pgx.Row) (*domain.Tenant, error) {
	var t domain.Tenant
	var status string
	var contactEmail *string
	var branding []byte
	if err := row.Scan(&t.ID, &t.Slug, &t.Name, &t.Sport, &status, &contactEmail, &branding, &t.CreatedAt, &t.UpdatedAt); err != nil {
		return nil, err
	}
	t.Status = domain.TenantStatus(status)
	if contactEmail != nil {
		t.ContactEmail = *contactEmail
	}
	if len(branding) > 0 {
		if err := json.Unmarshal(branding, &t.Branding); err != nil {
			return nil, fmt.Errorf("unmarshal branding: %w", err)
		}
	}
	return &t, nil
}

func (r *TenantRepo) Create(ctx context.Context, t *domain.Tenant) error {
	if t.ID == uuid.Nil {
		t.ID = uuid.New()
	}
	if t.Status == "" {
		t.Status = domain.TenantStatusActive
	}
	if t.Sport == "" {
		t.Sport = "football"
	}
	branding := t.Branding
	if branding == nil {
		branding = map[string]any{}
	}
	brandingJSON, err := json.Marshal(branding)
	if err != nil {
		return fmt.Errorf("marshal branding: %w", err)
	}

	_, err = db(ctx, r.pool).Exec(ctx,
		`INSERT INTO tenants (id, slug, name, sport, status, contact_email, branding) VALUES ($1,$2,$3,$4,$5,$6,$7)`,
		t.ID, t.Slug, t.Name, t.Sport, string(t.Status), nullIfEmpty(t.ContactEmail), brandingJSON,
	)
	if err != nil {
		if pgErrorCode(err) == pgCodeUniqueViolation {
			return domain.NewError("postgres.TenantRepo.Create", domain.ErrConflict)
		}
		return fmt.Errorf("postgres: create tenant: %w", err)
	}
	return nil
}

func (r *TenantRepo) GetByID(ctx context.Context, id uuid.UUID) (*domain.Tenant, error) {
	row := db(ctx, r.pool).QueryRow(ctx, `SELECT `+tenantColumns+` FROM tenants WHERE id = $1`, id)
	t, err := scanTenant(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, domain.NewError("postgres.TenantRepo.GetByID", domain.ErrNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("postgres: get tenant: %w", err)
	}
	return t, nil
}

func (r *TenantRepo) GetBySlug(ctx context.Context, slug string) (*domain.Tenant, error) {
	row := db(ctx, r.pool).QueryRow(ctx, `SELECT `+tenantColumns+` FROM tenants WHERE slug = $1`, slug)
	t, err := scanTenant(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, domain.NewError("postgres.TenantRepo.GetBySlug", domain.ErrNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("postgres: get tenant by slug: %w", err)
	}
	return t, nil
}

func (r *TenantRepo) Update(ctx context.Context, t *domain.Tenant) error {
	brandingJSON, err := json.Marshal(t.Branding)
	if err != nil {
		return fmt.Errorf("marshal branding: %w", err)
	}
	tag, err := db(ctx, r.pool).Exec(ctx,
		`UPDATE tenants SET name=$1, sport=$2, status=$3, contact_email=$4, branding=$5 WHERE id=$6`,
		t.Name, t.Sport, string(t.Status), nullIfEmpty(t.ContactEmail), brandingJSON, t.ID,
	)
	if err != nil {
		return fmt.Errorf("postgres: update tenant: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return domain.NewError("postgres.TenantRepo.Update", domain.ErrNotFound)
	}
	return nil
}

// List is the query the admission worker uses to discover which tenants
// to check for on-sale events with waiting fans — see
// internal/service/admission. Filtering by status here (rather than the
// worker filtering client-side) keeps a suspended tenant's events out of
// admission processing without the worker needing to know why.
func (r *TenantRepo) List(ctx context.Context, status domain.TenantStatus, limit, offset int) ([]*domain.Tenant, error) {
	rows, err := db(ctx, r.pool).Query(ctx,
		`SELECT `+tenantColumns+` FROM tenants WHERE status = $1 ORDER BY name LIMIT $2 OFFSET $3`,
		string(status), limit, offset,
	)
	if err != nil {
		return nil, fmt.Errorf("postgres: list tenants: %w", err)
	}
	defer rows.Close()

	var out []*domain.Tenant
	for rows.Next() {
		t, err := scanTenant(rows)
		if err != nil {
			return nil, fmt.Errorf("postgres: scan tenant: %w", err)
		}
		out = append(out, t)
	}
	return out, rows.Err()
}
