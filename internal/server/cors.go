// Package server provides HTTP handlers and middleware for the Hypergoat server.
package server

import (
	"net/http"
	"net/url"
	"strings"
)

// CORSConfig holds CORS middleware configuration.
type CORSConfig struct {
	// AllowedOrigins is a list of origins that are allowed to make cross-origin requests.
	// If empty, defaults to "*" (all origins allowed — suitable for development only).
	//
	// Each entry may be either an exact origin (e.g. "https://certs.social") or a
	// wildcard pattern containing a single "*" in the hostname (e.g.
	// "https://*.vercel.app"). Wildcards only match within a single DNS label —
	// "https://*.vercel.app" matches "https://foo.vercel.app" and
	// "https://foo-bar.vercel.app" but NOT "https://foo.bar.vercel.app" or
	// "https://vercel.app.attacker.com". The scheme must match exactly; the port
	// (if any) must also match exactly.
	AllowedOrigins []string

	// AllowedHeaders is the list of request headers allowed in CORS requests.
	// "Content-Type" and "Authorization" are always included.
	AllowedHeaders []string

	// AdminAPIKeySet controls whether X-User-DID is included in allowed headers.
	// When true, the admin API key mechanism is active and browsers need to send X-User-DID.
	AdminAPIKeySet bool
}

// originMatcher decides whether a given request Origin header is allowed.
// It is built once at middleware construction time so the per-request hot
// path stays fast: exact matches hit a map, wildcard patterns iterate a
// small slice.
type originMatcher struct {
	exact    map[string]struct{}
	wildcard []wildcardOrigin
}

// wildcardOrigin represents a parsed "https://*.example.com[:port]" pattern.
// It matches by comparing scheme + port exactly and requiring the request
// hostname to end with ".<suffix>" where suffix is the part after "*.".
type wildcardOrigin struct {
	scheme string // "https" or "http"
	// hostSuffix is the host tail AFTER the leading "*.". For a pattern
	// "https://*.vercel.app", hostSuffix is "vercel.app". A request host
	// matches iff it ends with "." + hostSuffix AND the label before the
	// dot contains no further dots (enforced in matches()).
	hostSuffix string
	port       string // "" if no port in the pattern
}

// matches returns true if the given Origin header value is allowed by this
// wildcard entry. Origin is a full origin string like "https://foo.vercel.app".
func (w wildcardOrigin) matches(origin string) bool {
	u, err := url.Parse(origin)
	if err != nil || u.Scheme != w.scheme {
		return false
	}
	if u.Port() != w.port {
		return false
	}
	host := u.Hostname()
	// Require exactly one label in place of "*": host must end with
	// "." + hostSuffix and the prefix before that dot must be a single
	// non-empty label (no dots).
	needle := "." + w.hostSuffix
	if !strings.HasSuffix(host, needle) {
		return false
	}
	prefix := host[:len(host)-len(needle)]
	if prefix == "" || strings.Contains(prefix, ".") {
		return false
	}
	return true
}

// buildOriginMatcher parses the configured origin list into an originMatcher.
// Malformed patterns are silently dropped — callers that care should validate
// input separately.
func buildOriginMatcher(origins []string) *originMatcher {
	m := &originMatcher{exact: make(map[string]struct{}, len(origins))}
	for _, raw := range origins {
		o := strings.TrimSpace(raw)
		if o == "" {
			continue
		}
		if !strings.Contains(o, "*") {
			m.exact[o] = struct{}{}
			continue
		}
		// Parse wildcard pattern. We only support a single "*." immediately
		// after "://" in the hostname component, e.g. "https://*.vercel.app".
		u, err := url.Parse(o)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
			continue
		}
		host := u.Hostname()
		if !strings.HasPrefix(host, "*.") {
			continue
		}
		suffix := host[2:]
		if suffix == "" || strings.Contains(suffix, "*") {
			continue
		}
		m.wildcard = append(m.wildcard, wildcardOrigin{
			scheme:     u.Scheme,
			hostSuffix: suffix,
			port:       u.Port(),
		})
	}
	return m
}

// allow reports whether the given Origin header value is permitted.
func (m *originMatcher) allow(origin string) bool {
	if origin == "" {
		return false
	}
	if _, ok := m.exact[origin]; ok {
		return true
	}
	for _, w := range m.wildcard {
		if w.matches(origin) {
			return true
		}
	}
	return false
}

// CORSMiddleware returns an HTTP middleware that handles CORS headers and preflight requests.
// It uses the configured allowed origins instead of hardcoding "*".
func CORSMiddleware(cfg CORSConfig) func(http.Handler) http.Handler {
	matcher := buildOriginMatcher(cfg.AllowedOrigins)
	allowAll := len(cfg.AllowedOrigins) == 0

	// Build allowed headers
	headers := []string{"Content-Type", "Authorization", "DPoP"}
	headers = append(headers, cfg.AllowedHeaders...)
	if cfg.AdminAPIKeySet {
		headers = append(headers, "X-User-DID")
	}
	allowedHeaders := strings.Join(headers, ", ")

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			origin := r.Header.Get("Origin")

			if allowAll {
				w.Header().Set("Access-Control-Allow-Origin", "*")
			} else if matcher.allow(origin) {
				w.Header().Set("Access-Control-Allow-Origin", origin)
				w.Header().Set("Vary", "Origin")
			}

			w.Header().Set("Access-Control-Allow-Methods", "POST, GET, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", allowedHeaders)
			w.Header().Set("Access-Control-Max-Age", "86400")

			// Handle preflight
			if r.Method == "OPTIONS" {
				w.WriteHeader(http.StatusNoContent)
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}
