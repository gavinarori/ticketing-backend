package domain

import (
	"context"
	"time"

	"github.com/google/uuid"
)

type MembershipStatus string

const (
	MembershipStatusActive    MembershipStatus = "active"
	MembershipStatusExpired   MembershipStatus = "expired"
	MembershipStatusCancelled MembershipStatus = "cancelled"
)

// Membership represents a user's tenant-specific standing — season
// ticket holder, loyalty tier, etc. — for one season. See
// migrations/000009_memberships.up.sql: (tenant_id, user_id, season) is
// unique, so a user has at most one membership per club per season.
type Membership struct {
	ID        uuid.UUID
	TenantID  uuid.UUID
	UserID    uuid.UUID
	Tier      string
	Season    string
	Status    MembershipStatus
	StartsAt  time.Time
	EndsAt    time.Time
	CreatedAt time.Time
	UpdatedAt time.Time
}

type MembershipRepository interface {
	Create(ctx context.Context, m *Membership) error
	GetActive(ctx context.Context, tenantID, userID uuid.UUID) (*Membership, error)
	ListByUser(ctx context.Context, tenantID, userID uuid.UUID) ([]*Membership, error)
}
