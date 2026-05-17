package admin

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	didpkg "github.com/GainForest/hypergoat/internal/atproto/did"
	"github.com/GainForest/hypergoat/internal/logsafe"
	"github.com/GainForest/hypergoat/internal/metrics"
)

// purgeTokenTTL bounds how long a preview-confirm token remains
// valid before the admin must re-issue the preview. Five minutes is
// long enough for a human operator to read a multi-thousand-row
// preview against external state (a takedown ticket, GDPR request,
// the indexer's own GraphiQL) without being punished for it; short
// enough that an abandoned browser tab cannot be replayed an hour
// later. Applies to both the actor-purge and reset-all scopes.
const purgeTokenTTL = 5 * time.Minute

// Token scopes are explicit strings on the wire so an actor-purge
// token can never be redeemed as a reset-all token (or vice versa)
// even if an attacker can produce HMAC collisions on an empty scope
// field. The strings are stable; do not rename without a version
// bump.
const (
	// ScopeActorPurge is the scope claim for previewPurgeActor /
	// purgeActor tokens. The token binds to (admin_did, target_did,
	// record_count, exp).
	ScopeActorPurge = "actor_purge"

	// ScopeResetAll is the scope claim for previewResetAll /
	// resetAll tokens. The token binds to (admin_did, total_rows,
	// exp). TargetDID is empty for this scope — the operation has
	// no single target.
	ScopeResetAll = "reset_all"
)

// PurgeTokenSigner mints and verifies HMAC-signed confirm tokens for
// admin destructive-op mutations. A token is a base64url-encoded
// payload + HMAC, bound to the requesting admin DID, a scope-specific
// secondary identifier (target DID for actor_purge, empty for
// reset_all), the row count at preview time, the scope string, and
// an expiry. Verification rejects any mismatch (different admin,
// different target, count drift, expired, different scope) via
// constant-time compare on the HMAC.
//
// Single-use is enforced server-side via an in-memory used-token
// set keyed by the signature. The set is cleaned up lazily on each
// verify call and proactively by a periodic sweeper goroutine.
// Restarts clear the set but the exp claim prevents replays beyond
// purgeTokenTTL anyway.
//
// Tokens are stateless from the server's perspective — verification
// re-derives the HMAC from claims, so a single replica restart
// mid-flow does not invalidate a not-yet-redeemed token.
//
// The signer is named "Purge" historically; both supported scopes
// purge data (one actor or the whole index). If a third scope
// without "purge" semantics is added, rename to ScopedTokenSigner.
type PurgeTokenSigner struct {
	secret []byte
	now    func() time.Time // injected for tests

	mu       sync.Mutex
	usedSigs map[string]time.Time // signature → exp (lazy + periodic prune)

	// stopSweeper, when set, halts the periodic prune goroutine
	// (see startSweeper). Closed by Close().
	stopSweeper chan struct{}
}

// maxUsedSigs caps the in-memory used-token set. Under hostile
// preview-spam an attacker with API-key access could otherwise grow
// the set unbounded (Verify only prunes on successful match; failed
// verifies bail at the HMAC check). Cap is high enough that legitimate
// admin traffic never hits it: at 1 preview/sec × 300s TTL = 300
// entries steady-state. Cap of 4096 gives 13× headroom.
const maxUsedSigs = 4096

// NewPurgeTokenSigner returns a signer keyed by the application's
// SECRET_KEY_BASE. The secret must be non-empty; an empty secret
// would let any caller forge tokens and is treated as a configuration
// error.
func NewPurgeTokenSigner(secret []byte) (*PurgeTokenSigner, error) {
	if len(secret) == 0 {
		return nil, errors.New("purge token signer: empty secret")
	}
	s := &PurgeTokenSigner{
		secret:      secret,
		now:         time.Now,
		usedSigs:    make(map[string]time.Time),
		stopSweeper: make(chan struct{}),
	}
	go s.runSweeper()
	return s, nil
}

