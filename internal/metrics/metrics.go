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

	recordsAuthorsFilterAppliedTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "hypergoat_records_authors_filter_applied_total",
			Help: "Number of record queries where an authors filter was applied, labelled by collection.",
		},
		[]string{"collection"},
	)

	recordsAuthorsFilterSize = prometheus.NewHistogram(
		prometheus.HistogramOpts{
			Name:    "hypergoat_records_authors_filter_size",
			Help:    "Number of DIDs in the authors filter per query.",
			Buckets: []float64{1, 5, 10, 25, 50, 100, 250, 500},
		},
	)

	recordsAuthorsFilterEmptyBlockedTotal = prometheus.NewCounter(
		prometheus.CounterOpts{
			Name: "hypergoat_records_authors_filter_empty_blocked_total",
			Help: "Number of record queries blocked because authors was an explicit empty list.",
		},
	)

	recordsAuthorsFilterTooLargeTotal = prometheus.NewCounter(
		prometheus.CounterOpts{
			Name: "hypergoat_records_authors_filter_too_large_total",
			Help: "Number of record queries rejected because authors exceeded the maximum size.",
		},
	)

	recordsSearchAppliedTotal = prometheus.NewCounter(
		prometheus.CounterOpts{
			Name: "hypergoat_records_search_applied_total",
			Help: "Number of record queries where a full-text search filter was applied.",
		},
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
		recordsAuthorsFilterAppliedTotal,
		recordsAuthorsFilterSize,
		recordsAuthorsFilterEmptyBlockedTotal,
		recordsAuthorsFilterTooLargeTotal,
		recordsSearchAppliedTotal,
		recordValidationFailedTotal,
		oauthRefreshJKTMismatchTotal,
		oauthRefreshLegacyNullJKTTotal,
		oauthRefreshLegacyExpiredTotal,
		serviceAuthVerifiedTotal,
		serviceAuthRejectedTotal,
		serviceAuthDIDResolveServedStaleTotal,
		notificationsRequestTotal,
		pdsResolveTotal,
		contributorIdentityTotal,
		graphqlQueryTimeoutTotal,
		purgeTokenRejectedTotal,
		purgeActorTotal,
		purgeRecordsDeleted,
		adminSettingsChangedTotal,
		resetAllTotal,
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

// RecordAuthorsFilterApplied is incremented when a record query uses an
// authors filter. `size` is the number of DIDs in the filter.
func RecordAuthorsFilterApplied(collection string, size int) {
	recordsAuthorsFilterAppliedTotal.WithLabelValues(collection).Inc()
	recordsAuthorsFilterSize.Observe(float64(size))
}

// RecordAuthorsFilterEmptyBlocked is incremented when a record query is
// short-circuited because the authors list was explicitly empty.
func RecordAuthorsFilterEmptyBlocked() {
	recordsAuthorsFilterEmptyBlockedTotal.Inc()
}

// RecordAuthorsFilterTooLarge is incremented when a record query is
// rejected because the authors list exceeded MaxAuthorsFilterSize.
func RecordAuthorsFilterTooLarge() {
	recordsAuthorsFilterTooLargeTotal.Inc()
}

// RecordSearchApplied is incremented when a record query uses a
// full-text search filter.
func RecordSearchApplied() {
	recordsSearchAppliedTotal.Inc()
}

// --- Record validation metrics ---

var recordValidationFailedTotal = prometheus.NewCounterVec(
	prometheus.CounterOpts{
		Name: "hypergoat_record_validation_failed_total",
		Help: "Records that failed lexicon validation at ingestion time.",
	},
	[]string{"collection"},
)

// RecordValidationFailed increments the validation failure counter.
func RecordValidationFailed(collection string) {
	recordValidationFailedTotal.WithLabelValues(collection).Inc()
}

// --- OAuth refresh token DPoP binding metrics (issue #24) ---

var (
	oauthRefreshJKTMismatchTotal = prometheus.NewCounter(
		prometheus.CounterOpts{
			Name: "hypergoat_oauth_refresh_jkt_mismatch_total",
			Help: "Refresh requests rejected because the DPoP proof JKT does not match the refresh token's bound JKT.",
		},
	)
	oauthRefreshLegacyNullJKTTotal = prometheus.NewCounter(
		prometheus.CounterOpts{
			Name: "hypergoat_oauth_refresh_legacy_null_jkt_total",
			Help: "Refresh requests served for a legacy (unbound) refresh token inside the sunset window.",
		},
	)
	oauthRefreshLegacyExpiredTotal = prometheus.NewCounter(
		prometheus.CounterOpts{
			Name: "hypergoat_oauth_refresh_legacy_expired_total",
			Help: "Refresh requests rejected for a legacy (unbound) refresh token past the sunset cutoff.",
		},
	)
)

// OAuthRefreshJKTMismatch increments when a refresh attempt presents a DPoP
// proof whose JKT does not match the refresh token's bound JKT (or when the
// proof is missing entirely on a bound token).
func OAuthRefreshJKTMismatch() {
	oauthRefreshJKTMismatchTotal.Inc()
}

// OAuthRefreshLegacyNullJKT increments when a legacy (unbound) refresh token
// is accepted under the sunset window. Watch this go to zero before removing
// the legacy path.
func OAuthRefreshLegacyNullJKT() {
	oauthRefreshLegacyNullJKTTotal.Inc()
}

// OAuthRefreshLegacyExpired increments when a legacy (unbound) refresh token
// is rejected because it was issued after the LegacyDPoPJKTCutoff.
func OAuthRefreshLegacyExpired() {
	oauthRefreshLegacyExpiredTotal.Inc()
}

// --- Service-auth JWT metrics (issue #57) ---

var (
	serviceAuthVerifiedTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "hypergoat_service_auth_verified_total",
			Help: "Service-auth JWT verifications that succeeded, labelled by lxm.",
		},
		[]string{"lxm"},
	)
	serviceAuthRejectedTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "hypergoat_service_auth_rejected_total",
			Help: "Service-auth JWT verifications that were rejected, labelled by reason and lxm.",
		},
		[]string{"reason", "lxm"},
	)
	serviceAuthDIDResolveServedStaleTotal = prometheus.NewCounter(
		prometheus.CounterOpts{
			Name: "hypergoat_did_resolve_served_stale_total",
			Help: "Number of service-auth verifies that proceeded with a stale-but-cached DID document because PLC was unavailable.",
		},
	)
	notificationsRequestTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "hypergoat_notifications_request_total",
			Help: "Notification GraphQL requests, labelled by endpoint (admin vs xrpc) and field. Used to gate removal of the admin-key path.",
		},
		[]string{"endpoint", "field"},
	)
)

