// Package oauth provides AT Protocol OAuth implementation.
// Auth middleware for protecting HTTP endpoints with DPoP/Bearer tokens.
package oauth

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"
)

// contextKey is a custom type for context keys to avoid collisions.
type contextKey string

const (
	// UserIDKey is the context key for the authenticated user's ID.
	UserIDKey contextKey = "user_id"
	// AccessTokenKey is the context key for the validated access token.
	AccessTokenKey contextKey = "access_token"
	// ScopesKey is the context key for the token's scopes.
	ScopesKey contextKey = "scopes"
)

// DefaultMaxDPoPAge is the default maximum age for DPoP proofs (5 minutes).
const DefaultMaxDPoPAge = 300

// AccessTokenStore provides access to OAuth access tokens.
type AccessTokenStore interface {
	// Get retrieves an access token by token string.
	// Returns nil, nil if not found.
	Get(ctx context.Context, token string) (*AccessToken, error)
}

// JTIStore provides DPoP JTI replay protection.
type JTIStore interface {
	// InsertIfNew records a JTI as used and reports whether it was
	// newly inserted. A false return indicates the JTI already
	// existed, i.e. a replay.
	InsertIfNew(ctx context.Context, jti *DPoPJTI) (bool, error)
}

// AuthMiddleware validates OAuth access tokens for protected resources.
type AuthMiddleware struct {
	tokens      AccessTokenStore
	jtis        JTIStore
	maxDPoPAge  int64
	resourceURL string // Base URL for the protected resource
}

// NewAuthMiddleware creates a new auth middleware.
func NewAuthMiddleware(tokens AccessTokenStore, jtis JTIStore, resourceURL string) *AuthMiddleware {
	return &AuthMiddleware{
		tokens:      tokens,
		jtis:        jtis,
		maxDPoPAge:  DefaultMaxDPoPAge,
		resourceURL: strings.TrimSuffix(resourceURL, "/"),
	}
}

// WithMaxDPoPAge sets the maximum age for DPoP proofs.
func (m *AuthMiddleware) WithMaxDPoPAge(seconds int64) *AuthMiddleware {
	m.maxDPoPAge = seconds
	return m
}

// AuthResult contains the result of a successful authentication.
type AuthResult struct {
	UserID      string
	AccessToken *AccessToken
	Scopes      []string
}

// ValidateRequest validates the Authorization header and returns the auth result.
// This is the core validation logic used by both RequireAuth and OptionalAuth.
func (m *AuthMiddleware) ValidateRequest(r *http.Request) (*AuthResult, error) {
	authHeader := r.Header.Get("Authorization")
	if authHeader == "" {
		return nil, ErrMissingAuth
	}

	parts := strings.SplitN(authHeader, " ", 2)
	if len(parts) != 2 {
		return nil, ErrInvalidAuthFormat
	}

	scheme := parts[0]
	token := parts[1]

	switch scheme {
	case "DPoP":
		return m.validateDPoPToken(r, token)
	case "Bearer":
		return m.validateBearerToken(r.Context(), token)
	default:
		return nil, ErrInvalidAuthScheme
	}
}

// validateDPoPToken validates a DPoP-bound access token.
func (m *AuthMiddleware) validateDPoPToken(r *http.Request, token string) (*AuthResult, error) {
	ctx := r.Context()

	// Get DPoP proof from header
	dpopProof := r.Header.Get("DPoP")
	if dpopProof == "" {
		return nil, ErrMissingDPoPProof
	}

	// Build the resource URL
	resourceURL := m.resourceURL + r.URL.Path
	if r.URL.RawQuery != "" {
		resourceURL += "?" + r.URL.RawQuery
	}

	// Verify the DPoP proof
	result, err := VerifyDPoPProof(dpopProof, r.Method, resourceURL, m.maxDPoPAge)
	if err != nil {
		return nil, &AuthError{Code: "invalid_dpop_proof", Description: err.Error()}
	}

	// Race-safe replay detection: InsertIfNew returns (true, nil)
	// if the row was new and (false, nil) if a row with the same
	// JTI already existed. The old Exists-then-Insert pattern had a
	// TOCTOU window between the check and the insert.
	jti := &DPoPJTI{
		JTI:       result.JTI,
		CreatedAt: result.IAT,
	}
	inserted, err := m.jtis.InsertIfNew(ctx, jti)
	if err != nil {
		return nil, ErrServerError
	}
	if !inserted {
		return nil, ErrDPoPReplay
	}

	// Get the access token
	accessToken, err := m.tokens.Get(ctx, token)
	if err != nil {
		return nil, ErrServerError
	}
	if accessToken == nil {
		return nil, ErrInvalidToken
	}

	// Check token validity
	if accessToken.IsExpired() {
		return nil, ErrTokenExpired
	}
	if accessToken.Revoked {
		return nil, ErrTokenRevoked
	}

	// Verify DPoP binding. The nil-check above must remain before
	// the dereference below; the constant-time compare does not
	// guard against nil. JKT is derived from public proof material,
	// so a timing leak is mostly cosmetic — but the codebase already
	// uses subtle.ConstantTimeCompare for PKCE (which has the same
	// "public-ish material" property), so consistency is the
	// strongest argument here.
	if accessToken.DPoPJKT == nil {
		return nil, ErrTokenNotDPoPBound
	}
	if subtle.ConstantTimeCompare([]byte(*accessToken.DPoPJKT), []byte(result.JKT)) != 1 {
		return nil, ErrDPoPKeyMismatch
	}

	// Check user
	if accessToken.UserID == nil {
		return nil, ErrTokenNoUser
	}

	// Parse scopes
	var scopes []string
	if accessToken.Scope != nil {
		scopes = ParseScopes(*accessToken.Scope)
	}

	return &AuthResult{
		UserID:      *accessToken.UserID,
		AccessToken: accessToken,
		Scopes:      scopes,
	}, nil
}

