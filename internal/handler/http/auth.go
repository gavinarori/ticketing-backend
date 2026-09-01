package http

import (
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"strings"

	"github.com/go-playground/validator/v10"
	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/gavinarori/ticketing-backend/internal/config"
	"github.com/gavinarori/ticketing-backend/internal/domain"
	appmw "github.com/gavinarori/ticketing-backend/internal/handler/middleware"
	"github.com/gavinarori/ticketing-backend/internal/pkg/response"
	authsvc "github.com/gavinarori/ticketing-backend/internal/service/auth"
)

type AuthHandler struct {
	auth      *authsvc.Service
	bootstrap config.BootstrapConfig
	validate  *validator.Validate
	log       *zap.Logger
}

func NewAuthHandler(auth *authsvc.Service, bootstrap config.BootstrapConfig, log *zap.Logger) *AuthHandler {
	return &AuthHandler{auth: auth, bootstrap: bootstrap, validate: validator.New(), log: log}
}

type registerRequest struct {
	Email     string `json:"email" validate:"required,email"`
	Password  string `json:"password" validate:"required,min=8"`
	FirstName string `json:"first_name" validate:"required"`
	LastName  string `json:"last_name" validate:"required"`
}

type userResponse struct {
	ID       string  `json:"id"`
	Email    string  `json:"email"`
	Role     string  `json:"role"`
	TenantID *string `json:"tenant_id,omitempty"`
}

func toUserResponse(u *domain.User) userResponse {
	resp := userResponse{ID: u.ID.String(), Email: u.Email, Role: string(u.Role)}
	if u.TenantID != nil {
		id := u.TenantID.String()
		resp.TenantID = &id
	}
	return resp
}

type tokenPairResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresAt    string `json:"expires_at"`
}

func toTokenPairResponse(p *authsvc.TokenPair) tokenPairResponse {
	return tokenPairResponse{
		AccessToken:  p.AccessToken,
		RefreshToken: p.RefreshToken,
		ExpiresAt:    p.AccessTokenExpiresAt.Format(http.TimeFormat),
	}
}

// Register handles POST /api/v1/auth/register — always creates a fan
// account. There is deliberately no way to request an admin account
// through this endpoint; see the package doc on RegisterAdmin.
func (h *AuthHandler) Register(w http.ResponseWriter, r *http.Request) {
	var req registerRequest
	if !decodeAndValidate(w, r, h.validate, &req) {
		return
	}

	user, err := h.auth.Register(r.Context(), req.Email, req.Password, req.FirstName, req.LastName)
	if err != nil {
		if errors.Is(err, authsvc.ErrWeakPassword) {
			response.Error(w, http.StatusBadRequest, "weak-password", err.Error())
			return
		}
		if errors.Is(err, domain.ErrConflict) {
			response.Error(w, http.StatusConflict, "email-taken", "an account with this email already exists")
			return
		}
		h.log.Error("register_failed", zap.Error(err))
		response.Error(w, http.StatusInternalServerError, "internal-error", "could not register")
		return
	}

	response.JSON(w, http.StatusCreated, toUserResponse(user))
}

type loginRequest struct {
	Email    string `json:"email" validate:"required,email"`
	Password string `json:"password" validate:"required"`
}

func (h *AuthHandler) Login(w http.ResponseWriter, r *http.Request) {
	var req loginRequest
	if !decodeAndValidate(w, r, h.validate, &req) {
		return
	}

	pair, err := h.auth.Login(r.Context(), req.Email, req.Password, r.UserAgent(), clientIP(r))
	if err != nil {
		if errors.Is(err, authsvc.ErrInvalidCredentials) || errors.Is(err, authsvc.ErrAccountInactive) {
			// Same response for both — see ErrInvalidCredentials' doc
			// comment on why account existence is never distinguishable
			// from a wrong password at this boundary either.
			response.Error(w, http.StatusUnauthorized, "invalid-credentials", "invalid email or password")
			return
		}
		h.log.Error("login_failed", zap.Error(err))
		response.Error(w, http.StatusInternalServerError, "internal-error", "could not log in")
		return
	}

	response.JSON(w, http.StatusOK, toTokenPairResponse(pair))
}

type refreshRequest struct {
	RefreshToken string `json:"refresh_token" validate:"required"`
}

func (h *AuthHandler) Refresh(w http.ResponseWriter, r *http.Request) {
	var req refreshRequest
	if !decodeAndValidate(w, r, h.validate, &req) {
		return
	}

	pair, err := h.auth.RefreshAccessToken(r.Context(), req.RefreshToken, r.UserAgent(), clientIP(r))
	if err != nil {
		switch {
		case errors.Is(err, authsvc.ErrInvalidCredentials), errors.Is(err, authsvc.ErrRefreshExpired), errors.Is(err, authsvc.ErrRefreshRevoked):
			response.Error(w, http.StatusUnauthorized, "invalid-refresh-token", "refresh token is invalid, expired, or revoked")
			return
		case errors.Is(err, authsvc.ErrAccountInactive):
			response.Error(w, http.StatusForbidden, "account-inactive", "account is not active")
			return
		}
		h.log.Error("refresh_failed", zap.Error(err))
		response.Error(w, http.StatusInternalServerError, "internal-error", "could not refresh session")
		return
	}

	response.JSON(w, http.StatusOK, toTokenPairResponse(pair))
}

