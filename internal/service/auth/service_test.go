package auth

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/gavinarori/ticketing-backend/internal/config"
	"github.com/gavinarori/ticketing-backend/internal/domain"
)

// --- fakes ---

type fakeUserRepo struct {
	mu    sync.Mutex
	byID  map[uuid.UUID]*domain.User
	email map[string]uuid.UUID
}

func newFakeUserRepo() *fakeUserRepo {
	return &fakeUserRepo{byID: map[uuid.UUID]*domain.User{}, email: map[string]uuid.UUID{}}
}

func (f *fakeUserRepo) Create(ctx context.Context, u *domain.User) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, exists := f.email[u.Email]; exists {
		return domain.NewError("fake.Create", domain.ErrConflict)
	}
	cp := *u
	f.byID[u.ID] = &cp
	f.email[u.Email] = u.ID
	return nil
}

func (f *fakeUserRepo) GetByID(ctx context.Context, id uuid.UUID) (*domain.User, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	u, ok := f.byID[id]
	if !ok {
		return nil, domain.NewError("fake.GetByID", domain.ErrNotFound)
	}
	cp := *u
	return &cp, nil
}

func (f *fakeUserRepo) GetByEmail(ctx context.Context, email string) (*domain.User, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	id, ok := f.email[email]
	if !ok {
		return nil, domain.NewError("fake.GetByEmail", domain.ErrNotFound)
	}
	cp := *f.byID[id]
	return &cp, nil
}

func (f *fakeUserRepo) Update(ctx context.Context, u *domain.User) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.byID[u.ID]; !ok {
		return domain.NewError("fake.Update", domain.ErrNotFound)
	}
	cp := *u
	f.byID[u.ID] = &cp
	return nil
}

var _ domain.UserRepository = (*fakeUserRepo)(nil)

type fakeRefreshTokenRepo struct {
	mu   sync.Mutex
	rows map[uuid.UUID]*domain.RefreshToken
}

func newFakeRefreshTokenRepo() *fakeRefreshTokenRepo {
	return &fakeRefreshTokenRepo{rows: map[uuid.UUID]*domain.RefreshToken{}}
}

func (f *fakeRefreshTokenRepo) Create(ctx context.Context, rt *domain.RefreshToken) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	cp := *rt
	f.rows[rt.ID] = &cp
	return nil
}

func (f *fakeRefreshTokenRepo) GetByTokenHash(ctx context.Context, tokenHash string) (*domain.RefreshToken, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, rt := range f.rows {
		if rt.TokenHash == tokenHash {
			cp := *rt
			return &cp, nil
		}
	}
	return nil, domain.NewError("fake.GetByTokenHash", domain.ErrNotFound)
}

func (f *fakeRefreshTokenRepo) Revoke(ctx context.Context, id uuid.UUID) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	rt, ok := f.rows[id]
	if !ok {
		return domain.NewError("fake.Revoke", domain.ErrNotFound)
	}
	now := time.Now()
	rt.RevokedAt = &now
	return nil
}

func (f *fakeRefreshTokenRepo) RevokeAllForUser(ctx context.Context, userID uuid.UUID) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	now := time.Now()
	for _, rt := range f.rows {
		if rt.UserID == userID {
			rt.RevokedAt = &now
		}
	}
	return nil
}

var _ domain.RefreshTokenRepository = (*fakeRefreshTokenRepo)(nil)

func testJWTConfig() config.JWTConfig {
	return config.JWTConfig{
		Secret:        "test-secret-at-least-16-bytes",
		RefreshSecret: "unused-by-this-service-directly",
		AccessTTL:     15 * time.Minute,
		RefreshTTL:    720 * time.Hour,
	}
}

func newTestService() (*Service, *fakeUserRepo, *fakeRefreshTokenRepo) {
	users := newFakeUserRepo()
	tokens := newFakeRefreshTokenRepo()
	return NewService(users, tokens, testJWTConfig()), users, tokens
}

// --- tests ---

