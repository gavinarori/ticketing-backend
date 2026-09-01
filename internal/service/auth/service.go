// Package auth implements account registration, login, and token
// lifecycle. Access tokens are short-lived signed JWTs (stateless —
// verified by signature alone, no DB round trip); refresh tokens are
// opaque random strings, persisted only as a hash, and rotated on every
// use so a stolen refresh token is only ever usable once before the
// legitimate client's next refresh invalidates it.
package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"

	"github.com/gavinarori/ticketing-backend/internal/config"
	"github.com/gavinarori/ticketing-backend/internal/domain"
)

// minPasswordLength is enforced at registration. This is a floor, not a
// full password-strength policy (no complexity rules) — deliberately
// simple for now; a real launch would want to check against a breached-
// password list (e.g. HaveIBeenPwned's range API) rather than inventing
// complexity rules that mostly just annoy users without improving
// security.
const minPasswordLength = 8

var (
	// ErrInvalidCredentials covers both "no such user" and "wrong
	// password" with one error — deliberately not distinguished, so a
	// login failure never tells an attacker whether an email address is
	// registered (a classic account-enumeration leak).
	ErrInvalidCredentials = errors.New("auth: invalid email or password")

	ErrWeakPassword    = fmt.Errorf("auth: password must be at least %d characters", minPasswordLength)
	ErrRefreshExpired  = errors.New("auth: refresh token expired")
	ErrRefreshRevoked  = errors.New("auth: refresh token has been revoked")
	ErrAccountInactive = errors.New("auth: account is not active")
)

// Claims is the payload of every access token this service issues.
type Claims struct {
	jwt.RegisteredClaims
	Role     string `json:"role"`
	TenantID string `json:"tenant_id,omitempty"`
}

type Service struct {
	users         domain.UserRepository
	refreshTokens domain.RefreshTokenRepository
	jwtCfg        config.JWTConfig
}

func NewService(users domain.UserRepository, refreshTokens domain.RefreshTokenRepository, jwtCfg config.JWTConfig) *Service {
	return &Service{users: users, refreshTokens: refreshTokens, jwtCfg: jwtCfg}
}

// TokenPair is what every successful login/refresh returns: a signed JWT
// for the Authorization header, and a raw refresh token for the client to
// store and later exchange. AccessTokenExpiresAt lets the client
// proactively refresh before the access token actually expires, instead
// of only reacting to a 401.
type TokenPair struct {
	AccessToken           string
	AccessTokenExpiresAt  time.Time
	RefreshToken          string
	RefreshTokenExpiresAt time.Time
}

// Register creates a new fan account (platform-wide, Role=fan,
// TenantID=nil — see domain.User). Admin account creation is a separate,
// privileged path (RegisterAdmin) — registration is never how an admin
// account is created, deliberately: a public endpoint that could mint
// admin accounts would be a critical vulnerability.
func (s *Service) Register(ctx context.Context, email, password, firstName, lastName string) (*domain.User, error) {
	email = normalizeEmail(email)
	if len(password) < minPasswordLength {
		return nil, ErrWeakPassword
	}

	hash, err := hashPassword(password)
	if err != nil {
		return nil, fmt.Errorf("auth: hash password: %w", err)
	}

	user := &domain.User{
		ID:           uuid.New(),
		Email:        email,
		PasswordHash: hash,
		FirstName:    firstName,
		LastName:     lastName,
		Status:       domain.UserStatusActive,
		Role:         domain.UserRoleFan,
	}
	if err := s.users.Create(ctx, user); err != nil {
		return nil, fmt.Errorf("auth: create user: %w", err)
	}
	user.PasswordHash = "" // never let the hash leave this function on the happy path either
	return user, nil
}

