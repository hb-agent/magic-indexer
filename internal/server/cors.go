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
	// wildcard pattern containing a single "*" in the leftmost DNS label of the
	// hostname. Supported pattern shapes:
	//
	//   "https://*.vercel.app"               matches "https://foo.vercel.app"
	//   "https://certs-social-*.vercel.app"  matches "https://certs-social-abc.vercel.app"
	//   "https://*-preview.example.com"      matches "https://foo-preview.example.com"
	//
	// The "*" always matches one or more characters within a single DNS label
	// (i.e. no dots). Everything after the leftmost label must match literally.
	// The scheme and port (if present) must match exactly. Patterns like
	// "*.vercel.app" do NOT match "foo.bar.vercel.app" or "vercel.app.attacker.com".
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

// wildcardOrigin represents a parsed wildcard origin pattern. It matches by
// comparing scheme + port exactly, then splitting the request host into
// leftmost label + rest and checking that the rest equals hostSuffix and
// the leftmost label starts with labelPrefix, ends with labelSuffix, and
// has at least one extra character in between (the "*" must match >=1 char).
type wildcardOrigin struct {
	scheme      string // "https" or "http"
	labelPrefix string // literal text before "*" in the leftmost label
	labelSuffix string // literal text after "*" in the leftmost label
	// hostSuffix is the literal host tail AFTER the first dot. For the
	// pattern "https://certs-social-*.vercel.app", hostSuffix is
	// "vercel.app".
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
	// Split the host into leftmost label + rest on the first dot.
	dot := strings.IndexByte(host, '.')
	if dot < 0 {
		return false
	}
	label := host[:dot]
	rest := host[dot+1:]
	if rest != w.hostSuffix {
		return false
	}
	// The leftmost label must contain no dots (by construction since we
	// split on the first dot, this is implicit) and must match
	// labelPrefix + "<non-empty>" + labelSuffix.
	if !strings.HasPrefix(label, w.labelPrefix) || !strings.HasSuffix(label, w.labelSuffix) {
		return false
	}
	// The "*" must match at least one character — ensure the overlap
	// between prefix and suffix doesn't consume the whole label.
	if len(label) <= len(w.labelPrefix)+len(w.labelSuffix) {
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
		// Parse wildcard pattern. We support a single "*" anywhere within
		// the LEFTMOST DNS label, e.g. "https://*.vercel.app" or
		// "https://certs-social-*.vercel.app". The part after the first
		// dot must be literal.
		u, err := url.Parse(o)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
			continue
		}
		host := u.Hostname()
		dot := strings.IndexByte(host, '.')
		if dot < 0 {
			continue // patterns like "https://*" alone are nonsense
		}
		label := host[:dot]
		rest := host[dot+1:]
		if rest == "" || strings.Contains(rest, "*") {
			// The non-leftmost part must be literal.
			continue
		}
		star := strings.IndexByte(label, '*')
		if star < 0 {
			// No "*" in the leftmost label — the wildcard must be
			// elsewhere, which we don't support.
			continue
		}
		if strings.IndexByte(label[star+1:], '*') >= 0 {
			// Multiple "*" in the leftmost label is not supported.
			continue
		}
		m.wildcard = append(m.wildcard, wildcardOrigin{
			scheme:      u.Scheme,
			labelPrefix: label[:star],
			labelSuffix: label[star+1:],
			hostSuffix:  rest,
			port:        u.Port(),
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