func TestRegister_HashesPasswordAndNeverReturnsIt(t *testing.T) {
	svc, users, _ := newTestService()

	user, err := svc.Register(context.Background(), "Fan@Example.com", "correct-horse-battery", "Jane", "Fan")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if user.PasswordHash != "" {
		t.Error("expected PasswordHash to be cleared on the returned user")
	}
	if user.Role != domain.UserRoleFan {
		t.Errorf("expected role 'fan', got %q", user.Role)
	}
	if user.TenantID != nil {
		t.Error("expected a fan account to have a nil TenantID")
	}

	stored, err := users.GetByEmail(context.Background(), "fan@example.com") // normalized lowercase
	if err != nil {
		t.Fatalf("expected to find user by normalized email: %v", err)
	}
	if stored.PasswordHash == "" || stored.PasswordHash == "correct-horse-battery" {
		t.Error("expected the stored password to be hashed, not empty or plaintext")
	}
}

func TestRegister_WeakPassword_Rejected(t *testing.T) {
	svc, _, _ := newTestService()
	_, err := svc.Register(context.Background(), "fan@example.com", "short", "Jane", "Fan")
	if !errors.Is(err, ErrWeakPassword) {
		t.Fatalf("expected ErrWeakPassword, got %v", err)
	}
}

func TestLogin_Success(t *testing.T) {
	svc, _, _ := newTestService()
	_, err := svc.Register(context.Background(), "fan@example.com", "correct-horse-battery", "Jane", "Fan")
	if err != nil {
		t.Fatal(err)
	}

	pair, err := svc.Login(context.Background(), "fan@example.com", "correct-horse-battery", "test-agent", "127.0.0.1")
	if err != nil {
		t.Fatalf("expected login to succeed, got %v", err)
	}
	if pair.AccessToken == "" || pair.RefreshToken == "" {
		t.Error("expected non-empty access and refresh tokens")
	}

	claims, err := svc.ParseAccessToken(pair.AccessToken)
	if err != nil {
		t.Fatalf("expected the issued access token to parse, got %v", err)
	}
	if claims.Role != "fan" {
		t.Errorf("expected role claim 'fan', got %q", claims.Role)
	}
}

func TestLogin_WrongPassword_Rejected(t *testing.T) {
	svc, _, _ := newTestService()
	_, err := svc.Register(context.Background(), "fan@example.com", "correct-horse-battery", "Jane", "Fan")
	if err != nil {
		t.Fatal(err)
	}

	_, err = svc.Login(context.Background(), "fan@example.com", "wrong-password", "", "")
	if !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("expected ErrInvalidCredentials, got %v", err)
	}
}

func TestLogin_UnknownEmail_ReturnsSameErrorAsWrongPassword(t *testing.T) {
	svc, _, _ := newTestService()
	_, err := svc.Login(context.Background(), "nobody@example.com", "whatever123", "", "")
	if !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("expected ErrInvalidCredentials (not a distinct 'user not found' error), got %v", err)
	}
}

func TestLogin_InactiveAccount_Rejected(t *testing.T) {
	svc, users, _ := newTestService()
	user, err := svc.Register(context.Background(), "fan@example.com", "correct-horse-battery", "Jane", "Fan")
	if err != nil {
		t.Fatal(err)
	}
	stored, _ := users.GetByEmail(context.Background(), "fan@example.com")
	stored.Status = domain.UserStatusSuspended
	_ = users.Update(context.Background(), stored)
	_ = user

	_, err = svc.Login(context.Background(), "fan@example.com", "correct-horse-battery", "", "")
	if !errors.Is(err, ErrAccountInactive) {
		t.Fatalf("expected ErrAccountInactive, got %v", err)
	}
}

func TestRefreshAccessToken_RotatesAndOldTokenBecomesUnusable(t *testing.T) {
	svc, _, tokens := newTestService()
	if _, err := svc.Register(context.Background(), "fan@example.com", "correct-horse-battery", "Jane", "Fan"); err != nil {
		t.Fatal(err)
	}
	first, err := svc.Login(context.Background(), "fan@example.com", "correct-horse-battery", "", "")
	if err != nil {
		t.Fatal(err)
	}

	second, err := svc.RefreshAccessToken(context.Background(), first.RefreshToken, "", "")
	if err != nil {
		t.Fatalf("expected refresh to succeed, got %v", err)
	}
	if second.RefreshToken == first.RefreshToken {
		t.Error("expected a NEW refresh token, got the same one back")
	}

	// The old refresh token must now be rejected — this is the whole
	// point of rotation.
	if _, err := svc.RefreshAccessToken(context.Background(), first.RefreshToken, "", ""); !errors.Is(err, ErrRefreshRevoked) {
		t.Fatalf("expected ErrRefreshRevoked reusing a rotated-away token, got %v", err)
	}

	_ = tokens // sanity: fake is exercised above via the service, not directly
}

