package middleware

import (
	"context"
	"net/http"

	"github.com/go-chi/chi/v5/middleware"
	"github.com/google/uuid"
)

// ctxKey is an unexported type to avoid context key collisions across
// packages (see https://pkg.go.dev/context#WithValue).
type ctxKey string

const requestIDKey ctxKey = "request_id"

// RequestID assigns a UUID to every incoming request (or reuses an
// upstream-supplied X-Request-ID if present), stores it in the request
// context, and echoes it back in the response header. Every log line and
// error response in this service should be traceable back to a single
// request ID — critical for debugging contention during ticket drops.
func RequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get("X-Request-ID")
		if id == "" {
			id = uuid.NewString()
		}

		ctx := context.WithValue(r.Context(), requestIDKey, id)
		w.Header().Set("X-Request-ID", id)

		// Also feed chi's own middleware.RequestID context key so any
		// third-party middleware relying on it keeps working.
		ctx = context.WithValue(ctx, middleware.RequestIDKey, id)

		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// RequestIDFromContext extracts the request ID set by RequestID. Returns
// "" if none is present (e.g. called outside an HTTP request, such as in a
// worker).
func RequestIDFromContext(ctx context.Context) string {
	id, _ := ctx.Value(requestIDKey).(string)
	return id
}
