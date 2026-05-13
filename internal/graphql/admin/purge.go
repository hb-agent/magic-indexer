package admin

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"
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
	usedSigs map[string]time.Time // signature → exp (for lazy prune)
}

// NewPurgeTokenSigner returns a signer keyed by the application's
// SECRET_KEY_BASE. The secret must be non-empty; an empty secret
// would let any caller forge tokens and is treated as a configuration
// error.
func NewPurgeTokenSigner(secret []byte) (*PurgeTokenSigner, error) {
	if len(secret) == 0 {
		return nil, errors.New("purge token signer: empty secret")
	}
	return &PurgeTokenSigner{
		secret:   secret,
		now:      time.Now,
		usedSigs: make(map[string]time.Time),
	}, nil
}

// purgeTokenClaims is the signed payload. JSON shape kept stable
// because the signature is over its serialized bytes.
type purgeTokenClaims struct {
	AdminDID    string `json:"admin_did"`
	TargetDID   string `json:"target_did"`
	RecordCount int64  `json:"record_count"`
	Exp         int64  `json:"exp"` // unix seconds
}

// Sign issues a fresh token for the given (admin, target, count)
// triple. Returns the token and its expiry timestamp so the
// resolver can echo "expires in M:SS" to the client.
func (s *PurgeTokenSigner) Sign(adminDID, targetDID string, recordCount int64) (string, time.Time, error) {
	exp := s.now().Add(purgeTokenTTL)
	claims := purgeTokenClaims{
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
var (
	ErrPurgeTokenInvalid     = errors.New("purge_token_invalid")
	ErrPurgeTokenExpired     = errors.New("purge_token_expired")
	ErrPurgeTokenAlreadyUsed = errors.New("purge_token_already_used")
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

	// Bind check: admin DID, target DID, and record count must
	// all match the values claimed at preview time. Without
	// these, admin A's token could be replayed by admin B, or
	// across a different DID, or against a stale row count.
	if claims.AdminDID != adminDID || claims.TargetDID != targetDID || claims.RecordCount != recordCount {
		return ErrPurgeTokenInvalid
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
	if did == "" {
		return nil, errors.New("did is required")
	}

	adminDID, _ := ctx.Value(contextKeyUserDID).(string)
	if adminDID == "" {
		return nil, errors.New("admin DID missing from context")
	}

	actor, err := r.repos.Actors.GetByDID(ctx, did)
	if err != nil {
		// "Actor not in the index" is still a legitimate purge
		// target — there may be records authored by the DID
		// before the actor row was indexed, and the operator
		// wants those gone too. Surface a zero-handle preview
		// rather than failing.
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
	if did == "" || confirmToken == "" {
		return nil, errors.New("did and confirmToken are required")
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

	slog.Info("actor purge",
		"event", "actor_purge",
		"actor_did", did,
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