// ServiceAuthVerified increments the success counter. Only called after
// full signature + claim + replay validation.
func ServiceAuthVerified(lxm string) {
	serviceAuthVerifiedTotal.WithLabelValues(lxm).Inc()
}

// ServiceAuthRejected increments the rejection counter. `lxm` may be
// "unknown" for rejections that happen before claims parse (missing
// header, malformed header, size-cap). Reason labels are bounded to the
// sentinel-error set in internal/oauth/serviceauth_errors.go.
func ServiceAuthRejected(reason, lxm string) {
	serviceAuthRejectedTotal.WithLabelValues(reason, lxm).Inc()
}

// DIDResolveServedStale increments when the resolver fell back to a
// recently-expired cache entry because the upstream (PLC or did:web)
// was unreachable. Alert if this is non-zero for more than 5 minutes —
// it means the indexer is authenticating requests against keys that
// could have rotated.
func DIDResolveServedStale() {
	serviceAuthDIDResolveServedStaleTotal.Inc()
}

// NotificationsRequest increments per handled notifications GraphQL op,
// split by auth path (`admin` vs `xrpc`) and field. Primary purpose:
// prove the admin-key path has zero traffic before deleting it.
func NotificationsRequest(endpoint, field string) {
	notificationsRequestTotal.WithLabelValues(endpoint, field).Inc()
}

// --- PDS resolution metrics (issue maearth-social#10) ---
//
// These count the outcome of resolving an actor's DID document to
// the AtprotoPersonalDataServer service endpoint at ingestion time.
// The resolved value flows into actor.pds and underpins the
// excludePds GraphQL filter, so the rate of "no_endpoint" or
// "failed" tells operators how leaky the filter is in steady state.