// runSweeper periodically prunes expired entries from usedSigs so
// Verify doesn't pay an O(N) lazy-prune cost on every call. Stops
// when Close() closes stopSweeper.
func (s *PurgeTokenSigner) runSweeper() {
	t := time.NewTicker(purgeTokenTTL)
	defer t.Stop()
	for {
		select {
		case <-s.stopSweeper:
			return
		case <-t.C:
			s.sweepExpired()
		}
	}
}

// sweepExpired drops entries whose Exp has passed. Also enforces the
// maxUsedSigs cap by dropping the oldest entries when over: a hostile
// caller cannot grow the map unbounded.
func (s *PurgeTokenSigner) sweepExpired() {
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()
	for k, exp := range s.usedSigs {
		if now.Unix() >= exp.Unix() {
			delete(s.usedSigs, k)
		}
	}
	// Belt-and-braces cap. Map iteration order is randomised, so
	// "oldest" here is "whatever the runtime gives us first." Good
	// enough — the goal is to bound memory, not to preserve any
	// particular eviction policy.
	if len(s.usedSigs) > maxUsedSigs {
		toRemove := len(s.usedSigs) - maxUsedSigs
		for k := range s.usedSigs {
			if toRemove <= 0 {
				break
			}
			delete(s.usedSigs, k)
			toRemove--
		}
	}
}

// Close halts the periodic sweeper goroutine. Idempotent.
func (s *PurgeTokenSigner) Close() {
	select {
	case <-s.stopSweeper:
		// already closed
	default:
		close(s.stopSweeper)
	}
}

// purgeTokenClaims is the signed payload. JSON shape kept stable
// because the signature is over its serialized bytes.
//
// The `V` (version) field exists so a future addition of a claim (a
// `Reason` field for SECURITY.md log correlation, say) can be rolled
// out without silently invalidating every outstanding token at
// deploy. Verify rejects any token whose `v` doesn't match the
// expected purgeTokenVersion. The Scope field (added in v2)
// disambiguates actor_purge from reset_all so an admin's
// actor_purge token cannot be redeemed against the resetAll
// mutation by an attacker who somehow captures it.
type purgeTokenClaims struct {
	V           int    `json:"v"`
	Scope       string `json:"scope"`
	AdminDID    string `json:"admin_did"`
	TargetDID   string `json:"target_did"`
	RecordCount int64  `json:"record_count"`
	Exp         int64  `json:"exp"` // unix seconds
}

// purgeTokenVersion is the current claim shape. Bump when adding
// or removing a field; old-version tokens are rejected as
// ErrPurgeTokenInvalid (one TTL of disruption on deploy, no
// silent verification failures).
//
// v2 added the Scope field (Track 3 in the 2026-05-13 review
// follow-up). v1 tokens — which lack a Scope claim — are rejected
// at deploy; admins re-preview and continue.
const purgeTokenVersion = 2

// Sign issues a fresh token for the given (admin, target, count,
// scope) tuple. Returns the token and its expiry timestamp so the
// resolver can echo "expires in M:SS" to the client. scope must be
// one of the package-level Scope* constants.
func (s *PurgeTokenSigner) Sign(scope, adminDID, targetDID string, recordCount int64) (string, time.Time, error) {
	exp := s.now().Add(purgeTokenTTL)
	claims := purgeTokenClaims{
		V:           purgeTokenVersion,
		Scope:       scope,
		AdminDID:    adminDID,
		TargetDID:   targetDID,
		RecordCount: recordCount,
		Exp:         exp.Unix(),
	}
	payload, err := json.Marshal(claims)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("marshal claims: %w", err)
	}
	sig := s.hmac(payload)
	token := base64.RawURLEncoding.EncodeToString(payload) + "." + base64.RawURLEncoding.EncodeToString(sig)
	return token, exp, nil
}

