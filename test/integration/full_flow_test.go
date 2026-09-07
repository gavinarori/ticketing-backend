//go:build integration

package integration

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/gavinarori/ticketing-backend/internal/config"
	apphttp "github.com/gavinarori/ticketing-backend/internal/handler/http"
	pgrepo "github.com/gavinarori/ticketing-backend/internal/repository/postgres"
	authsvc "github.com/gavinarori/ticketing-backend/internal/service/auth"
)

// newFullTestRouter wires every handler this codebase has, exactly as
// cmd/api/main.go does, against the real Postgres/Redis behind env — the
// same server the hand-driven curl flow ran against, minus the network
// listener (httptest.NewServer gives us that for free with real
// net/http plumbing).
func newFullTestRouter(t *testing.T, env *testEnv) (*httptest.Server, *authsvc.Service) {
	t.Helper()

	userRepo := pgrepo.NewUserRepo(env.pool)
	refreshTokenRepo := pgrepo.NewRefreshTokenRepo(env.pool)
	venueRepo := pgrepo.NewVenueRepo(env.pool)
	seatCategoryRepo := pgrepo.NewSeatCategoryRepo(env.pool)

	authService := authsvc.NewService(userRepo, refreshTokenRepo, config.JWTConfig{
		Secret:     "test-secret-at-least-16-bytes-long",
		AccessTTL:  15 * time.Minute,
		RefreshTTL: 24 * time.Hour,
	})

	router := apphttp.NewRouter(apphttp.RouterDeps{
		Cfg:        &config.Config{},
		Log:        testLogger(t),
		Health:     apphttp.NewHealthHandler(env.pool, env.redis),
		Webhook:    apphttp.NewWebhookHandler(env.gateway, env.orderRepo, env.paymentRepo, env.orderSvc, testLogger(t)),
		Auth:       apphttp.NewAuthHandler(authService, config.BootstrapConfig{Secret: "test-bootstrap-secret"}, testLogger(t)),
		AuthSvc:    authService,
		AdminVenue: apphttp.NewAdminVenueHandler(venueRepo, testLogger(t)),
		AdminEvent: apphttp.NewAdminEventHandler(env.eventRepo, seatCategoryRepo, env.repo, testLogger(t)),
		Event:      apphttp.NewEventHandler(env.eventRepo, env.repo, testLogger(t)),
		Inventory:  apphttp.NewInventoryHandler(env.svc, testLogger(t)),
		Order:      apphttp.NewOrderHandler(env.orderRepo, env.orderSvc, testLogger(t)),
	})

	server := httptest.NewServer(router)
	t.Cleanup(server.Close)
	return server, authService
}

func jsonField(t *testing.T, resp *http.Response, path ...string) any {
	t.Helper()
	m := decodeJSON(t, resp)
	var cur any = m
	for _, p := range path {
		mm, ok := cur.(map[string]any)
		if !ok {
			t.Fatalf("cannot descend into %q: not an object", p)
		}
		cur, ok = mm[p]
		if !ok {
			t.Fatalf("field %q not found in response", p)
		}
	}
	return cur
}