// validateBearerToken validates a Bearer access token (non-DPoP).
func (m *AuthMiddleware) validateBearerToken(ctx context.Context, token string) (*AuthResult, error) {
	// Get the access token
	accessToken, err := m.tokens.Get(ctx, token)
	if err != nil {
		return nil, ErrServerError
	}
	if accessToken == nil {
		return nil, ErrInvalidToken
	}

	// Check token validity
	if accessToken.IsExpired() {
		return nil, ErrTokenExpired
	}
	if accessToken.Revoked {
		return nil, ErrTokenRevoked
	}

	// DPoP-bound tokens MUST use DPoP authorization
	if accessToken.DPoPJKT != nil {
		return nil, ErrDPoPRequired
	}

	// Check user
	if accessToken.UserID == nil {
		return nil, ErrTokenNoUser
	}

	// Parse scopes
	var scopes []string
	if accessToken.Scope != nil {
		scopes = ParseScopes(*accessToken.Scope)
	}

	return &AuthResult{
		UserID:      *accessToken.UserID,
		AccessToken: accessToken,
		Scopes:      scopes,
	}, nil
}

// RequireAuth returns middleware that requires authentication.
// Requests without valid authentication receive a 401 response.
func (m *AuthMiddleware) RequireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		result, err := m.ValidateRequest(r)
		if err != nil {
			m.writeError(w, err)
			return
		}

		// Add auth info to context
		ctx := context.WithValue(r.Context(), UserIDKey, result.UserID)
		ctx = context.WithValue(ctx, AccessTokenKey, result.AccessToken)
		ctx = context.WithValue(ctx, ScopesKey, result.Scopes)

		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// OptionalAuth returns middleware that accepts optional authentication.
// Requests without authentication proceed without user context.
// Requests whose Authorization header is present but *not* a valid
// OAuth access token also proceed without user context — the
// downstream handler is expected to perform its own auth check (for
// example, the admin GraphQL handler accepts an ADMIN_API_KEY bearer
// token that is not an OAuth token). Previously this middleware
// returned 401 unconditionally on a failed OAuth validation, which
// broke the admin API-key auth path.
//
// Handlers that need to *require* a valid OAuth token should use
// RequireAuth, not OptionalAuth.
func (m *AuthMiddleware) OptionalAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Check if Authorization header is present
		if r.Header.Get("Authorization") == "" {
			// No auth provided, proceed without user context
			next.ServeHTTP(w, r)
			return
		}

		// Auth header present, try to validate it as an OAuth token.
		// On failure, pass through with no user context — the
		// downstream handler may accept a different auth scheme
		// (API key, etc.).
		result, err := m.ValidateRequest(r)
		if err != nil {
			next.ServeHTTP(w, r)
			return
		}

		// Add auth info to context
		ctx := context.WithValue(r.Context(), UserIDKey, result.UserID)
		ctx = context.WithValue(ctx, AccessTokenKey, result.AccessToken)
		ctx = context.WithValue(ctx, ScopesKey, result.Scopes)

		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// RequireScope returns middleware that requires specific scopes.
// Must be used after RequireAuth or OptionalAuth.
func (m *AuthMiddleware) RequireScope(requiredScopes ...string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			scopes, ok := r.Context().Value(ScopesKey).([]string)
			if !ok {
				m.writeError(w, ErrInsufficientScope)
				return
			}

			// Check if all required scopes are present
			scopeSet := make(map[string]bool)
			for _, s := range scopes {
				scopeSet[s] = true
			}

			for _, required := range requiredScopes {
				if !scopeSet[required] {
					m.writeError(w, ErrInsufficientScope)
					return
				}
			}

			next.ServeHTTP(w, r)
		})
	}
}