// Sentinel errors returned by Verify so the resolver can map them
// to stable GraphQL error codes for the client UI.
//
// ErrPurgeTokenCountDrift is split from ErrPurgeTokenInvalid because
// it represents a benign race (a record was ingested between preview
// and confirm), not an attack. Forensic separation matters: a burst
// of CountDrift is "operator is racing ingest," while a burst of
// Invalid is "someone is trying to forge tokens."
var (
	ErrPurgeTokenInvalid     = errors.New("purge_token_invalid")
	ErrPurgeTokenExpired     = errors.New("purge_token_expired")
	ErrPurgeTokenAlreadyUsed = errors.New("purge_token_already_used")
	ErrPurgeTokenCountDrift  = errors.New("purge_token_count_drift")
)

// Verify confirms the token is well-formed, signed by us, and bound
// to the given (scope, admin, target, count) tuple, then marks the
// signature as consumed. Returns ErrPurgeTokenInvalid /
// ErrPurgeTokenExpired / ErrPurgeTokenAlreadyUsed for the three
// distinct rejection modes the UI cares about; anything else
// collapses to ErrPurgeTokenInvalid. A token minted for one scope
// (actor_purge) is rejected when verified under a different scope
// (reset_all) so the two surfaces cannot cross-redeem.
//
// For metric labelling resolvers should call VerifyReason instead;
// Verify discards the more granular reason.
func (s *PurgeTokenSigner) Verify(token, scope, adminDID, targetDID string, recordCount int64) error {
	_, err := s.VerifyReason(token, scope, adminDID, targetDID, recordCount)
	return err
}

// VerifyReason is Verify plus a bounded reason string suitable for
// use as a Prometheus label. Reasons map 1:1 to the
// metrics.PurgeReason* sentinels:
//
//	"invalid"          — malformed / HMAC fail / wrong version / unbind admin / unbind target DID
//	"wrong_admin"      — admin DID mismatch (more specific than "invalid")
//	"wrong_target"     — target DID mismatch (more specific than "invalid")
//	"scope_mismatch"   — token minted for a different scope
//	"count_drift"      — record count differs from preview time
//	"expired"          — past Exp
//	"already_used"     — single-use violation
//
// On success the returned reason is "". The error contract is
// identical to Verify so existing callers can continue to use
// errors.Is on the sentinel set.
//
// Reason granularity is deliberately tighter than the error set —
// wrong_admin and wrong_target both collapse to ErrPurgeTokenInvalid
// (the UI doesn't need to distinguish; an operator typing the
// wrong DID is observed as the same "invalid token" experience as a
// forge attempt) but the metric labels do distinguish so an alert
// on `wrong_admin` traffic can fire on "admin A trying to redeem
// admin B's token" without the noise of every random forge attempt.
func (s *PurgeTokenSigner) VerifyReason(token, scope, adminDID, targetDID string, recordCount int64) (string, error) {
	// Split into payload + signature halves before doing any
	// HMAC work — a malformed token shouldn't get past parsing.
	parts := strings.SplitN(token, ".", 2)
	if len(parts) != 2 {
		return "invalid", ErrPurgeTokenInvalid
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return "invalid", ErrPurgeTokenInvalid
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "invalid", ErrPurgeTokenInvalid
	}

	// Constant-time HMAC compare. Any payload-tampering shows
	// up as a mismatch here.
	want := s.hmac(payload)
	if subtle.ConstantTimeCompare(want, sig) != 1 {
		return "invalid", ErrPurgeTokenInvalid
	}

	// Only deserialize after the HMAC has matched — payload
	// is now trusted to be ours.
	var claims purgeTokenClaims
	if err := json.Unmarshal(payload, &claims); err != nil {
		return "invalid", ErrPurgeTokenInvalid
	}

	// Reject tokens minted by a previous version of the claim
	// schema. See purgeTokenVersion docs.
	if claims.V != purgeTokenVersion {
		return "invalid", ErrPurgeTokenInvalid
	}

	// Scope binding. A token minted for actor_purge cannot be
	// verified against reset_all and vice versa. Belt-and-braces
	// with the HMAC (which already covers the scope bytes via
	// the payload), but having the resolver pass an explicit
	// expected scope makes the destructive-op gate self-evident
	// at the call site.
	if claims.Scope != scope {
		return "scope_mismatch", ErrPurgeTokenInvalid
	}

	// Bind check: admin DID and target DID must match the values
	// claimed at preview time. Without these, admin A's token
	// could be replayed by admin B or across a different DID.
	// For reset_all both sides pass the empty TargetDID; the
	// equality check holds.
	if claims.AdminDID != adminDID {
		return "wrong_admin", ErrPurgeTokenInvalid
	}
	if claims.TargetDID != targetDID {
		return "wrong_target", ErrPurgeTokenInvalid
	}
	// Record count drift is a separate sentinel — see
	// ErrPurgeTokenCountDrift docs.
	if claims.RecordCount != recordCount {
		return "count_drift", ErrPurgeTokenCountDrift
	}

	now := s.now()
	if now.Unix() >= claims.Exp {
		return "expired", ErrPurgeTokenExpired
	}

	// Single-use check + lazy prune.
	sigKey := parts[1]
	s.mu.Lock()
	defer s.mu.Unlock()
	for k, exp := range s.usedSigs {
		if now.Unix() >= exp.Unix() {
			delete(s.usedSigs, k)
		}
	}
	if _, used := s.usedSigs[sigKey]; used {
		return "already_used", ErrPurgeTokenAlreadyUsed
	}
	s.usedSigs[sigKey] = time.Unix(claims.Exp, 0)
	return "", nil
}