func authedRequest(t *testing.T, method, url, body, bearerToken, tenantID string) *http.Request {
	t.Helper()
	var req *http.Request
	var err error
	if body != "" {
		req, err = http.NewRequest(method, url, strings.NewReader(body))
	} else {
		req, err = http.NewRequest(method, url, nil)
	}
	if err != nil {
		t.Fatal(err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if bearerToken != "" {
		req.Header.Set("Authorization", "Bearer "+bearerToken)
	}
	if tenantID != "" {
		req.Header.Set("X-Tenant-ID", tenantID)
	}
	return req
}

func mustUUID(t *testing.T, s string) uuid.UUID {
	t.Helper()
	id, err := uuid.Parse(s)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func firstInventoryID(t *testing.T, env *testEnv, eventID string) string {
	t.Helper()
	var id string
	if err := env.pool.QueryRow(context.Background(),
		`SELECT id FROM event_seat_inventory WHERE event_id = $1 LIMIT 1`, mustUUID(t, eventID)).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

// TestFullFlow_AdminPublishesFanBuys is the whole product, end to end,
// through real HTTP against real Postgres and Redis: an admin sets up a
// club, a venue, an event, prices it, and publishes GA inventory; a fan
// registers, browses publicly, is gated by the waiting room, holds a
// seat, creates an order, authorizes payment, and payment is confirmed —
// with the public listing's availability count and the fan's own order
// list both checked afterward, not just individual call return values.
//
// This reproduces, as an automated test, the exact flow that was first
// proven by hand with curl against the live compiled binary.
func TestFullFlow_AdminPublishesFanBuys(t *testing.T) {
	env := setup(t)
	ctx := context.Background()
	server, _ := newFullTestRouter(t, env)
	client := server.Client()

	tenantID := seedTenant(t, env)

	// --- admin bootstrap + login ---
	bootstrapBody := `{"tenant_id":"` + tenantID.String() + `","email":"admin@flow.example","password":"correct-horse-battery","first_name":"Club","last_name":"Admin"}`
	req := authedRequest(t, http.MethodPost, server.URL+"/api/v1/admin/bootstrap", bootstrapBody, "", "")
	req.Header.Set("X-Bootstrap-Secret", "test-bootstrap-secret")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("bootstrap: expected 201, got %d: %s", resp.StatusCode, readBody(t, resp))
	}

	resp = doJSON(t, client, http.MethodPost, server.URL+"/api/v1/auth/login",
		`{"email":"admin@flow.example","password":"correct-horse-battery"}`, "")
	adminToken := jsonField(t, resp, "data", "access_token").(string)

	// --- admin: venue ---
	resp = doJSON(t, client, http.MethodPost, server.URL+"/api/v1/admin/venues",
		`{"name":"Flow Stadium","city":"Nairobi","country":"Kenya"}`, adminToken)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create venue: expected 201, got %d: %s", resp.StatusCode, readBody(t, resp))
	}
	venueID := jsonField(t, resp, "data", "id").(string)

	// --- admin: seat category ---
	resp = doJSON(t, client, http.MethodPost, server.URL+"/api/v1/admin/seat-categories",
		`{"name":"General"}`, adminToken)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create seat category: expected 201, got %d: %s", resp.StatusCode, readBody(t, resp))
	}
	seatCategoryID := jsonField(t, resp, "data", "id").(string)

	// --- admin: event ---
	eventBody := `{"venue_id":"` + venueID + `","name":"Flow Match","starts_at":"2027-01-01T15:00:00Z","sales_start_at":"2026-01-01T00:00:00Z","sales_end_at":"2026-12-31T23:59:59Z"}`
	resp = doJSON(t, client, http.MethodPost, server.URL+"/api/v1/admin/events", eventBody, adminToken)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create event: expected 201, got %d: %s", resp.StatusCode, readBody(t, resp))
	}
	eventID := jsonField(t, resp, "data", "id").(string)

	// --- admin: ticket category ---
	etcBody := `{"seat_category_id":"` + seatCategoryID + `","price_cents":150000,"currency":"KES","max_per_order":4}`
	resp = doJSON(t, client, http.MethodPost, server.URL+"/api/v1/admin/events/"+eventID+"/ticket-categories", etcBody, adminToken)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create ticket category: expected 201, got %d: %s", resp.StatusCode, readBody(t, resp))
	}
	etcID := jsonField(t, resp, "data", "id").(string)

	// --- admin: publish (3 GA tickets) ---
	publishBody := `{"event_ticket_category_id":"` + etcID + `","quantity":3}`
	resp = doJSON(t, client, http.MethodPost, server.URL+"/api/v1/admin/events/"+eventID+"/publish", publishBody, adminToken)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("publish: expected 201, got %d: %s", resp.StatusCode, readBody(t, resp))
	}
	if got := jsonField(t, resp, "data", "generated"); got.(float64) != 3 {
		t.Fatalf("expected 3 inventory rows generated, got %v", got)
	}

	// --- fan: register + login ---
	resp = doJSON(t, client, http.MethodPost, server.URL+"/api/v1/auth/register",
		`{"email":"fan@flow.example","password":"correct-horse-battery","first_name":"Fan","last_name":"Flow"}`, "")
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("register: expected 201, got %d: %s", resp.StatusCode, readBody(t, resp))
	}
	resp = doJSON(t, client, http.MethodPost, server.URL+"/api/v1/auth/login",
		`{"email":"fan@flow.example","password":"correct-horse-battery"}`, "")
	fanToken := jsonField(t, resp, "data", "access_token").(string)

	// --- fan: public browse (needs tenant header, no auth) ---
	req = authedRequest(t, http.MethodGet, server.URL+"/api/v1/events", "", "", tenantID.String())
	resp, err = client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list events: expected 200, got %d", resp.StatusCode)
	}

	// --- fan: hold BEFORE joining the queue must be rejected ---
	invID := firstInventoryID(t, env, eventID)
	req = authedRequest(t, http.MethodPost, server.URL+"/api/v1/inventory/"+invID+"/hold",
		`{"event_id":"`+eventID+`"}`, fanToken, tenantID.String())
	resp, err = client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("hold before admission: expected 403, got %d: %s", resp.StatusCode, readBody(t, resp))
	}

	// --- fan: join queue, then admit directly via the service (the
	// admission-control worker loop isn't built yet — a documented gap;
	// this is the test's stand-in for it, same as the live curl flow used
	// a raw Redis script for). ---
	req = authedRequest(t, http.MethodPost, server.URL+"/api/v1/events/"+eventID+"/queue", "", fanToken, tenantID.String())
	resp, err = client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("join queue: expected 200, got %d", resp.StatusCode)
	}
	eventUUID := mustUUID(t, eventID)
	if _, err := env.svc.AdmitNext(ctx, eventUUID, 10); err != nil {
		t.Fatalf("admit next: %v", err)
	}

	// --- fan: hold now succeeds ---
	req = authedRequest(t, http.MethodPost, server.URL+"/api/v1/inventory/"+invID+"/hold",
		`{"event_id":"`+eventID+`"}`, fanToken, tenantID.String())
	resp, err = client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("hold: expected 200, got %d: %s", resp.StatusCode, readBody(t, resp))
	}
	holdToken := jsonField(t, resp, "data", "hold_token").(string)

	// --- fan: create order ---
	orderBody := `{"idempotency_key":"full-flow-test","items":[{"inventory_id":"` + invID + `","hold_token":"` + holdToken + `"}]}`
	req = authedRequest(t, http.MethodPost, server.URL+"/api/v1/orders", orderBody, fanToken, tenantID.String())
	resp, err = client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create order: expected 201, got %d: %s", resp.StatusCode, readBody(t, resp))
	}
	orderData := decodeJSON(t, resp)["data"].(map[string]any)
	orderID := orderData["id"].(string)
	if orderData["total_cents"].(float64) != 150000 {
		t.Fatalf("expected order total 150000 (matching the ticket category price), got %v", orderData["total_cents"])
	}

	// --- fan: authorize payment ---
	req = authedRequest(t, http.MethodPost, server.URL+"/api/v1/orders/"+orderID+"/authorize", "", fanToken, tenantID.String())
	resp, err = client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("authorize: expected 200, got %d: %s", resp.StatusCode, readBody(t, resp))
	}

	// --- confirm payment directly via the order service (mirrors what
	// the webhook handler does after signature verification — webhook
	// signature verification itself is covered by
	// TestOrderFlow_HoldToPaid and the payment package's own tests). ---
	var paymentID uuid.UUID
	if err := env.pool.QueryRow(ctx, `SELECT id FROM payments WHERE order_id = $1`, mustUUID(t, orderID)).Scan(&paymentID); err != nil {
		t.Fatal(err)
	}
	if err := env.orderSvc.ConfirmPayment(ctx, tenantID, mustUUID(t, orderID), paymentID); err != nil {
		t.Fatalf("confirm payment: %v", err)
	}

	// --- fan: order list now shows paid ---
	req = authedRequest(t, http.MethodGet, server.URL+"/api/v1/orders", "", fanToken, tenantID.String())
	resp, err = client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	orders := decodeJSON(t, resp)["data"].([]any)
	if len(orders) != 1 {
		t.Fatalf("expected 1 order, got %d", len(orders))
	}
	if orders[0].(map[string]any)["status"] != "paid" {
		t.Fatalf("expected order status 'paid', got %v", orders[0].(map[string]any)["status"])
	}

	// --- public listing reflects the sale: 2 of 3 now available ---
	req = authedRequest(t, http.MethodGet, server.URL+"/api/v1/events/"+eventID, "", "", tenantID.String())
	resp, err = client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	categories := decodeJSON(t, resp)["data"].(map[string]any)["ticket_categories"].([]any)
	available := categories[0].(map[string]any)["available"].(float64)
	if available != 2 {
		t.Fatalf("expected 2 available after one sale out of 3, got %v", available)
	}
}