// writeError writes an authentication error response.
func (m *AuthMiddleware) writeError(w http.ResponseWriter, err error) {
	w.Header().Set("Content-Type", "application/json")

	var authErr *AuthError
	if errors.As(err, &authErr) {
		status := authErr.HTTPStatus()
		w.WriteHeader(status)

		resp := map[string]string{"error": authErr.Code}
		if authErr.Description != "" {
			resp["error_description"] = authErr.Description
		}
		_ = json.NewEncoder(w).Encode(resp)
		return
	}

	// Default to 401 for unknown errors
	w.WriteHeader(http.StatusUnauthorized)
	_ = json.NewEncoder(w).Encode(map[string]string{
		"error": "unauthorized",
	})
}

// UserIDFromContext extracts the user ID from the request context.
// Returns empty string if not authenticated.
func UserIDFromContext(ctx context.Context) string {
	if v := ctx.Value(UserIDKey); v != nil {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

// AccessTokenFromContext extracts the access token from the request context.
// Returns nil if not authenticated.
func AccessTokenFromContext(ctx context.Context) *AccessToken {
	if v := ctx.Value(AccessTokenKey); v != nil {
		if t, ok := v.(*AccessToken); ok {
			return t
		}
	}
	return nil
}

// ScopesFromContext extracts the scopes from the request context.
// Returns nil if not authenticated.
func ScopesFromContext(ctx context.Context) []string {
	if v := ctx.Value(ScopesKey); v != nil {
		if s, ok := v.([]string); ok {
			return s
		}
	}
	return nil
}

// Common authentication errors.
var (
	ErrMissingAuth       = &AuthError{Code: "missing_authorization", Description: "Missing Authorization header"}
	ErrInvalidAuthFormat = &AuthError{Code: "invalid_request", Description: "Invalid Authorization header format"}
	ErrInvalidAuthScheme = &AuthError{Code: "invalid_request", Description: "Unsupported authorization scheme"}
	ErrMissingDPoPProof  = &AuthError{Code: "invalid_dpop_proof", Description: "Missing DPoP proof for DPoP-bound token"}
	ErrDPoPReplay        = &AuthError{Code: "invalid_dpop_proof", Description: "DPoP proof replay detected"}
	ErrDPoPKeyMismatch   = &AuthError{Code: "invalid_dpop_proof", Description: "DPoP key mismatch"}
	ErrTokenNotDPoPBound = &AuthError{Code: "invalid_token", Description: "Token is not DPoP-bound"}
	ErrDPoPRequired      = &AuthError{Code: "invalid_token", Description: "DPoP-bound token requires DPoP authorization"}
	ErrInvalidToken      = &AuthError{Code: "invalid_token", Description: "Invalid access token"}
	ErrTokenExpired      = &AuthError{Code: "invalid_token", Description: "Access token has expired"}
	ErrTokenRevoked      = &AuthError{Code: "invalid_token", Description: "Access token has been revoked"}
	ErrTokenNoUser       = &AuthError{Code: "invalid_token", Description: "Token has no user"}
	ErrServerError       = &AuthError{Code: "server_error", Description: "Internal server error"}
	ErrInsufficientScope = &AuthError{Code: "insufficient_scope", Description: "Insufficient scope for this resource"}
)

// AuthError represents an authentication/authorization error.
type AuthError struct {
	Code        string
	Description string
}

func (e *AuthError) Error() string {
	if e.Description != "" {
		return e.Code + ": " + e.Description
	}
	return e.Code
}

// HTTPStatus returns the appropriate HTTP status code for this error.
func (e *AuthError) HTTPStatus() int {
	switch e.Code {
	case "insufficient_scope":
		return http.StatusForbidden
	case "server_error":
		return http.StatusInternalServerError
	default:
		return http.StatusUnauthorized
	}
}

// UseJTI atomically records a JTI and reports whether this was a
// new entry. A false return means the JTI already existed and the
// caller should treat it as a replay.
func UseJTI(ctx context.Context, store JTIStore, jti string, iat int64) (bool, error) {
	return store.InsertIfNew(ctx, &DPoPJTI{
		JTI:       jti,
		CreatedAt: iat,
	})
}

// CleanupExpiredJTIs is a helper to clean up old JTI records.
// Call this periodically to prevent the JTI table from growing indefinitely.
func CleanupExpiredJTIs(ctx context.Context, store interface {
	DeleteOlderThan(ctx context.Context, beforeTimestamp int64) error
}, maxAge time.Duration) error {
	cutoff := time.Now().Add(-maxAge).Unix()
	return store.DeleteOlderThan(ctx, cutoff)
}
