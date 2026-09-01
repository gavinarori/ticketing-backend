//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/gavinarori/ticketing-backend/internal/config"
	apphttp "github.com/gavinarori/ticketing-backend/internal/handler/http"
	pgrepo "github.com/gavinarori/ticketing-backend/internal/repository/postgres"
	authsvc "github.com/gavinarori/ticketing-backend/internal/service/auth"
)

func testLogger(t *testing.T) *zap.Logger {
	t.Helper()
	return zap.NewNop()
}

func seedTenant(t *testing.T, env *testEnv) uuid.UUID {
	t.Helper()
	id := uuid.New()
	_, err := env.pool.Exec(context.Background(), `INSERT INTO tenants (id, slug, name) VALUES ($1, $2, $3)`, id, "tenant-"+id.String()[:8], "Test Tenant")
	if err != nil {
		t.Fatalf("seed tenant: %v", err)
	}
	return id
}

func newTestAuthRouter(t *testing.T, env *testEnv, bootstrapSecret string) (*httptest.Server, *authsvc.Service) {
	t.Helper()
	userRepo := pgrepo.NewUserRepo(env.pool)
	refreshTokenRepo := pgrepo.NewRefreshTokenRepo(env.pool)
	authService := authsvc.NewService(userRepo, refreshTokenRepo, config.JWTConfig{
		Secret:     "test-secret-at-least-16-bytes-long",
		AccessTTL:  15 * time.Minute,
		RefreshTTL: 24 * time.Hour,
	})
	authHandler := apphttp.NewAuthHandler(authService, config.BootstrapConfig{Secret: bootstrapSecret}, testLogger(t))

	router := apphttp.NewRouter(apphttp.RouterDeps{
		Cfg:     &config.Config{},
		Log:     testLogger(t),
		Health:  apphttp.NewHealthHandler(env.pool, env.redis),
		Webhook: apphttp.NewWebhookHandler(env.gateway, env.orderRepo, env.paymentRepo, env.orderSvc, testLogger(t)),
		Auth:    authHandler,
		AuthSvc: authService,
	})
	server := httptest.NewServer(router)
	t.Cleanup(server.Close)
	return server, authService
}

