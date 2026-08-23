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
)

// RouterDeps collects everything the router needs to wire up handlers.
// As services are added (event, inventory, order, ...) their handlers get
// added here rather than router.go reaching into globals.
type RouterDeps struct {
	Cfg    *config.Config
	Log    *zap.Logger
	Health *HealthHandler
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

	// --- Versioned API ---
	r.Route("/api/v1", func(v1 chi.Router) {
		// Placeholder — event, seatmap, order, ticket, user routes will be
		// mounted here as their services/handlers are built out, e.g.:
		// v1.Mount("/events", eventHandler.Routes())
		v1.Get("/", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"data":{"service":"ticketing-backend","status":"scaffold"}}`))
		})
	})

	return r
}
