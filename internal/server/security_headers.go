package server

import "net/http"

// SecurityHeadersMiddleware returns an HTTP middleware that adds a
// conservative set of defensive response headers:
//
//   - X-Content-Type-Options: nosniff — prevents browsers from
//     MIME-sniffing JSON into HTML/script.
//   - X-Frame-Options: DENY — blocks clickjacking on any served HTML
//     (GraphiQL UI, admin UI).
//   - Referrer-Policy: no-referrer — avoids leaking URLs to third
//     parties when the admin UI links out.
//   - Strict-Transport-Security — only emitted when httpsOnly is
//     true (i.e., the service is configured to run behind TLS), to
//     avoid sending the header from a plain-http dev instance.
//
// Content-Security-Policy is intentionally omitted here because the
// GraphiQL UI loads its assets from a CDN and a too-strict CSP would
// break it. Operators who need a tighter policy can add it in front
// of this middleware at their reverse proxy.
func SecurityHeadersMiddleware(httpsOnly bool) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			h := w.Header()
			h.Set("X-Content-Type-Options", "nosniff")
			h.Set("X-Frame-Options", "DENY")
			h.Set("Referrer-Policy", "no-referrer")
			if httpsOnly {
				// 1 year, applies to subdomains. Only enable when
				// the operator has confirmed the deployment is
				// HTTPS-only — otherwise an HSTS header from a
				// dev instance on http would break local work.
				h.Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
			}
			next.ServeHTTP(w, r)
		})
	}
}