func (s *PurgeTokenSigner) hmac(payload []byte) []byte {
	m := hmac.New(sha256.New, s.secret)
	m.Write(payload)
	return m.Sum(nil)
}

// TapRemover is the minimal subset of *tap.AdminClient the resolver
// needs for best-effort post-commit cleanup. Defined as an interface
// so tests can substitute a stub and so the resolver isn't forced
// to pull in the tap package directly.
type TapRemover interface {
	RemoveRepos(ctx context.Context, dids []string) error
}

// SetPurgeTokenSigner wires the HMAC signer used by previewPurgeActor /
// purgeActor. Mutation is disabled until this is set (the resolver
// returns a clear "purge not configured" error).
func (r *Resolver) SetPurgeTokenSigner(s *PurgeTokenSigner) {
	r.purgeTokenSigner = s
}

// SetTapRemover wires an optional Tap admin client for best-effort
// cleanup after a successful purge. Leaving this unset (TAP_ENABLED=false)
// is fine — the resolver no-ops the Tap leg.
func (r *Resolver) SetTapRemover(t TapRemover) {
	r.tapRemover = t
}

// PreviewPurgeActor materializes the preview the operator confirms
// against and returns a freshly-signed token they hand back to
// PurgeActor. The record count baked into the token is what the
// signer will verify against at confirm time — count drift between
// preview and purge is a deliberate signal to re-preview, not to
// race ahead.
func (r *Resolver) PreviewPurgeActor(ctx context.Context, did string) (map[string]interface{}, error) {
	if err := requireAdmin(ctx); err != nil {
		return nil, err
	}
	if r.purgeTokenSigner == nil {
		return nil, errors.New("purge mutation is not configured")
	}
	// Strict DID validation: the target DID flows into the HMAC payload,
	// into the SQL DELETE param, into the structured audit log line, and
	// into the Tap removal HTTP call. Newline / control chars in the
	// input would forge audit-log lines or split CSV values downstream.
	// The same discipline #64 (commit c069afa) applied to contributor /
	// settings paths must apply here too — destructive ops especially.
	if !didpkg.IsValid(did) {
		return nil, errors.New("invalid DID")
	}

	adminDID, _ := ctx.Value(contextKeyUserDID).(string)
	if adminDID == "" {
		return nil, errors.New("admin DID missing from context")
	}

	actor, err := r.repos.Actors.GetByDID(ctx, did)
	if err != nil {
		// "Actor not in the index" is a legitimate purge target —
		// there may be records authored by the DID before the
		// actor row was indexed, and the operator wants those
		// gone too. But any OTHER DB error (connection dead,
		// scan failure, statement-timeout fire) must not be
		// collapsed into "actor absent": the operator would see
		// `actorExists=false` and proceed against a phantom zero
		// count. Distinguish via errors.Is(sql.ErrNoRows).
		if !errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("get actor: %w", err)
		}
		actor = nil
	}

	recordCount, err := r.repos.Records.CountByDID(ctx, did)
	if err != nil {
		return nil, fmt.Errorf("count records: %w", err)
	}

	token, exp, err := r.purgeTokenSigner.Sign(ScopeActorPurge, adminDID, did, recordCount)
	if err != nil {
		return nil, fmt.Errorf("sign purge token: %w", err)
	}

	out := map[string]interface{}{
		"did":             did,
		"recordCount":     recordCount,
		"confirmToken":    token,
		"tokenExpiresAt":  exp.UTC().Format(time.RFC3339),
		"tokenTtlSeconds": int(purgeTokenTTL / time.Second),
		"actorExists":     actor != nil,
		"handle":          "",
		"latestIndexedAt": "",
	}
	if actor != nil {
		out["handle"] = actor.Handle
		if !actor.IndexedAt.IsZero() {
			out["latestIndexedAt"] = actor.IndexedAt.UTC().Format(time.RFC3339)
		}
	}
	return out, nil
}

