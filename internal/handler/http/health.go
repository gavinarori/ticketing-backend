package http

import (
	"context"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	"github.com/gavinarori/ticketing-backend/internal/pkg/response"
)

// HealthHandler exposes liveness and readiness endpoints. Kubernetes (or
// any orchestrator) should point its liveness probe at Live and its
// readiness probe at Ready — the distinction matters under load: a pod
// that's alive but can't reach Postgres/Redis should be pulled from the
// load balancer (readiness) without being restarted (liveness).
type HealthHandler struct {
	db    *pgxpool.Pool
	redis *redis.Client
}

func NewHealthHandler(db *pgxpool.Pool, redisClient *redis.Client) *HealthHandler {
	return &HealthHandler{db: db, redis: redisClient}
}

// Live always returns 200 as long as the process is running and able to
// handle HTTP — it deliberately does NOT check dependencies.
func (h *HealthHandler) Live(w http.ResponseWriter, r *http.Request) {
	response.JSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// Ready checks Postgres and Redis connectivity with a short timeout and
// reports 503 if either is unreachable, so the pod is taken out of rotation
// during a dependency outage instead of accepting requests it can't serve.
func (h *HealthHandler) Ready(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()

	checks := map[string]string{}
	healthy := true

	if err := h.db.Ping(ctx); err != nil {
		checks["postgres"] = err.Error()
		healthy = false
	} else {
		checks["postgres"] = "ok"
	}

	if err := h.redis.Ping(ctx).Err(); err != nil {
		checks["redis"] = err.Error()
		healthy = false
	} else {
		checks["redis"] = "ok"
	}

	status := http.StatusOK
	if !healthy {
		status = http.StatusServiceUnavailable
	}
	response.JSON(w, status, checks)
}
