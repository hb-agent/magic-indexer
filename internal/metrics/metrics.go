// Package metrics exposes a Prometheus counter / histogram set for
// the parts of the system where production visibility matters most:
// HTTP requests, Jetstream ingestion, labeler ingestion, and record
// writes. The package intentionally keeps the instrumented surface
// small so that adding a new counter requires a deliberate edit, not
// a broad sprinkle.
//
// All metrics live in a package-level DefaultRegistry. Callers use
// the package-level functions — Record…, Observe… — instead of
// touching the registry directly.
//
// The /metrics HTTP handler is registered by cmd/hypergoat/main.go
// and served alongside /health and /stats.
package metrics

import (
	"net/http"
	"strconv"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Registry is the Prometheus registry backing every metric in this
// package. A fresh registry (rather than the global
// prometheus.DefaultRegisterer) keeps the exposed series list small
// and predictable — no Go runtime / process collectors unless we
// explicitly opt in below.
var Registry = prometheus.NewRegistry()

// httpLabels cardinality notes:
//   - method: bounded (GET/POST/…)
//   - path: aggregated to route templates by callers (never the raw URL
//     path — user-controlled cardinality would blow up the series count)
//   - code: bounded to status code strings
var (
	httpRequestsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "hypergoat_http_requests_total",
			Help: "Number of HTTP requests processed, labelled by method, route, and status code.",
		},
		[]string{"method", "route", "code"},
	)

	httpRequestDuration = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "hypergoat_http_request_duration_seconds",
			Help:    "HTTP request duration in seconds, labelled by method and route.",
			Buckets: prometheus.DefBuckets,
		},
		[]string{"method", "route"},
	)

	jetstreamEventsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "hypergoat_jetstream_events_total",
			Help: "Jetstream events received, labelled by collection and operation.",
		},
		[]string{"collection", "operation"},
	)

	jetstreamErrorsTotal = prometheus.NewCounter(
		prometheus.CounterOpts{
			Name: "hypergoat_jetstream_errors_total",
			Help: "Jetstream event handling errors (insert failed, parse failed, etc.).",
		},
	)

	labelerLabelsReceived = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "hypergoat_labeler_labels_received_total",
			Help: "Labels received from an ATProto labeler, labelled by src DID.",
		},
		[]string{"src"},
	)

	labelerLabelsRejected = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "hypergoat_labeler_labels_rejected_total",
			Help: "Labels rejected by validateLabel or ON CONFLICT, labelled by src and reason.",
		},
		[]string{"src", "reason"},
	)

	recordsInsertedTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "hypergoat_records_inserted_total",
			Help: "Records written to the record table, labelled by collection.",
		},
		[]string{"collection"},
	)
)

func init() {
	Registry.MustRegister(
		httpRequestsTotal,
		httpRequestDuration,
		jetstreamEventsTotal,
		jetstreamErrorsTotal,
		labelerLabelsReceived,
		labelerLabelsRejected,
		recordsInsertedTotal,
	)
}

// Handler returns the HTTP handler that serves the Prometheus text
// exposition format. Mount it at /metrics.
func Handler() http.Handler {
	return promhttp.HandlerFor(Registry, promhttp.HandlerOpts{
		Registry:          Registry,
		EnableOpenMetrics: true,
	})
}

// RecordHTTP is called from the HTTP metrics middleware with the
// dispatched chi route pattern (never the raw URL — see cardinality
// notes above) and the response status code.
func RecordHTTP(method, route string, status int, durationSeconds float64) {
	code := httpStatusString(status)
	httpRequestsTotal.WithLabelValues(method, route, code).Inc()
	httpRequestDuration.WithLabelValues(method, route).Observe(durationSeconds)
}

// RecordJetstreamEvent increments the event counter for a commit
// op. Non-commit events (identity/account) are not counted here.
func RecordJetstreamEvent(collection, operation string) {
	jetstreamEventsTotal.WithLabelValues(collection, operation).Inc()
}

// RecordJetstreamError is incremented from the commit error path.
func RecordJetstreamError() {
	jetstreamErrorsTotal.Inc()
}

// RecordLabelReceived is incremented once per label successfully
// persisted from a labeler subscription or backfill.
func RecordLabelReceived(src string) {
	labelerLabelsReceived.WithLabelValues(src).Inc()
}

// RecordLabelRejected is incremented once per label dropped by
// validateLabel (or any other upsert-time rejection). `reason` is
// one of a small fixed set to keep cardinality bounded.
func RecordLabelRejected(src, reason string) {
	labelerLabelsRejected.WithLabelValues(src, reason).Inc()
}

// RecordInserted is incremented once per record row inserted by
// the Jetstream consumer.
func RecordInserted(collection string) {
	recordsInsertedTotal.WithLabelValues(collection).Inc()
}

// httpStatusString converts an int status code into a stable
// label string. Grouping by class (2xx/3xx/4xx/5xx) would cost
// signal; full codes are bounded enough.
func httpStatusString(code int) string {
	if code < 100 || code > 599 {
		return "other"
	}
	return strconv.Itoa(code)
}
