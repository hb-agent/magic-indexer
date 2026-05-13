// Package middleware contains chi-compatible HTTP middlewares used
// by the public, admin, and notifications endpoints. Today this
// package holds only QueryTimeoutMiddleware; older middlewares
// (CORS, security headers, etc.) live one directory up.
package middleware

import (
	"context"
	"net/http"
	"time"
)

// QueryTimeoutMiddleware installs a per-request deadline on the
// request context. The wrapped handler is responsible for detecting
// `r.Context().Err() == context.DeadlineExceeded` and shaping the
// timeout response (set X-Query-Timeout header, replace error body,
// increment metric) BEFORE the response body is written.
//
// Three reviewers independently caught the post-handler approach
// (header.Set / metric.Inc after next.ServeHTTP returns) as broken:
// headers are flushed once the encoder writes the body; the
// post-handler ctx.Err() check races the deadline timer. The
// contract is: this middleware does one thing — install the
// deadline — and the handler owns the response shaping.
//
// timeout is the public-endpoint budget from
// config.GraphQLPublicQueryTimeoutMs. Pass values via
// time.Duration so test setups can construct it directly without
// going through the config layer.
func QueryTimeoutMiddleware(timeout time.Duration) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx, cancel := context.WithTimeout(r.Context(), timeout)
			defer cancel()
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}
