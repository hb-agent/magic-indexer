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
)

// purgeTokenTTL bounds how long a previewPurgeActor token remains
// valid before the admin must re-issue the preview. Five minutes is
// long enough for a human operator to read a multi-thousand-record
// preview against external state (a takedown ticket, GDPR request,
// the indexer's own GraphiQL) without being punished for it; short
// enough that an abandoned browser tab cannot be replayed an hour
// later.
const purgeTokenTTL = 5 * time.Minute

// PurgeTokenSigner mints and verifies HMAC-signed confirm tokens for
// the actor-purge admin mutation. A token is a base64url-encoded
// payload + HMAC, bound to the requesting admin DID, the target
// DID, the record count at preview time, and an expiry. Verification
// rejects any mismatch (different admin, different target, count
// drift, expired) via constant-time compare on the HMAC.
//
// Single-use is enforced server-side via an in-memory used-token
// set keyed by the signature. The set is cleaned up lazily on each
// verify call. Restarts clear the set but the exp claim prevents
// replays beyond purgeTokenTTL anyway.
//
// Tokens are stateless from the server's perspective — verification
// re-derives the HMAC from claims, so a single replica restart
// mid-flow does not invalidate a not-yet-redeemed token.
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
// expected purgeTokenVersion.
type purgeTokenClaims struct {
	V           int    `json:"v"`
	AdminDID    string `json:"admin_did"`
	TargetDID   string `json:"target_did"`
	RecordCount int64  `json:"record_count"`
	Exp         int64  `json:"exp"` // unix seconds
}

// purgeTokenVersion is the current claim shape. Bump when adding
// or removing a field; old-version tokens are rejected as
// ErrPurgeTokenInvalid (one TTL of disruption on deploy, no
// silent verification failures).
const purgeTokenVersion = 1

// Sign issues a fresh token for the given (admin, target, count)
// triple. Returns the token and its expiry timestamp so the
// resolver can echo "expires in M:SS" to the client.
func (s *PurgeTokenSigner) Sign(adminDID, targetDID string, recordCount int64) (string, time.Time, error) {
	exp := s.now().Add(purgeTokenTTL)
	claims := purgeTokenClaims{
		V:           purgeTokenVersion,
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
// to the given (admin, target, count) triple, then marks the
// signature as consumed. Returns ErrPurgeTokenInvalid /
// ErrPurgeTokenExpired / ErrPurgeTokenAlreadyUsed for the three
// distinct rejection modes the UI cares about; anything else
// collapses to ErrPurgeTokenInvalid.
func (s *PurgeTokenSigner) Verify(token, adminDID, targetDID string, recordCount int64) error {
	// Split into payload + signature halves before doing any
	// HMAC work — a malformed token shouldn't get past parsing.
	parts := strings.SplitN(token, ".", 2)
	if len(parts) != 2 {
		return ErrPurgeTokenInvalid
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return ErrPurgeTokenInvalid
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return ErrPurgeTokenInvalid
	}

	// Constant-time HMAC compare. Any payload-tampering shows
	// up as a mismatch here.
	want := s.hmac(payload)
	if subtle.ConstantTimeCompare(want, sig) != 1 {
		return ErrPurgeTokenInvalid
	}

	// Only deserialize after the HMAC has matched — payload
	// is now trusted to be ours.
	var claims purgeTokenClaims
	if err := json.Unmarshal(payload, &claims); err != nil {
		return ErrPurgeTokenInvalid
	}

	// Reject tokens minted by a previous version of the claim
	// schema. See purgeTokenVersion docs.
	if claims.V != purgeTokenVersion {
		return ErrPurgeTokenInvalid
	}

	// Bind check: admin DID and target DID must match the values
	// claimed at preview time. Without these, admin A's token
	// could be replayed by admin B or across a different DID.
	if claims.AdminDID != adminDID || claims.TargetDID != targetDID {
		return ErrPurgeTokenInvalid
	}
	// Record count drift is a separate sentinel — see
	// ErrPurgeTokenCountDrift docs.
	if claims.RecordCount != recordCount {
		return ErrPurgeTokenCountDrift
	}

	now := s.now()
	if now.Unix() >= claims.Exp {
		return ErrPurgeTokenExpired
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
		return ErrPurgeTokenAlreadyUsed
	}
	s.usedSigs[sigKey] = time.Unix(claims.Exp, 0)
	return nil
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

	token, exp, err := r.purgeTokenSigner.Sign(adminDID, did, recordCount)
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
	// ErrPurgeTokenInvalid and the operator re-previews.
	recordCount, err := r.repos.Records.CountByDID(ctx, did)
	if err != nil {
		return nil, fmt.Errorf("count records: %w", err)
	}
	if err := r.purgeTokenSigner.Verify(confirmToken, adminDID, did, recordCount); err != nil {
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
	// already returned success for the SQL leg.
	tapStatus := "skipped"
	if r.tapRemover != nil {
		tapCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := r.tapRemover.RemoveRepos(tapCtx, []string{did}); err != nil {
			slog.Warn("actor purge: tap removal failed (sql commit succeeded; rerun manually)",
				"actor_did", did,
				"err", err)
			tapStatus = "failed"
		} else {
			tapStatus = "removed"
		}
	}

	// Structured audit log — see SECURITY.md operator contract for
	// the retention requirement (≥90d GDPR-minimum, 1y recommended).
	// `target_did` is the mutation input; previously a redundant
	// `actor_did` carried the same value, which broke any downstream
	// SIEM tooling assuming the two fields differ. Dropped.
	slog.Info("actor purge",
		"event", "actor_purge",
		"target_did", did,
		"record_count", deletedRecords,
		"actor_rows", deletedActor,
		"requested_by_did", adminDID,
		"tap_status", tapStatus,
		"ts", time.Now().UTC().Format(time.RFC3339))

	return map[string]interface{}{
		"did":              did,
		"recordsDeleted":   deletedRecords,
		"actorRowsDeleted": deletedActor,
		"tapStatus":        tapStatus,
	}, nil
}