// RegisterAdmin creates a tenant-scoped club-staff account. Callers are
// responsible for authorization — this method itself performs none; it's
// intended to be reached only via an endpoint already gated by
// RequireAdmin (an existing admin creating a colleague) or a one-off
// bootstrap path (see internal/handler/http/auth.go's bootstrap handler)
// for a tenant's very first admin.
func (s *Service) RegisterAdmin(ctx context.Context, tenantID uuid.UUID, email, password, firstName, lastName string) (*domain.User, error) {
	email = normalizeEmail(email)
	if len(password) < minPasswordLength {
		return nil, ErrWeakPassword
	}

	hash, err := hashPassword(password)
	if err != nil {
		return nil, fmt.Errorf("auth: hash password: %w", err)
	}

	user := &domain.User{
		ID:           uuid.New(),
		Email:        email,
		PasswordHash: hash,
		FirstName:    firstName,
		LastName:     lastName,
		Status:       domain.UserStatusActive,
		Role:         domain.UserRoleAdmin,
		TenantID:     &tenantID,
	}
	if err := s.users.Create(ctx, user); err != nil {
		return nil, fmt.Errorf("auth: create admin user: %w", err)
	}
	user.PasswordHash = ""
	return user, nil
}

// Login verifies credentials and issues a fresh token pair. userAgent and
// ipAddress are stored alongside the refresh token purely for the
// account holder's own audit trail (e.g. a future "active sessions" UI) —
// never used to gate the login itself.
func (s *Service) Login(ctx context.Context, email, password, userAgent, ipAddress string) (*TokenPair, error) {
	user, err := s.users.GetByEmail(ctx, normalizeEmail(email))
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			// Run bcrypt anyway against a fixed dummy hash so a
			// nonexistent-account login takes roughly the same wall-clock
			// time as a wrong-password one — otherwise the response-time
			// difference itself leaks whether the email is registered.
			_ = bcrypt.CompareHashAndPassword([]byte(dummyHashForTiming), []byte(password))
			return nil, ErrInvalidCredentials
		}
		return nil, fmt.Errorf("auth: look up user: %w", err)
	}

	if user.Status != domain.UserStatusActive {
		return nil, ErrAccountInactive
	}

	if err := bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(password)); err != nil {
		return nil, ErrInvalidCredentials
	}

	return s.issueTokenPair(ctx, user, userAgent, ipAddress)
}

// RefreshAccessToken exchanges a valid, unexpired, unrevoked refresh
// token for a new token pair, and revokes the token that was presented —
// refresh tokens are single-use. This is "rotation": if a refresh token
// is ever stolen, the thief and the legitimate client are now racing to
// use it first, and whichever one loses gets a revoked-token error on
// their very next refresh attempt instead of a silently-still-working
// stolen credential.
func (s *Service) RefreshAccessToken(ctx context.Context, rawToken, userAgent, ipAddress string) (*TokenPair, error) {
	hash := hashToken(rawToken)
	stored, err := s.refreshTokens.GetByTokenHash(ctx, hash)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			return nil, ErrInvalidCredentials
		}
		return nil, fmt.Errorf("auth: look up refresh token: %w", err)
	}
	if stored.RevokedAt != nil {
		return nil, ErrRefreshRevoked
	}
	if stored.ExpiresAt.Before(time.Now()) {
		return nil, ErrRefreshExpired
	}

	user, err := s.users.GetByID(ctx, stored.UserID)
	if err != nil {
		return nil, fmt.Errorf("auth: look up user for refresh: %w", err)
	}
	if user.Status != domain.UserStatusActive {
		return nil, ErrAccountInactive
	}

	if err := s.refreshTokens.Revoke(ctx, stored.ID); err != nil {
		return nil, fmt.Errorf("auth: revoke used refresh token: %w", err)
	}

	return s.issueTokenPair(ctx, user, userAgent, ipAddress)
}

