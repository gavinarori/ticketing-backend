package middleware

import (
	"context"
	"net/http"

	"github.com/google/uuid"

	"github.com/gavinarori/ticketing-backend/internal/pkg/response"
)

type tenantCtxKey string

const resolvedTenantKey tenantCtxKey = "resolved_tenant_id"

// RequireTenantHeader resolves which tenant a FAN-facing request is
// scoped to from the X-Tenant-ID header, for endpoints like "browse this
// club's events" where the caller isn't tied to one tenant by their own
// account (see domain.User: fans are platform-wide).
//
// Deliberately distinct from admin.TenantIDFromContext (auth.go), which
// reads the tenant from the JWT claims of an authenticated admin. That
// distinction is a real security boundary, not just naming: an admin's
// tenant comes from their signed token because they're PRIVILEGED to act
// on that one tenant's data, and must never be able to switch tenants by
// changing a header. A fan choosing which club's events to browse is not
// a privilege at all — any fan can browse any tenant's public events —
// so a plain header is the right mechanism there and would be the wrong,
// dangerous one for admin routes.
//
// Known simplification: this resolves a raw tenant UUID from the header
// directly rather than a human-friendly slug (which would need a
// database lookup this middleware doesn't have access to a repository
// for). A production frontend would resolve "gor-mahia-fc" -> tenant UUID
// once (e.g. via subdomain), cache it, and send the UUID from there —
// this middleware doesn't change if that's added later, since it only
// ever deals in the UUID.
func RequireTenantHeader(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw := r.Header.Get("X-Tenant-ID")
		if raw == "" {
			response.Error(w, http.StatusBadRequest, "missing-tenant", "X-Tenant-ID header is required")
			return
		}
		tenantID, err := uuid.Parse(raw)
		if err != nil {
			response.Error(w, http.StatusBadRequest, "invalid-tenant", "X-Tenant-ID must be a valid UUID")
			return
		}
		ctx := context.WithValue(r.Context(), resolvedTenantKey, tenantID)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// ResolvedTenantIDFromContext returns the tenant RequireTenantHeader
// resolved. Fan-facing handlers use this; admin handlers use
// TenantIDFromContext (auth.go) instead — see RequireTenantHeader's doc
// comment for why those are deliberately not the same function.
func ResolvedTenantIDFromContext(ctx context.Context) (uuid.UUID, bool) {
	id, ok := ctx.Value(resolvedTenantKey).(uuid.UUID)
	return id, ok
}