func TestRefreshAccessToken_Expired_Rejected(t *testing.T) {
	svc, users, tokens := newTestService()
	user, err := svc.Register(context.Background(), "fan@example.com", "correct-horse-battery", "Jane", "Fan")
	if err != nil {
		t.Fatal(err)
	}
	stored, _ := users.GetByEmail(context.Background(), "fan@example.com")

	raw := "manually-issued-token-for-this-test"
	must(t, tokens.Create(context.Background(), &domain.RefreshToken{
		ID:        uuid.New(),
		UserID:    stored.ID,
		TokenHash: hashToken(raw),
		ExpiresAt: time.Now().Add(-time.Hour), // already expired
	}))

	_, err = svc.RefreshAccessToken(context.Background(), raw, "", "")
	if !errors.Is(err, ErrRefreshExpired) {
		t.Fatalf("expected ErrRefreshExpired, got %v", err)
	}
	_ = user
}

func TestLogout_RevokesToken(t *testing.T) {
	svc, _, _ := newTestService()
	if _, err := svc.Register(context.Background(), "fan@example.com", "correct-horse-battery", "Jane", "Fan"); err != nil {
		t.Fatal(err)
	}
	pair, err := svc.Login(context.Background(), "fan@example.com", "correct-horse-battery", "", "")
	if err != nil {
		t.Fatal(err)
	}

	if err := svc.Logout(context.Background(), pair.RefreshToken); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if _, err := svc.RefreshAccessToken(context.Background(), pair.RefreshToken, "", ""); !errors.Is(err, ErrRefreshRevoked) {
		t.Fatalf("expected a logged-out refresh token to be rejected as revoked, got %v", err)
	}
}

func TestParseAccessToken_RejectsTamperedToken(t *testing.T) {
	svc, _, _ := newTestService()
	if _, err := svc.Register(context.Background(), "fan@example.com", "correct-horse-battery", "Jane", "Fan"); err != nil {
		t.Fatal(err)
	}
	pair, err := svc.Login(context.Background(), "fan@example.com", "correct-horse-battery", "", "")
	if err != nil {
		t.Fatal(err)
	}

	tampered := pair.AccessToken[:len(pair.AccessToken)-1] + "x"
	if _, err := svc.ParseAccessToken(tampered); err == nil {
		t.Error("expected a tampered token to fail signature verification")
	}
}

func TestParseAccessToken_RejectsTokenSignedWithDifferentSecret(t *testing.T) {
	svc1, _, _ := newTestService()
	if _, err := svc1.Register(context.Background(), "fan@example.com", "correct-horse-battery", "Jane", "Fan"); err != nil {
		t.Fatal(err)
	}
	pair, err := svc1.Login(context.Background(), "fan@example.com", "correct-horse-battery", "", "")
	if err != nil {
		t.Fatal(err)
	}

	otherCfg := testJWTConfig()
	otherCfg.Secret = "a-completely-different-secret-value"
	svc2 := NewService(newFakeUserRepo(), newFakeRefreshTokenRepo(), otherCfg)

	if _, err := svc2.ParseAccessToken(pair.AccessToken); err == nil {
		t.Error("expected a token signed with a different secret to fail verification")
	}
}

func TestRegisterAdmin_SetsTenantAndRole(t *testing.T) {
	svc, _, _ := newTestService()
	tenantID := uuid.New()

	user, err := svc.RegisterAdmin(context.Background(), tenantID, "staff@club.example", "correct-horse-battery", "Club", "Staff")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if user.Role != domain.UserRoleAdmin {
		t.Errorf("expected role 'admin', got %q", user.Role)
	}
	if user.TenantID == nil || *user.TenantID != tenantID {
		t.Errorf("expected TenantID %s, got %v", tenantID, user.TenantID)
	}

	pair, err := svc.Login(context.Background(), "staff@club.example", "correct-horse-battery", "", "")
	if err != nil {
		t.Fatalf("expected admin login to succeed: %v", err)
	}
	claims, err := svc.ParseAccessToken(pair.AccessToken)
	if err != nil {
		t.Fatal(err)
	}
	if claims.Role != "admin" {
		t.Errorf("expected role claim 'admin', got %q", claims.Role)
	}
	if claims.TenantID != tenantID.String() {
		t.Errorf("expected tenant_id claim %s, got %q", tenantID, claims.TenantID)
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
