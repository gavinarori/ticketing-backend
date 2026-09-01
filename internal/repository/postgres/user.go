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

type UserRepo struct {
	pool *pgxpool.Pool
}

func NewUserRepo(pool *pgxpool.Pool) *UserRepo { return &UserRepo{pool: pool} }

var _ domain.UserRepository = (*UserRepo)(nil)

const userColumns = `id, email, phone, password_hash, first_name, last_name, status, role, tenant_id, email_verified_at, created_at, updated_at`

func scanUser(row pgx.Row) (*domain.User, error) {
	var u domain.User
	var status, role string
	if err := row.Scan(
		&u.ID, &u.Email, &u.Phone, &u.PasswordHash, &u.FirstName, &u.LastName,
		&status, &role, &u.TenantID, &u.EmailVerifiedAt, &u.CreatedAt, &u.UpdatedAt,
	); err != nil {
		return nil, err
	}
	u.Status = domain.UserStatus(status)
	u.Role = domain.UserRole(role)
	return &u, nil
}

func (r *UserRepo) Create(ctx context.Context, u *domain.User) error {
	if u.ID == uuid.Nil {
		u.ID = uuid.New()
	}
	_, err := db(ctx, r.pool).Exec(ctx,
		`INSERT INTO users (id, email, phone, password_hash, first_name, last_name, status, role, tenant_id)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
		u.ID, u.Email, u.Phone, u.PasswordHash, u.FirstName, u.LastName, string(u.Status), string(u.Role), u.TenantID,
	)
	if err != nil {
		if pgErrorCode(err) == pgCodeUniqueViolation {
			return domain.NewError("postgres.UserRepo.Create", domain.ErrConflict)
		}
		return fmt.Errorf("postgres: create user: %w", err)
	}
	return nil
}

func (r *UserRepo) GetByID(ctx context.Context, id uuid.UUID) (*domain.User, error) {
	row := db(ctx, r.pool).QueryRow(ctx, `SELECT `+userColumns+` FROM users WHERE id = $1`, id)
	u, err := scanUser(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, domain.NewError("postgres.UserRepo.GetByID", domain.ErrNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("postgres: get user: %w", err)
	}
	return u, nil
}

// GetByEmail relies on the users.email column being CITEXT (see
// migrations/000001_extensions_and_helpers.up.sql) for case-insensitive
// matching at the database level — this query does not lower() its
// input, and doesn't need to.
func (r *UserRepo) GetByEmail(ctx context.Context, email string) (*domain.User, error) {
	row := db(ctx, r.pool).QueryRow(ctx, `SELECT `+userColumns+` FROM users WHERE email = $1`, email)
	u, err := scanUser(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, domain.NewError("postgres.UserRepo.GetByEmail", domain.ErrNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("postgres: get user by email: %w", err)
	}
	return u, nil
}

func (r *UserRepo) Update(ctx context.Context, u *domain.User) error {
	tag, err := db(ctx, r.pool).Exec(ctx,
		`UPDATE users SET email=$1, phone=$2, password_hash=$3, first_name=$4, last_name=$5, status=$6, email_verified_at=$7
		 WHERE id = $8`,
		u.Email, u.Phone, u.PasswordHash, u.FirstName, u.LastName, string(u.Status), u.EmailVerifiedAt, u.ID,
	)
	if err != nil {
		if pgErrorCode(err) == pgCodeUniqueViolation {
			return domain.NewError("postgres.UserRepo.Update", domain.ErrConflict)
		}
		return fmt.Errorf("postgres: update user: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return domain.NewError("postgres.UserRepo.Update", domain.ErrNotFound)
	}
	return nil
}

type RefreshTokenRepo struct {
	pool *pgxpool.Pool
}

func NewRefreshTokenRepo(pool *pgxpool.Pool) *RefreshTokenRepo { return &RefreshTokenRepo{pool: pool} }

var _ domain.RefreshTokenRepository = (*RefreshTokenRepo)(nil)

func (r *RefreshTokenRepo) Create(ctx context.Context, rt *domain.RefreshToken) error {
	if rt.ID == uuid.Nil {
		rt.ID = uuid.New()
	}
	_, err := db(ctx, r.pool).Exec(ctx,
		`INSERT INTO refresh_tokens (id, user_id, token_hash, user_agent, ip_address, expires_at)
		 VALUES ($1, $2, $3, $4, $5::inet, $6)`,
		rt.ID, rt.UserID, rt.TokenHash, rt.UserAgent, nullIfEmpty(rt.IPAddress), rt.ExpiresAt,
	)
	if err != nil {
		if pgErrorCode(err) == pgCodeUniqueViolation {
			return domain.NewError("postgres.RefreshTokenRepo.Create", domain.ErrConflict)
		}
		return fmt.Errorf("postgres: create refresh token: %w", err)
	}
	return nil
}

func (r *RefreshTokenRepo) GetByTokenHash(ctx context.Context, tokenHash string) (*domain.RefreshToken, error) {
	row := db(ctx, r.pool).QueryRow(ctx,
		`SELECT id, user_id, token_hash, user_agent, COALESCE(host(ip_address), ''), expires_at, revoked_at, created_at
		 FROM refresh_tokens WHERE token_hash = $1`,
		tokenHash,
	)
	var rt domain.RefreshToken
	err := row.Scan(&rt.ID, &rt.UserID, &rt.TokenHash, &rt.UserAgent, &rt.IPAddress, &rt.ExpiresAt, &rt.RevokedAt, &rt.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, domain.NewError("postgres.RefreshTokenRepo.GetByTokenHash", domain.ErrNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("postgres: get refresh token: %w", err)
	}
	return &rt, nil
}

func (r *RefreshTokenRepo) Revoke(ctx context.Context, id uuid.UUID) error {
	tag, err := db(ctx, r.pool).Exec(ctx, `UPDATE refresh_tokens SET revoked_at = now() WHERE id = $1 AND revoked_at IS NULL`, id)
	if err != nil {
		return fmt.Errorf("postgres: revoke refresh token: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return domain.NewError("postgres.RefreshTokenRepo.Revoke", domain.ErrNotFound)
	}
	return nil
}

func (r *RefreshTokenRepo) RevokeAllForUser(ctx context.Context, userID uuid.UUID) error {
	_, err := db(ctx, r.pool).Exec(ctx, `UPDATE refresh_tokens SET revoked_at = now() WHERE user_id = $1 AND revoked_at IS NULL`, userID)
	if err != nil {
		return fmt.Errorf("postgres: revoke all refresh tokens for user: %w", err)
	}
	return nil
}

func nullIfEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