// purgeActor verifies the bound token, then runs the SQL-only
// destructive leg inside one transaction. Tap removal is fired
// best-effort *after* the commit because Tap is an HTTP sidecar
// and cannot enlist in sql.BeginTx — the alternative ordering
// (Tap first, then SQL) would leave Tap state out of sync if the
// commit failed for any reason, which is the worse failure mode.
// On Tap failure the mutation still reports success because the
// authoritative state (the index) has been purged; operators get
// a structured log line and can rerun the Tap cleanup manually.
func (r *Resolver) PurgeActor(ctx context.Context, did, confirmToken string) (map[string]interface{}, error) {
	if err := requireAdmin(ctx); err != nil {
		return nil, err
	}
	if r.purgeTokenSigner == nil {
		return nil, errors.New("purge mutation is not configured")
	}
	if confirmToken == "" {
		return nil, errors.New("confirmToken is required")
	}
	// Strict DID validation (mirror of PreviewPurgeActor). The token
	// also binds to the target DID, but the binding check is downstream
	// of HMAC compare — surface the cleaner error message first.
	if !didpkg.IsValid(did) {
		return nil, errors.New("invalid DID")
	}

	adminDID, _ := ctx.Value(contextKeyUserDID).(string)
	if adminDID == "" {
		return nil, errors.New("admin DID missing from context")
	}

	// Recount records so the token-bound count is verified against
	// fresh state. If the count drifted (a record was ingested
	// between preview and purge), Verify rejects with
	// ErrPurgeTokenCountDrift and the operator re-previews.
	recordCount, err := r.repos.Records.CountByDID(ctx, did)
	if err != nil {
		return nil, fmt.Errorf("count records: %w", err)
	}
	// VerifyReason returns a bounded reason string (one of
	// metrics.PurgeReason*) suitable for use as a Prometheus
	// label. On rejection we increment the metric *before*
	// returning so the SLO covers every failure path, including
	// the wrong-admin / wrong-target forge attempts that
	// collapse to ErrPurgeTokenInvalid in the error contract.
	if reason, err := r.purgeTokenSigner.VerifyReason(confirmToken, ScopeActorPurge, adminDID, did, recordCount); err != nil {
		metrics.PurgeTokenRejected(reason)
		return nil, err
	}

	tx, err := r.repos.Records.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	deletedRecords, err := r.repos.Records.DeleteByDIDTx(ctx, tx, did)
	if err != nil {
		return nil, err
	}
	deletedActor, err := r.repos.Actors.DeleteByDIDTx(ctx, tx, did)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit purge tx: %w", err)
	}

	// Best-effort Tap cleanup. Bounded timeout so a sidecar
	// outage can't stall the resolver — the mutation has
	// already returned success for the SQL leg. tapStatus must
	// be one of metrics.PurgeTapStatus*; the resolver echoes the
	// same value back to GraphQL.
	tapStatus := metrics.PurgeTapStatusSkipped
	if r.tapRemover != nil {
		tapCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := r.tapRemover.RemoveRepos(tapCtx, []string{did}); err != nil {
			slog.Warn("actor purge: tap removal failed (sql commit succeeded; rerun manually)",
				"actor_did", logsafe.DID(did),
				"err", err)
			tapStatus = metrics.PurgeTapStatusFailed
		} else {
			tapStatus = metrics.PurgeTapStatusRemoved
		}
	}

	// Structured audit log — see SECURITY.md operator contract for
	// the retention requirement (≥90d GDPR-minimum, 1y recommended).
	// `target_did` is the mutation input; previously a redundant
	// `actor_did` carried the same value, which broke any downstream
	// SIEM tooling assuming the two fields differ. Dropped.
	//
	// did and adminDID are both validated upstream (did.IsValid in
	// PreviewPurgeActor + PurgeActor, admin DID set by the auth
	// middleware after did.IsValid). logsafe.DID is belt-and-braces:
	// if a future bug bypasses upstream validation, the audit line
	// is still well-formed.
	slog.Info("actor purge",
		"event", "actor_purge",
		"target_did", logsafe.DID(did),
		"record_count", deletedRecords,
		"actor_rows", deletedActor,
		"requested_by_did", logsafe.DID(adminDID),
		"tap_status", tapStatus,
		"ts", time.Now().UTC().Format(time.RFC3339))

	// Metric tail: one purge counted, records-deleted histogram
	// updated. Bucketed at 1/10/100/1k/10k/100k/1M so a single
	// large-actor takedown is distinguishable from a noisy
	// test-data cleanup loop.
	metrics.PurgeActorCompleted(tapStatus, deletedRecords)

	return map[string]interface{}{
		"did":              did,
		"recordsDeleted":   deletedRecords,
		"actorRowsDeleted": deletedActor,
		"tapStatus":        tapStatus,
	}, nil
}

