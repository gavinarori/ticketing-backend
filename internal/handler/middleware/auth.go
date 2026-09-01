package middleware

import (
	"context"
	"net/http"
	"strings"

	"github.com/google/uuid"

	"github.com/gavinarori/ticketing-backend/internal/pkg/response"
	authsvc "github.com/gavinarori/ticketing-backend/internal/service/auth"
)

type authCtxKey string

const claimsKey authCtxKey = "auth_claims"

// RequireAuth validates the Authorization: Bearer <token> header against
// the given auth service and, on success, stashes the parsed claims in
// the request context for downstream handlers (via ClaimsFromContext /
// UserIDFromContext / RoleFromContext / TenantIDFromContext) — no
// database lookup happens here, since verifying a signed JWT is
// self-contained by design (see internal/service/auth's package doc).
func RequireAuth(authService *authsvc.Service) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			header := r.Header.Get("Authorization")
			token, ok := strings.CutPrefix(header, "Bearer ")
			if !ok || token == "" {
				response.Error(w, http.StatusUnauthorized, "missing-token", "Authorization: Bearer <token> header is required")
				return
			}

			claims, err := authService.ParseAccessToken(token)
			if err != nil {
				response.Error(w, http.StatusUnauthorized, "invalid-token", "access token is invalid or expired")
				return
			}

			ctx := context.WithValue(r.Context(), claimsKey, claims)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// RequireAdmin must be mounted after RequireAuth. It rejects any request
// whose token doesn't carry the admin role — it does NOT check that the
// admin belongs to the tenant being acted on; handlers that operate on a
// specific tenant's resources are responsible for comparing
// TenantIDFromContext against the resource's own tenant_id themselves,
// since "which tenant" is a per-endpoint concern this generic gate can't
// know.
func RequireAdmin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		role, ok := RoleFromContext(r.Context())
		if !ok || role != "admin" {
			response.Error(w, http.StatusForbidden, "forbidden", "admin role required")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// ClaimsFromContext retrieves the claims RequireAuth stored, if any.
func ClaimsFromContext(ctx context.Context) (*authsvc.Claims, bool) {
	claims, ok := ctx.Value(claimsKey).(*authsvc.Claims)
	return claims, ok
}

// UserIDFromContext parses the token's subject claim as the
// authenticated user's ID.
func UserIDFromContext(ctx context.Context) (uuid.UUID, bool) {
	claims, ok := ClaimsFromContext(ctx)
	if !ok {
		return uuid.Nil, false
	}
	id, err := uuid.Parse(claims.Subject)
	if err != nil {
		return uuid.Nil, false
	}
	return id, true
}

func RoleFromContext(ctx context.Context) (string, bool) {
	claims, ok := ClaimsFromContext(ctx)
	if !ok {
		return "", false
	}
	return claims.Role, true
}

// TenantIDFromContext returns the admin's tenant, if the authenticated
// user is an admin (fans have no tenant — see domain.User).
func TenantIDFromContext(ctx context.Context) (uuid.UUID, bool) {
	claims, ok := ClaimsFromContext(ctx)
	if !ok || claims.TenantID == "" {
		return uuid.Nil, false
	}
	id, err := uuid.Parse(claims.TenantID)
	if err != nil {
		return uuid.Nil, false
	}
	return id, true
}