var (
	pdsResolveTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "hypergoat_pds_resolve_total",
			Help: "Outcomes of PDS resolution at actor upsert time. Outcome labels: ok, failed (resolver returned an error), no_endpoint (DID document had no AtprotoPersonalDataServer entry).",
		},
		[]string{"outcome"},
	)
)

// PDSResolveOK increments the ok-outcome counter for PDS resolution.
func PDSResolveOK() {
	pdsResolveTotal.WithLabelValues("ok").Inc()
}

// PDSResolveFailed increments the failed-outcome counter (resolver
// returned an error: PLC outage, network, document parse failure).
func PDSResolveFailed() {
	pdsResolveTotal.WithLabelValues("failed").Inc()
}

// PDSResolveNoEndpoint increments the no-endpoint-outcome counter
// (resolution succeeded but the DID document had no
// AtprotoPersonalDataServer service entry).
func PDSResolveNoEndpoint() {
	pdsResolveTotal.WithLabelValues("no_endpoint").Inc()
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

// contributor_identity_total — outcomes of contributorIdentity
// extraction at ingest time on org.hypercerts.claim.activity
// records. The indexer's policy is "read the value only when it is
// a DID"; this counter surfaces producer drift so operators can
// nudge upstream writers that are still emitting handles or
// unrecognised shapes. Mirrors the pds_resolve_total shape above.
//
// Outcomes:
//   - did                 — value resolved to a valid DID
//   - non_did             — value was a string (bare or object
//                            .identity) but did not pass did.IsValid
//   - unrecognized_shape  — value was neither a string nor an object
//                            with a string .identity field, or the
//                            contributorIdentity field was absent.

var (
	contributorIdentityTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "hypergoat_contributor_identity_total",
			Help: "Outcomes of contributorIdentity extraction at ingest. Outcome labels: did, non_did, unrecognized_shape.",
		},
		[]string{"outcome"},
	)
)

// ContributorIdentityDID records a contributor whose identity
// resolved to a valid DID.
func ContributorIdentityDID() {
	contributorIdentityTotal.WithLabelValues("did").Inc()
}

// ContributorIdentityNonDID records a contributor whose identity
// was a string but did not pass strict DID validation (e.g. a
// handle).
func ContributorIdentityNonDID() {
	contributorIdentityTotal.WithLabelValues("non_did").Inc()
}

// ContributorIdentityUnrecognizedShape records a contributor whose
// identity was neither a string nor an object with a string
// .identity field. A rising trend here is the operator's signal
// that producers may be shipping strong-ref contributor identities.
func ContributorIdentityUnrecognizedShape() {
	contributorIdentityTotal.WithLabelValues("unrecognized_shape").Inc()
}

// graphql_query_timeout_total — public GraphQL requests that
// exceeded the per-request deadline (issue #71's Layer 2). Today
// the only route emitting this label is `public`; admin and
// subscription paths are not bounded by the per-request
// middleware. Mirrors the pds_resolve_total shape.
var (
	graphqlQueryTimeoutTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "hypergoat_graphql_query_timeout_total",
			Help: "GraphQL requests that exceeded the per-request deadline. Route labels: public.",
		},
		[]string{"route"},
	)
)

// GraphQLQueryTimeout records a per-request deadline-exceeded
// outcome on the given route. Today only "public" is emitted; the
// label argument keeps the surface open without invariably opening
// it to user input — every call site uses a hardcoded string
// constant.
func GraphQLQueryTimeout(route string) {
	graphqlQueryTimeoutTotal.WithLabelValues(route).Inc()
}

// --- Admin destructive-op metrics (T-OBS-1, T-OBS-2) ---
//
// PurgeActor (one actor) and ResetAll (whole instance) are the two
// destructive admin mutations. Without these counters there is no
// alertable signal on token-forgery attempts or sustained purge
// volume. Reason labels for token rejection are bounded to a
// hard-listed set — never err.Error() — so cardinality stays
// flat under hostile input.
//
// PurgeReason* constants are the only values that may flow into the
// `reason` label of hypergoat_purge_token_rejected_total. They map
// 1:1 to the sentinel errors in internal/graphql/admin/purge.go
// plus two failure modes that the resolver detects before reaching
// the signer (wrong_admin, wrong_target — kept for symmetry with
// the report's recommended label set even though Verify maps them
// to ErrPurgeTokenInvalid today).
const (
	PurgeReasonInvalid       = "invalid"
	PurgeReasonExpired       = "expired"
	PurgeReasonAlreadyUsed   = "already_used"
	PurgeReasonWrongAdmin    = "wrong_admin"
	PurgeReasonCountDrift    = "count_drift"
	PurgeReasonWrongTarget   = "wrong_target"
	PurgeReasonScopeMismatch = "scope_mismatch"
)