// =============================================================================
// Reset-all admin mutations
//
// Co-located with PreviewPurgeActor / PurgeActor (above) because
// they share the destructive-admin contract: preview returns a
// signed token, confirm requires that token, both go through the
// auditSettingsChanged log shape. Moved here from resolvers.go in
// 2026-05-17 Track 5 per plan-review item R2.2 — the comment at
// the original resolvers.go:1338 already documented the
// "Mirrors the PreviewPurgeActor contract" intent.
// =============================================================================

// resetAllTables is the hard-listed deletion target set for the
// resetAll admin mutation. ORDER MATTERS — child rows go before
// parents so FK constraints don't reject the delete. Tables NOT in
// this list are preserved intentionally:
//
//   - schema_migrations: bookkeeping, must outlive a reset.
//   - config: operator settings (admin_dids, relay_url, ...) — a
//     reset must not lock the operator out of their own instance.
//   - lexicon: schema definitions. The point of resetAll is to wipe
//     data, not unregister lexicons.
//   - label_definition: includes the seeded Bluesky takedown
//     vocabulary; preserved by design.
//   - oauth_client: registered client apps. A reset invalidates
//     every issued token (below) but keeps the registrations so
//     existing apps can re-authenticate.
//   - jetstream_cursor: operational state. Wiping this would force
//     a re-backfill from the relay's earliest cursor.
//
// SOURCE OF TRUTH: the migration files in
// internal/database/migrations/postgres/*.up.sql. When a new
// migration adds a table whose contents are user/actor/activity
// data, append it to this list. TODO(track-3 follow-up): when this
// list outgrows ~30 entries, replace with the introspection
// approach (SELECT FROM pg_tables WHERE schemaname='public') so it
// can't rot quietly.
var resetAllTables = []string{
	// Notifications subsystem (migration 015). notification_participant
	// has a FK to notification; child first.
	"notification_participant",
	"notification",
	"actor_state",

	// Moderation: reports + applied labels + per-user prefs
	// (migrations 003, 004). label_definition is preserved.
	"actor_label_preference",
	"label",
	"report",

	// OAuth tokens / sessions / replay caches / requests
	// (migration 001 + 016). oauth_client is preserved so
	// registered apps can re-authenticate. All token / session
	// tables FK to oauth_client with ON DELETE CASCADE; we
	// delete them explicitly to make the count exact.
	"oauth_authorization_code",
	"oauth_atp_request",
	"oauth_atp_session",
	"oauth_auth_request",
	"oauth_dpop_jti",
	"oauth_dpop_nonce",
	"oauth_par_request",
	"oauth_refresh_token",
	"oauth_access_token",
	"admin_session",

	// Activity log (migration 001).
	"jetstream_activity",

	// Records authored by every actor (migration 001).
	"record",

	// Actors themselves (migration 001).
	"actor",
}