func doJSON(t *testing.T, client *http.Client, method, url, body, bearerToken string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(method, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if bearerToken != "" {
		req.Header.Set("Authorization", "Bearer "+bearerToken)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func readBody(t *testing.T, resp *http.Response) string {
	t.Helper()
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func decodeJSON(t *testing.T, resp *http.Response) map[string]any {
	t.Helper()
	defer resp.Body.Close()
	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode response body: %v", err)
	}
	return out
}

// TestAuthFlow_ThroughRealHTTP is the auth-layer analog of
// TestOrderFlow_HoldToPaid: it drives the actual chi router with real
// net/http request/response plumbing, against real Postgres — not the
// service layer directly. This is deliberately how the "RemoteAddr
// includes a port, and Postgres's INET column rejects that" bug surfaced
// when this flow was exercised by hand with curl against a live process;
// httptest's RemoteAddr is "host:port" just like a real listener's, so
// this test reproduces the exact conditions that found the bug and locks
// the fix in.
func TestAuthFlow_ThroughRealHTTP(t *testing.T) {
	env := setup(t)
	server, _ := newTestAuthRouter(t, env, "")
	client := server.Client()

	resp := doJSON(t, client, http.MethodPost, server.URL+"/api/v1/auth/register",
		`{"email":"http-flow@example.com","password":"correct-horse-battery","first_name":"HTTP","last_name":"Flow"}`, "")
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("register: expected 201, got %d: %s", resp.StatusCode, readBody(t, resp))
	}

	resp = doJSON(t, client, http.MethodPost, server.URL+"/api/v1/auth/login",
		`{"email":"http-flow@example.com","password":"correct-horse-battery"}`, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("login: expected 200, got %d: %s", resp.StatusCode, readBody(t, resp))
	}
	loginData := decodeJSON(t, resp)["data"].(map[string]any)
	accessToken := loginData["access_token"].(string)
	refreshToken := loginData["refresh_token"].(string)
	if accessToken == "" || refreshToken == "" {
		t.Fatal("expected non-empty access and refresh tokens")
	}

	resp = doJSON(t, client, http.MethodGet, server.URL+"/api/v1/me", "", accessToken)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("/me: expected 200, got %d: %s", resp.StatusCode, readBody(t, resp))
	}

	resp = doJSON(t, client, http.MethodGet, server.URL+"/api/v1/me", "", "")
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("/me without token: expected 401, got %d", resp.StatusCode)
	}

	resp = doJSON(t, client, http.MethodPost, server.URL+"/api/v1/auth/refresh", `{"refresh_token":"`+refreshToken+`"}`, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("refresh: expected 200, got %d: %s", resp.StatusCode, readBody(t, resp))
	}
	newRefreshToken := decodeJSON(t, resp)["data"].(map[string]any)["refresh_token"].(string)
	if newRefreshToken == refreshToken {
		t.Error("expected a rotated (different) refresh token")
	}

	// Reusing the old (pre-rotation) refresh token must now fail.
	resp = doJSON(t, client, http.MethodPost, server.URL+"/api/v1/auth/refresh", `{"refresh_token":"`+refreshToken+`"}`, "")
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("reused refresh token: expected 401, got %d", resp.StatusCode)
	}

	// Logout the still-valid current refresh token, then confirm it's rejected too.
	resp = doJSON(t, client, http.MethodPost, server.URL+"/api/v1/auth/logout", `{"refresh_token":"`+newRefreshToken+`"}`, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("logout: expected 200, got %d: %s", resp.StatusCode, readBody(t, resp))
	}
	resp = doJSON(t, client, http.MethodPost, server.URL+"/api/v1/auth/refresh", `{"refresh_token":"`+newRefreshToken+`"}`, "")
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("refresh after logout: expected 401, got %d", resp.StatusCode)
	}
}

// TestBootstrapAdmin_ThroughRealHTTP proves the bootstrap secret gate and
// the resulting admin's role/tenant claims, through the real router.
func TestBootstrapAdmin_ThroughRealHTTP(t *testing.T) {
	env := setup(t)
	tenantID := seedTenant(t, env)
	server, authService := newTestAuthRouter(t, env, "test-bootstrap-secret")
	client := server.Client()

	body := `{"tenant_id":"` + tenantID.String() + `","email":"admin@http-flow.example","password":"correct-horse-battery","first_name":"Club","last_name":"Admin"}`

	req, _ := http.NewRequest(http.MethodPost, server.URL+"/api/v1/admin/bootstrap", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Bootstrap-Secret", "wrong-secret")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("wrong secret: expected 401, got %d", resp.StatusCode)
	}

	req, _ = http.NewRequest(http.MethodPost, server.URL+"/api/v1/admin/bootstrap", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Bootstrap-Secret", "test-bootstrap-secret")
	resp, err = client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("correct secret: expected 201, got %d: %s", resp.StatusCode, readBody(t, resp))
	}

	resp = doJSON(t, client, http.MethodPost, server.URL+"/api/v1/auth/login",
		`{"email":"admin@http-flow.example","password":"correct-horse-battery"}`, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("admin login: expected 200, got %d", resp.StatusCode)
	}
	accessToken := decodeJSON(t, resp)["data"].(map[string]any)["access_token"].(string)

	claims, err := authService.ParseAccessToken(accessToken)
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

// TestBootstrapAdmin_DisabledWhenSecretUnset confirms the endpoint
// refuses to operate (404, not a 401 that would reveal its existence)
// when no bootstrap secret is configured.
func TestBootstrapAdmin_DisabledWhenSecretUnset(t *testing.T) {
	env := setup(t)
	server, _ := newTestAuthRouter(t, env, "") // empty = disabled
	client := server.Client()

	req, _ := http.NewRequest(http.MethodPost, server.URL+"/api/v1/admin/bootstrap", strings.NewReader(`{}`))
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404 when bootstrap is disabled, got %d", resp.StatusCode)
	}
}
