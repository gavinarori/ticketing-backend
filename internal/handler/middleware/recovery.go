package middleware

import (
	"net/http"
	"runtime/debug"

	"go.uber.org/zap"

	"github.com/gavinarori/ticketing-backend/internal/pkg/response"
)

// Recovery catches panics in downstream handlers, logs the stack trace
// with the request ID for correlation, and returns a standard 500 JSON
// envelope instead of letting the connection die or leaking a stack trace
// to the client (chi's default Recoverer does the latter in dev mode).
func Recovery(log *zap.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				if rec := recover(); rec != nil {
					log.Error("panic_recovered",
						zap.String("request_id", RequestIDFromContext(r.Context())),
						zap.Any("panic", rec),
						zap.String("stack", string(debug.Stack())),
					)
					response.Error(w, http.StatusInternalServerError, "internal-error", "something went wrong")
				}
			}()
			next.ServeHTTP(w, r)
		})
	}
}