// PurgeTapStatus* are the legal values for the `tap_status` label
// on hypergoat_purge_actor_total. Mirror the strings the resolver
// already returns via the GraphQL response.
const (
	PurgeTapStatusRemoved = "removed"
	PurgeTapStatusFailed  = "failed"
	PurgeTapStatusSkipped = "skipped"
)

// AdminSettingsField* are the legal values for the `field` label
// on hypergoat_admin_settings_changed_total. Every UpdateSettings
// branch + AddAdmin / RemoveAdmin uses one of these constants;
// never a runtime string.
const (
	AdminSettingsFieldDomainAuthority      = "domainAuthority"
	AdminSettingsFieldRelayURL             = "relayUrl"
	AdminSettingsFieldPLCDirectoryURL      = "plcDirectoryUrl"
	AdminSettingsFieldJetstreamURL         = "jetstreamUrl"
	AdminSettingsFieldOAuthSupportedScopes = "oauthSupportedScopes"
	AdminSettingsFieldAdminDIDs            = "adminDids"
)

var (
	purgeTokenRejectedTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "hypergoat_purge_token_rejected_total",
			Help: "PurgeTokenSigner.Verify rejections, labelled by reason. Reasons: invalid, expired, already_used, wrong_admin, count_drift, wrong_target, scope_mismatch. Cardinality is bounded by a hard-listed sentinel set; no err.Error() values flow into the label.",
		},
		[]string{"reason"},
	)

	purgeActorTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "hypergoat_purge_actor_total",
			Help: "Successful purgeActor mutations, labelled by best-effort tap removal outcome. tap_status values: removed, failed, skipped.",
		},
		[]string{"tap_status"},
	)

	purgeRecordsDeleted = prometheus.NewHistogram(
		prometheus.HistogramOpts{
			Name:    "hypergoat_purge_records_deleted",
			Help:    "Distribution of records deleted per successful purgeActor mutation.",
			Buckets: []float64{0, 1, 10, 100, 1000, 10000, 100000, 1000000},
		},
	)

	adminSettingsChangedTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "hypergoat_admin_settings_changed_total",
			Help: "Admin settings mutations applied, labelled by field. Field values: domainAuthority, relayUrl, plcDirectoryUrl, jetstreamUrl, oauthSupportedScopes, adminDids. adminDids is incremented by AddAdmin / RemoveAdmin too.",
		},
		[]string{"field"},
	)

	resetAllTotal = prometheus.NewCounter(
		prometheus.CounterOpts{
			Name: "hypergoat_reset_all_total",
			Help: "Successful resetAll admin mutations. No labels — the operation has no per-target dimensions and a successful reset wipes the whole instance.",
		},
	)
)

// PurgeTokenRejected increments the rejection counter under a
// bounded reason label. Pass one of the PurgeReason* constants —
// never err.Error().
func PurgeTokenRejected(reason string) {
	purgeTokenRejectedTotal.WithLabelValues(reason).Inc()
}

// PurgeActorCompleted increments the success counter for purgeActor
// and observes the records-deleted distribution. tapStatus must be
// one of the PurgeTapStatus* constants.
func PurgeActorCompleted(tapStatus string, recordsDeleted int64) {
	purgeActorTotal.WithLabelValues(tapStatus).Inc()
	purgeRecordsDeleted.Observe(float64(recordsDeleted))
}

// AdminSettingsChanged increments the per-field settings-mutation
// counter. field must be one of the AdminSettingsField* constants.
func AdminSettingsChanged(field string) {
	adminSettingsChangedTotal.WithLabelValues(field).Inc()
}

// ResetAllCompleted increments the resetAll success counter. No
// labels — the operation has no per-target dimensions.
func ResetAllCompleted() {
	resetAllTotal.Inc()
}