// PreviewResetAll materializes the row-count preview the operator
// confirms against and returns an HMAC-signed token bound to
// (admin_did, total_rows, exp, scope=reset_all). Mirrors the
// PreviewPurgeActor contract; see internal/graphql/admin/purge.go.
//
// ResetAll is strictly more destructive than PurgeActor (wipes the
// whole index, not one actor) and therefore must be at least as
// hardened: same HMAC signer, same single-use + count-drift + scope
// binding, same audit-log shape.
func (r *Resolver) PreviewResetAll(ctx context.Context) (map[string]interface{}, error) {
	if err := requireAdmin(ctx); err != nil {
		return nil, err
	}
	if r.purgeTokenSigner == nil {
		return nil, fmt.Errorf("resetAll mutation is not configured")
	}
	adminDID, _ := ctx.Value(contextKeyUserDID).(string)
	if adminDID == "" {
		return nil, fmt.Errorf("admin DID missing from context")
	}

	tables, totalRows, err := r.resetAllCounts(ctx)
	if err != nil {
		return nil, fmt.Errorf("count rows: %w", err)
	}

	// TargetDID is empty for the reset_all scope — the operation
	// has no single target. The signer's admin + count + scope
	// bindings carry the security contract.
	token, exp, err := r.purgeTokenSigner.Sign(ScopeResetAll, adminDID, "", totalRows)
	if err != nil {
		return nil, fmt.Errorf("sign reset token: %w", err)
	}

	return map[string]interface{}{
		"totalRows":       totalRows,
		"tables":          tables,
		"confirmToken":    token,
		"tokenExpiresAt":  exp.UTC().Format(time.RFC3339),
		"tokenTtlSeconds": int(purgeTokenTTL / time.Second),
	}, nil
}

