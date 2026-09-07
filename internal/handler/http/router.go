package http

import (
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	chimw "github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"
	"go.uber.org/zap"

	"github.com/gavinarori/ticketing-backend/internal/config"
	appmw "github.com/gavinarori/ticketing-backend/internal/handler/middleware"
	authsvc "github.com/gavinarori/ticketing-backend/internal/service/auth"
)

// RouterDeps collects everything the router needs to wire up handlers.
type RouterDeps struct {
	Cfg        *config.Config
	Log        *zap.Logger
	Health     *HealthHandler
	Webhook    *WebhookHandler
	Auth       *AuthHandler
	AuthSvc    *authsvc.Service
	AdminVenue *AdminVenueHandler
	AdminEvent *AdminEventHandler
	Event      *EventHandler
	Inventory  *InventoryHandler
	Order      *OrderHandler
}

// NewRouter builds the full chi.Mux for the API: global middleware chain,
// health endpoints, and (eventually) versioned API route groups under
// /api/v1, each isolated by the Tenant middleware once tenants exist.
func NewRouter(deps RouterDeps) http.Handler {
	r := chi.NewRouter()

	// --- Global middleware chain (order matters) ---
	r.Use(appmw.RequestID)          // 1. assign/propagate request ID first
	r.Use(chimw.RealIP)             // 2. resolve real client IP behind proxies
	r.Use(appmw.Recovery(deps.Log)) // 3. catch panics before they escape
	r.Use(appmw.Logging(deps.Log))  // 4. log every request, incl. recovered panics' final status
	r.Use(chimw.Timeout(60 * time.Second))
	r.Use(cors.Handler(cors.Options{
		// TODO: restrict to configured origins (web app, mobile app) before production.
		AllowedOrigins:   []string{"*"},
		AllowedMethods:   []string{"GET", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"},
		AllowedHeaders:   []string{"Accept", "Authorization", "Content-Type", "X-Request-ID", "X-Tenant-ID"},
		ExposedHeaders:   []string{"X-Request-ID"},
		AllowCredentials: true,
		MaxAge:           300,
	}))

	// --- Health / infra probes ---
	r.Get("/healthz", deps.Health.Live)
	r.Get("/readyz", deps.Health.Ready)

	// --- Payment provider webhooks ---
	// Deliberately mounted outside /api/v1 and outside any JWT auth
	// middleware — Stripe authenticates itself via a signed payload, not
	// a bearer token, and it isn't a "versioned API" consumer.
	r.Post("/webhooks/stripe", deps.Webhook.Stripe)

	// --- Versioned API ---
	r.Route("/api/v1", func(v1 chi.Router) {
		v1.Get("/", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"data":{"service":"ticketing-backend","status":"scaffold"}}`))
		})

		// --- Auth: public ---
		v1.Route("/auth", func(auth chi.Router) {
			auth.Post("/register", deps.Auth.Register)
			auth.Post("/login", deps.Auth.Login)
			auth.Post("/refresh", deps.Auth.Refresh)
			auth.Post("/logout", deps.Auth.Logout)
		})

		// --- Auth: requires a valid access token ---
		v1.Group(func(protected chi.Router) {
			protected.Use(appmw.RequireAuth(deps.AuthSvc))
			protected.Get("/me", deps.Auth.Me)
		})

		// --- Admin: bootstrap is its own auth scheme (shared secret, not
		// JWT) since it exists specifically to create the first admin,
		// before any admin — and therefore any valid token — can exist. ---
		v1.Route("/admin", func(admin chi.Router) {
			admin.Post("/bootstrap", deps.Auth.BootstrapAdmin)

			// --- Admin: requires a valid admin access token. Every
			// handler mounted here derives its tenant from the token
			// itself (appmw.TenantIDFromContext), never from a header —
			// see middleware/tenant.go's doc comment for why that
			// distinction is a hard security boundary. ---
			admin.Group(func(protected chi.Router) {
				protected.Use(appmw.RequireAuth(deps.AuthSvc))
				protected.Use(appmw.RequireAdmin)

				protected.Post("/venues", deps.AdminVenue.Create)
				protected.Get("/venues", deps.AdminVenue.List)

				protected.Post("/seat-categories", deps.AdminEvent.CreateSeatCategory)

				protected.Post("/events", deps.AdminEvent.CreateEvent)
				protected.Post("/events/{eventID}/ticket-categories", deps.AdminEvent.CreateTicketCategory)
				protected.Post("/events/{eventID}/publish", deps.AdminEvent.PublishEvent)
			})
		})

		// --- Fan-facing: public browsing, scoped by X-Tenant-ID (see
		// middleware/tenant.go) since a fan account isn't tied to one
		// club — no auth required to look at what's on sale. ---
		v1.Group(func(fan chi.Router) {
			fan.Use(appmw.RequireTenantHeader)
			fan.Get("/events", deps.Event.List)
			fan.Get("/events/{id}", deps.Event.Get)
		})

		// --- Fan-facing: the purchase path. Requires both an
		// authenticated fan AND a resolved tenant — every handler here
		// needs to know who is buying and from which club. ---
		v1.Group(func(purchase chi.Router) {
			purchase.Use(appmw.RequireAuth(deps.AuthSvc))
			purchase.Use(appmw.RequireTenantHeader)

			purchase.Post("/events/{eventID}/queue", deps.Inventory.JoinQueue)
			purchase.Get("/events/{eventID}/queue", deps.Inventory.QueueStatus)

			purchase.Post("/inventory/{id}/hold", deps.Inventory.Hold)
			purchase.Post("/inventory/{id}/release", deps.Inventory.Release)

			purchase.Post("/orders", deps.Order.Create)
			purchase.Post("/orders/{id}/authorize", deps.Order.Authorize)
			purchase.Get("/orders", deps.Order.List)
			purchase.Get("/orders/{id}", deps.Order.Get)
		})
	})

	return r
}
