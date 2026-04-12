package metrics

import (
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
)

// Middleware wraps an HTTP handler and records per-request metrics.
// Must be installed *after* chi's RoutePattern middleware (the chi
// default router records it automatically) so the route template —
// not the raw URL — becomes the metric label. If chi.RouteContext
// returns an empty pattern (e.g., 404 path), the label falls back
// to a fixed "unmatched" bucket so we don't explode cardinality.
func Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rw := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rw, r)

		route := chi.RouteContext(r.Context()).RoutePattern()
		if route == "" {
			route = "unmatched"
		}
		RecordHTTP(r.Method, route, rw.status, time.Since(start).Seconds())
	})
}

// statusRecorder captures the status code without buffering the body.
type statusRecorder struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
}

func (r *statusRecorder) WriteHeader(code int) {
	if !r.wroteHeader {
		r.status = code
		r.wroteHeader = true
	}
	r.ResponseWriter.WriteHeader(code)
}

func (r *statusRecorder) Write(b []byte) (int, error) {
	if !r.wroteHeader {
		r.wroteHeader = true
	}
	return r.ResponseWriter.Write(b)
}
