package middleware

import (
	"net/http"
	"time"

	"github.com/go-chi/chi/v5/middleware"
	"go.uber.org/zap"
)

// Logging returns a middleware that emits one structured log line per
// request, tagged with the request ID from RequestID so it can be
// correlated with any error responses or downstream service logs.
//
// This must be mounted after RequestID in the chain.
func Logging(log *zap.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()

			// wrap ResponseWriter so we can capture the status code/size
			// chi's middleware.WrapResponseWriter is the standard idiom here.
			ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)

			next.ServeHTTP(ww, r)

			log.Info("http_request",
				zap.String("request_id", RequestIDFromContext(r.Context())),
				zap.String("method", r.Method),
				zap.String("path", r.URL.Path),
				zap.Int("status", ww.Status()),
				zap.Int("bytes", ww.BytesWritten()),
				zap.Duration("duration", time.Since(start)),
				zap.String("remote_addr", r.RemoteAddr),
			)
		})
	}
}