func (h *AuthHandler) Logout(w http.ResponseWriter, r *http.Request) {
	var req refreshRequest
	if !decodeAndValidate(w, r, h.validate, &req) {
		return
	}
	if err := h.auth.Logout(r.Context(), req.RefreshToken); err != nil {
		h.log.Error("logout_failed", zap.Error(err))
		response.Error(w, http.StatusInternalServerError, "internal-error", "could not log out")
		return
	}
	response.JSON(w, http.StatusOK, map[string]string{"status": "logged_out"})
}

// Me handles GET /api/v1/me — mounted behind RequireAuth, so the caller
// is already known; this just echoes back who the token says they are.
// A real implementation would load the full profile from UserRepository;
// kept to the token's own claims here since that's all this round wires
// up end to end.
func (h *AuthHandler) Me(w http.ResponseWriter, r *http.Request) {
	userID, ok := appmw.UserIDFromContext(r.Context())
	if !ok {
		response.Error(w, http.StatusUnauthorized, "missing-token", "not authenticated")
		return
	}
	role, _ := appmw.RoleFromContext(r.Context())
	resp := map[string]any{"id": userID.String(), "role": role}
	if tenantID, ok := appmw.TenantIDFromContext(r.Context()); ok {
		resp["tenant_id"] = tenantID.String()
	}
	response.JSON(w, http.StatusOK, resp)
}

type bootstrapAdminRequest struct {
	TenantID  string `json:"tenant_id" validate:"required,uuid"`
	Email     string `json:"email" validate:"required,email"`
	Password  string `json:"password" validate:"required,min=8"`
	FirstName string `json:"first_name" validate:"required"`
	LastName  string `json:"last_name" validate:"required"`
}

// BootstrapAdmin handles POST /api/v1/admin/bootstrap — creates the very
// first admin for a tenant, guarded by a shared secret (X-Bootstrap-
// Secret header) instead of a JWT, since no admin exists yet to issue
// one. Disabled entirely (404, not just "wrong secret") when
// ADMIN_BOOTSTRAP_SECRET is unset — an empty configured secret must never
// match an empty/missing header.
func (h *AuthHandler) BootstrapAdmin(w http.ResponseWriter, r *http.Request) {
	if h.bootstrap.Secret == "" {
		response.Error(w, http.StatusNotFound, "not-found", "not found")
		return
	}
	if r.Header.Get("X-Bootstrap-Secret") != h.bootstrap.Secret {
		response.Error(w, http.StatusUnauthorized, "invalid-secret", "invalid bootstrap secret")
		return
	}

	var req bootstrapAdminRequest
	if !decodeAndValidate(w, r, h.validate, &req) {
		return
	}
	tenantID, err := uuid.Parse(req.TenantID)
	if err != nil {
		response.Error(w, http.StatusBadRequest, "invalid-tenant-id", "tenant_id must be a valid UUID")
		return
	}

	user, err := h.auth.RegisterAdmin(r.Context(), tenantID, req.Email, req.Password, req.FirstName, req.LastName)
	if err != nil {
		if errors.Is(err, authsvc.ErrWeakPassword) {
			response.Error(w, http.StatusBadRequest, "weak-password", err.Error())
			return
		}
		if errors.Is(err, domain.ErrConflict) {
			response.Error(w, http.StatusConflict, "email-taken", "an account with this email already exists")
			return
		}
		h.log.Error("bootstrap_admin_failed", zap.Error(err))
		response.Error(w, http.StatusInternalServerError, "internal-error", "could not create admin")
		return
	}

	response.JSON(w, http.StatusCreated, toUserResponse(user))
}

// decodeAndValidate is a small shared helper: decode the JSON body into
// dst, then run struct-tag validation, writing an appropriate error
// response and returning false on either failure so handlers can just
// `if !decodeAndValidate(...) { return }`.
func decodeAndValidate(w http.ResponseWriter, r *http.Request, v *validator.Validate, dst any) bool {
	if err := json.NewDecoder(r.Body).Decode(dst); err != nil {
		response.Error(w, http.StatusBadRequest, "invalid-body", "could not parse request body")
		return false
	}
	if err := v.Struct(dst); err != nil {
		response.Error(w, http.StatusBadRequest, "validation-failed", err.Error())
		return false
	}
	return true
}

// clientIP takes the first hop off X-Forwarded-For if present (set by
// chi's RealIP middleware ahead of this handler in the chain), falling
// back to RemoteAddr's host portion — deliberately stripped of the port
// via net.SplitHostPort, since RemoteAddr is "host:port" and the
// ip_address column this feeds is a real Postgres INET, which rejects a
// port-suffixed value outright. Best-effort only — this is stored purely
// for the account holder's own audit trail (see auth.Service.Login's doc
// comment), never used for any security decision.
func clientIP(r *http.Request) string {
	if fwd := r.Header.Get("X-Forwarded-For"); fwd != "" {
		// X-Forwarded-For can be a comma-separated chain of proxies; the
		// client's own address is the first entry.
		if idx := strings.Index(fwd, ","); idx != -1 {
			fwd = fwd[:idx]
		}
		return strings.TrimSpace(fwd)
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return "" // unparseable — leave it out rather than feed garbage to an INET column
	}
	return host
}
