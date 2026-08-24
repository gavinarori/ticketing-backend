package domain

import (
	"context"
	"time"

	"github.com/google/uuid"
)

type TenantStatus string

const (
	TenantStatusActive    TenantStatus = "active"
	TenantStatusSuspended TenantStatus = "suspended"
	TenantStatusInactive  TenantStatus = "inactive"
)

// Tenant is a club or league using the platform — the root of
// multi-tenancy (see migrations/000002_tenants.up.sql). Every other
// tenant-scoped entity in this package embeds a TenantID field rather
// than a pointer back to Tenant, so entities stay independently loadable
// without forcing a join just to know which club a row belongs to.
type Tenant struct {
	ID           uuid.UUID
	Slug         string
	Name         string
	Sport        string
	Status       TenantStatus
	ContactEmail string
	Branding     map[string]any
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// TenantRepository is implemented by internal/repository/postgres.
// Defined here, in domain, so service code depends on this interface
// rather than a concrete Postgres type — "accept interfaces, return
// structs".
type TenantRepository interface {
	Create(ctx context.Context, t *Tenant) error
	GetByID(ctx context.Context, id uuid.UUID) (*Tenant, error)
	GetBySlug(ctx context.Context, slug string) (*Tenant, error)
	Update(ctx context.Context, t *Tenant) error
	List(ctx context.Context, status TenantStatus, limit, offset int) ([]*Tenant, error)
}