// resetAllCounts returns per-table row counts plus the sum across
// every entry in resetAllTables. Each count is a separate query;
// the list is small (<20) so the round-trips are noise next to the
// destructive delete that follows.
func (r *Resolver) resetAllCounts(ctx context.Context) ([]map[string]interface{}, int64, error) {
	db := r.repos.Records.DB()
	tables := make([]map[string]interface{}, 0, len(resetAllTables))
	var total int64
	for _, table := range resetAllTables {
		// Table names are hard-listed package constants; no SQL
		// injection surface. quoteIdent defends future
		// contributors who copy-paste this loop with a different
		// source.
		var count int64
		// nolint:gosec // table names are validated package constants
		query := fmt.Sprintf("SELECT COUNT(*) FROM %s", quoteIdent(table))
		if err := db.QueryRowContext(ctx, query).Scan(&count); err != nil {
			return nil, 0, fmt.Errorf("count %s: %w", table, err)
		}
		tables = append(tables, map[string]interface{}{
			"name":  table,
			"count": count,
		})
		total += count
	}
	return tables, total, nil
}

// quoteIdent quotes a SQL identifier for Postgres. The input is
// always a hard-listed table name from this package; this is
// defense-in-depth for future code that might pass through
// user-controlled input.
func quoteIdent(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

// ResetAll verifies the bound HMAC token, then truncates every
// table in resetAllTables inside a single transaction. The whole
// set commits or rolls back atomically. On commit, emits the
// structured audit log line documented in SECURITY.md.
func (r *Resolver) ResetAll(ctx context.Context, confirmToken string) (map[string]interface{}, error) {
	if err := requireAdmin(ctx); err != nil {
		return nil, err
	}
	if r.purgeTokenSigner == nil {
		return nil, fmt.Errorf("resetAll mutation is not configured")
	}
	if confirmToken == "" {
		return nil, fmt.Errorf("confirmToken is required")
	}
	adminDID, _ := ctx.Value(contextKeyUserDID).(string)
	if adminDID == "" {
		return nil, fmt.Errorf("admin DID missing from context")
	}

	// Re-count under fresh state so the token-bound total is
	// verified against current rows. A drift between preview and
	// confirm rejects with ErrPurgeTokenCountDrift and the
	// operator re-previews — exactly the actor-purge flow.
	_, totalRows, err := r.resetAllCounts(ctx)
	if err != nil {
		return nil, fmt.Errorf("count rows: %w", err)
	}
	// VerifyReason returns the bounded metric reason (one of
	// metrics.PurgeReason*) so a token-forge attempt against the
	// resetAll surface is visible in
	// hypergoat_purge_token_rejected_total just like an actor-purge
	// forge attempt. Metric increments before the early-return so
	// every failure mode is observed.
	if reason, err := r.purgeTokenSigner.VerifyReason(confirmToken, ScopeResetAll, adminDID, "", totalRows); err != nil {
		metrics.PurgeTokenRejected(reason)
		return nil, err
	}

	tx, err := r.repos.Records.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var totalDeleted int64
	for _, table := range resetAllTables {
		// nolint:gosec // table names are hard-listed package constants
		query := fmt.Sprintf("DELETE FROM %s", quoteIdent(table))
		res, err := tx.ExecContext(ctx, query)
		if err != nil {
			return nil, fmt.Errorf("delete from %s: %w", table, err)
		}
		if n, err := res.RowsAffected(); err == nil {
			totalDeleted += n
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit reset tx: %w", err)
	}

	// Structured audit log — see SECURITY.md operator contract
	// for the retention requirement (≥90d GDPR-minimum, 1y
	// recommended). Shape mirrors actor_purge so log shippers
	// can route both with one rule. requested_by_did flows through
	// logsafe.DID — defense-in-depth even though the admin DID
	// was already validated by the auth middleware.
	slog.Info("admin reset_all",
		"event", "reset_all",
		"requested_by_did", logsafe.DID(adminDID),
		"rows_deleted", totalDeleted,
		"tables_affected", len(resetAllTables),
		"ts", time.Now().UTC().Format(time.RFC3339),
	)
	metrics.ResetAllCompleted()

	return map[string]interface{}{
		"rowsDeleted":    totalDeleted,
		"tablesAffected": len(resetAllTables),
	}, nil
}