// Logout revokes a single refresh token (the one the client is holding),
// not every session — see RevokeAllForUser (not yet exposed via HTTP)
// for "log out everywhere".
func (s *Service) Logout(ctx context.Context, rawToken string) error {
	stored, err := s.refreshTokens.GetByTokenHash(ctx, hashToken(rawToken))
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			return nil // already gone; logout is idempotent from the caller's perspective
		}
		return fmt.Errorf("auth: look up refresh token: %w", err)
	}
	if stored.RevokedAt != nil {
		return nil
	}
	if err := s.refreshTokens.Revoke(ctx, stored.ID); err != nil {
		return fmt.Errorf("auth: revoke refresh token: %w", err)
	}
	return nil
}

// ParseAccessToken validates a JWT's signature and expiry and returns its
// claims. This is what internal/handler/middleware/auth.go calls on every
// authenticated request — deliberately no database lookup here, which is
// the entire point of using a signed token for the access-token layer
// instead of a session ID: verifying it costs nothing.
func (s *Service) ParseAccessToken(tokenString string) (*Claims, error) {
	claims := &Claims{}
	token, err := jwt.ParseWithClaims(tokenString, claims, func(t *jwt.Token) (interface{}, error) {
		return []byte(s.jwtCfg.Secret), nil
	}, jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Name}))
	if err != nil {
		return nil, fmt.Errorf("auth: parse access token: %w", err)
	}
	if !token.Valid {
		return nil, errors.New("auth: invalid access token")
	}
	return claims, nil
}

func (s *Service) issueTokenPair(ctx context.Context, user *domain.User, userAgent, ipAddress string) (*TokenPair, error) {
	now := time.Now()
	accessExpiresAt := now.Add(s.jwtCfg.AccessTTL)

	claims := &Claims{
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   user.ID.String(),
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(accessExpiresAt),
		},
		Role: string(user.Role),
	}
	if user.TenantID != nil {
		claims.TenantID = user.TenantID.String()
	}

	accessToken, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(s.jwtCfg.Secret))
	if err != nil {
		return nil, fmt.Errorf("auth: sign access token: %w", err)
	}

	rawRefresh, err := generateRefreshToken()
	if err != nil {
		return nil, fmt.Errorf("auth: generate refresh token: %w", err)
	}
	refreshExpiresAt := now.Add(s.jwtCfg.RefreshTTL)

	if err := s.refreshTokens.Create(ctx, &domain.RefreshToken{
		ID:        uuid.New(),
		UserID:    user.ID,
		TokenHash: hashToken(rawRefresh),
		UserAgent: userAgent,
		IPAddress: ipAddress,
		ExpiresAt: refreshExpiresAt,
	}); err != nil {
		return nil, fmt.Errorf("auth: store refresh token: %w", err)
	}

	return &TokenPair{
		AccessToken:           accessToken,
		AccessTokenExpiresAt:  accessExpiresAt,
		RefreshToken:          rawRefresh,
		RefreshTokenExpiresAt: refreshExpiresAt,
	}, nil
}

func normalizeEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}

func hashPassword(password string) (string, error) {
	// bcrypt.DefaultCost (10) balances hashing time against brute-force
	// resistance for a login-rate-limited endpoint; there's no need to
	// tune this without a specific, measured reason to.
	b, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// dummyHashForTiming is a valid bcrypt hash of an arbitrary password,
// used only to burn roughly the same CPU time as a real password check
// when the account being logged into doesn't exist (see Login).
const dummyHashForTiming = "$2a$10$CwTycUXWue0Thq9StjUM0uJ8Q0nOtIY6Ck6vE6iZbSJn7O.HxTHwK"

// generateRefreshToken produces a 256-bit cryptographically random token,
// base64url-encoded for safe use as a bearer value (in a header or JSON
// body) with no padding characters to worry about escaping.
func generateRefreshToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// hashToken is what's actually persisted for a refresh token — never the
// raw value. SHA-256 (not bcrypt) is deliberate and correct here: unlike
// a password, a refresh token is already high-entropy random data, not
// something a human chose from a small effective keyspace, so it needs no
// slow, salted KDF to resist brute force — a fast cryptographic hash is
// the right tool, and using bcrypt here would only add unnecessary
// latency to every single authenticated request's refresh flow.
func hashToken(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}
