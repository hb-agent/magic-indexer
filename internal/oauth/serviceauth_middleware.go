package oauth

import (
	"context"
	"log/slog"
	"net/http"
	"strings"

	"github.com/GainForest/hypergoat/internal/metrics"
)

// serviceAuthDIDKeyType is a package-private key type so callers cannot
// accidentally read/write from the same context slot with a raw string.
type serviceAuthDIDKeyType struct{}

var serviceAuthDIDKey = serviceAuthDIDKeyType{}

// ActingDIDFromContext returns the DID injected by ServiceAuthMiddleware
// on successful verification. The second return is false when the
// context does not carry a verified DID — handlers MUST fail closed in
// that case (defence-in-depth in case the middleware is ever misrouted).
func ActingDIDFromContext(ctx context.Context) (string, bool) {
	did, ok := ctx.Value(serviceAuthDIDKey).(string)
	return did, ok && did != ""
}

// ServiceAuthMiddleware returns a chi-compatible middleware that verifies
// an ATProto service-auth JWT from the Authorization header and injects
// the issuer DID into the request context. `expectedLxm` pins the
// accepted lxm claim per endpoint — callers mount one middleware per
// lxm so a token for `list` can't be replayed on `updateSeen`.
//
// 401 responses include `WWW-Authenticate: Bearer` per RFC 6750 but
// intentionally do not include an `error_description` — enumeration of
// internal failure modes is more useful to an attacker than a legitimate
// caller (who already sees the token in their own logs).
func ServiceAuthMiddleware(verifier *ServiceAuthVerifier, expectedLxm string) func(http.Handler) http.Handler {
	if verifier == nil {
		panic("ServiceAuthMiddleware: verifier must be non-nil")
	}
	if expectedLxm == "" {
		panic("ServiceAuthMiddleware: expectedLxm must be non-empty")
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			token, err := extractBearer(r)
			if err != nil {
				rejectServiceAuth(w, err, expectedLxm, "invalid_request")
				return
			}
			did, err := verifier.Verify(r.Context(), token, expectedLxm)
			if err != nil {
				rejectServiceAuth(w, err, expectedLxm, "invalid_token")
				return
			}
			metrics.ServiceAuthVerified(expectedLxm)
			ctx := context.WithValue(r.Context(), serviceAuthDIDKey, did)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

func extractBearer(r *http.Request) (string, error) {
	h := r.Header.Get("Authorization")
	if h == "" {
		return "", ErrServiceAuthMissingHeader
	}
	// Scheme name is case-insensitive per RFC 6750 §2.1.
	if len(h) < 7 || !strings.EqualFold(h[:7], "Bearer ") {
		return "", ErrServiceAuthMalformedHeader
	}
	token := strings.TrimSpace(h[7:])
	if token == "" {
		return "", ErrServiceAuthMalformedHeader
	}
	return token, nil
}

func rejectServiceAuth(w http.ResponseWriter, err error, lxm, oauthErr string) {
	reason := ReasonFor(err)
	metrics.ServiceAuthRejected(reason, lxm)
	// Warn-level: operators need to see the underlying cause on a 401
	// spike. The metric label is bounded; the slog message carries the
	// wrapped error for triage.
	slog.Warn("service-auth rejected", "reason", reason, "lxm", lxm, "error", err.Error())
	w.Header().Set("WWW-Authenticate", `Bearer error="`+oauthErr+`"`)
	w.WriteHeader(http.StatusUnauthorized)
}
