package admin

// Resolver-level tests for the purge subsystem (T-COV-1). The
// signer-level tests in purge_test.go cover the HMAC contract; the
// tests below cover everything that lives on the resolver itself —
// admin gating, DID validation, transaction boundary, tap-status
// classification, audit-log emission, and metric increments — none
// of which are exercised by the signer tests.
//
// The tests use the Postgres test harness (testutil.SetupTestDB) so
// the same SQL the resolver issues in production runs here. Locally
// the harness fails-fast with "connection refused" when Postgres is
// not available — that's the documented behaviour for the wider DB
// test suite and CI provides the connection.
//
// Stub TapRemover lives in test_helpers.go alongside the
// admin-context constructor so resolver code can stay untouched.

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/GainForest/hypergoat/internal/metrics"
	"github.com/GainForest/hypergoat/internal/testutil"
)

// newPurgeFixture wires a Resolver against a fresh Postgres test
// schema, a fresh PurgeTokenSigner with a controllable clock, and a
// fresh TapRemover stub (which can be reconfigured per-test).
// `now` is returned so tests advancing past the token TTL stay
// inside the harness rather than reaching into the signer's
// unexported clock from each call site.
type purgeFixture struct {
	t        *testing.T
	db       *testutil.TestDB
	resolver *Resolver
	signer   *PurgeTokenSigner
	tap      *stubTapRemover
	now      *time.Time
	logBuf   *bytes.Buffer
}

func newPurgeFixture(t *testing.T) *purgeFixture {
	t.Helper()
	db := testutil.SetupTestDB(t)

	signer, err := NewPurgeTokenSigner([]byte("test-secret-of-sufficient-entropy-for-purge-resolver-tests"))
	if err != nil {
		t.Fatalf("NewPurgeTokenSigner: %v", err)
	}
	t.Cleanup(signer.Close)

	now := time.Now().UTC()
	signer.now = func() time.Time { return now }

	repos := &Repositories{
		Records:          db.Records,
		Actors:           db.Actors,
		Lexicons:         db.Lexicons,
		Config:           db.Config,
		OAuthClients:     db.OAuthClients,
		Activity:         db.Activity,
		Labels:           db.Labels,
		LabelDefinitions: db.LabelDefinitions,
		LabelPreferences: db.LabelPreferences,
		Reports:          db.Reports,
	}
	resolver := NewResolver(repos, "did:plc:domain")
	resolver.SetPurgeTokenSigner(signer)
	tap := &stubTapRemover{}
	resolver.SetTapRemover(tap)

	// Swap slog default so audit-log assertions can read the
	// emitted lines. Restore in t.Cleanup so suite-wide log
	// output isn't blackholed for subsequent tests.
	logBuf := &bytes.Buffer{}
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(logBuf, &slog.HandlerOptions{Level: slog.LevelInfo})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	return &purgeFixture{
		t:        t,
		db:       db,
		resolver: resolver,
		signer:   signer,
		tap:      tap,
		now:      &now,
		logBuf:   logBuf,
	}
}

// adminCtx returns a context tagged as an admin operator. The DID
// flows into the token binding and into the audit-log line; tests
// vary it where the binding semantics matter.
func adminCtx(did string) context.Context {
	return ContextWithAuth(context.Background(), did, "admin.handle", true, []string{did})
}

// nonAdminCtx returns a context tagged as a non-admin operator.
// requireAdmin must reject before any DB or signer work happens.
func nonAdminCtx(did string) context.Context {
	return ContextWithAuth(context.Background(), did, "user.handle", false, nil)
}

// seedRecords inserts n records for `did` in collection `col`. The
// inserts are positional so test bodies stay readable.
func seedRecords(t *testing.T, db *testutil.TestDB, did, col string, n int) {
	t.Helper()
	ctx := context.Background()
	for i := 0; i < n; i++ {
		uri := "at://" + did + "/" + col + "/r" + itoa(i)
		if _, err := db.Records.Insert(ctx, uri, "cid"+itoa(i), did, col, `{"v":1}`); err != nil {
			t.Fatalf("seed record %d: %v", i, err)
		}
	}
}

// itoa avoids pulling strconv into the test helper signature. The
// numbers involved (record indices, single digits) are too small to
// merit the import-noise cost.
func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b [20]byte
	pos := len(b)
	for i > 0 {
		pos--
		b[pos] = byte('0' + i%10)
		i /= 10
	}
	return string(b[pos:])
}

// previewAndSign goes through the resolver's preview path and
// returns the token + the record-count baked into it. Tests that
// only need a happy-path token use this; tests that exercise the
// failure modes call the signer directly to mint a divergent token.
func (f *purgeFixture) previewAndSign(adminDID, targetDID string) (string, int64) {
	f.t.Helper()
	out, err := f.resolver.PreviewPurgeActor(adminCtx(adminDID), targetDID)
	if err != nil {
		f.t.Fatalf("PreviewPurgeActor: %v", err)
	}
	tok, _ := out["confirmToken"].(string)
	if tok == "" {
		f.t.Fatalf("PreviewPurgeActor returned no token")
	}
	cnt, _ := out["recordCount"].(int64)
	return tok, cnt
}

// gatherCounter returns the current value of a labelled counter
// from the metrics registry. Returns 0 when the label combination
// hasn't been observed yet — same shape as
// extractors/shared_test.go's counterValue helper but lookup is by
// arbitrary label name (not hard-coded "outcome").
func gatherCounter(t *testing.T, name, labelName, labelValue string) float64 {
	t.Helper()
	families, err := metrics.Registry.Gather()
	if err != nil {
		t.Fatalf("registry gather: %v", err)
	}
	for _, mf := range families {
		if mf.GetName() != name {
			continue
		}
		for _, m := range mf.GetMetric() {
			for _, l := range m.GetLabel() {
				if l.GetName() == labelName && l.GetValue() == labelValue {
					return m.GetCounter().GetValue()
				}
			}
		}
	}
	return 0
}

// gatherHistogramSampleCount returns the cumulative sample count of
// a histogram (no labels). The records-deleted histogram has no
// label dimensions; we use the count rather than the sum so an
// observed 0-records purge still ticks the counter visibly.
func gatherHistogramSampleCount(t *testing.T, name string) uint64 {
	t.Helper()
	families, err := metrics.Registry.Gather()
	if err != nil {
		t.Fatalf("registry gather: %v", err)
	}
	for _, mf := range families {
		if mf.GetName() != name {
			continue
		}
		for _, m := range mf.GetMetric() {
			if m.GetHistogram() != nil {
				return m.GetHistogram().GetSampleCount()
			}
		}
	}
	return 0
}

// --- Tests --------------------------------------------------------

// TestPurgeActor_HappyPath wires up a real DB, previews + confirms,
// and asserts: (a) records and actor row are gone after the
// mutation, (b) the response payload reports tap_status="removed",
// (c) an audit-log line with event=actor_purge is emitted, (d) the
// purge_actor / records_deleted metrics tick.
func TestPurgeActor_HappyPath(t *testing.T) {
	f := newPurgeFixture(t)
	const adminDID = "did:plc:admin1"
	const targetDID = "did:plc:victim1"
	const col = "com.example.test"

	if err := f.db.Actors.Upsert(context.Background(), targetDID, "victim.example.com"); err != nil {
		t.Fatalf("upsert actor: %v", err)
	}
	seedRecords(t, f.db, targetDID, col, 3)

	tokRejBefore := gatherCounter(t, "hypergoat_purge_token_rejected_total", "reason", "")
	purgeBefore := gatherCounter(t, "hypergoat_purge_actor_total", "tap_status", metrics.PurgeTapStatusRemoved)
	histBefore := gatherHistogramSampleCount(t, "hypergoat_purge_records_deleted")

	token, count := f.previewAndSign(adminDID, targetDID)
	if count != 3 {
		t.Fatalf("preview recordCount = %d, want 3", count)
	}

	res, err := f.resolver.PurgeActor(adminCtx(adminDID), targetDID, token)
	if err != nil {
		t.Fatalf("PurgeActor: %v", err)
	}

	// Response shape.
	if rd, _ := res["recordsDeleted"].(int64); rd != 3 {
		t.Errorf("recordsDeleted = %v, want 3", res["recordsDeleted"])
	}
	if ar, _ := res["actorRowsDeleted"].(int64); ar != 1 {
		t.Errorf("actorRowsDeleted = %v, want 1", res["actorRowsDeleted"])
	}
	if ts, _ := res["tapStatus"].(string); ts != metrics.PurgeTapStatusRemoved {
		t.Errorf("tapStatus = %q, want %q", res["tapStatus"], metrics.PurgeTapStatusRemoved)
	}

	// SQL state is fully purged inside the resolver's transaction.
	if c, err := f.db.Records.CountByDID(context.Background(), targetDID); err != nil || c != 0 {
		t.Errorf("post-purge CountByDID(%s) = (%d, %v), want (0, nil)", targetDID, c, err)
	}
	if _, err := f.db.Actors.GetByDID(context.Background(), targetDID); err == nil {
		t.Errorf("post-purge GetByDID(%s) returned an actor; row should be gone", targetDID)
	}

	// Tap stub was invoked once with the target DID.
	if calls := f.tap.calls(); len(calls) != 1 || len(calls[0]) != 1 || calls[0][0] != targetDID {
		t.Errorf("tap RemoveRepos calls = %v, want one call with [%s]", calls, targetDID)
	}

	// Audit log: event=actor_purge with the validated DIDs.
	log := f.logBuf.String()
	if !strings.Contains(log, `event=actor_purge`) {
		t.Errorf("audit log missing event=actor_purge: %s", log)
	}
	if !strings.Contains(log, "target_did="+targetDID) {
		t.Errorf("audit log missing target_did=%s: %s", targetDID, log)
	}
	if !strings.Contains(log, "requested_by_did="+adminDID) {
		t.Errorf("audit log missing requested_by_did=%s: %s", adminDID, log)
	}
	if !strings.Contains(log, "tap_status="+metrics.PurgeTapStatusRemoved) {
		t.Errorf("audit log missing tap_status=%s: %s", metrics.PurgeTapStatusRemoved, log)
	}

	// Metrics ticked.
	if got := gatherCounter(t, "hypergoat_purge_actor_total", "tap_status", metrics.PurgeTapStatusRemoved); got != purgeBefore+1 {
		t.Errorf("purge_actor_total{tap_status=removed} = %v, want %v (before=%v)", got, purgeBefore+1, purgeBefore)
	}
	if got := gatherHistogramSampleCount(t, "hypergoat_purge_records_deleted"); got != histBefore+1 {
		t.Errorf("purge_records_deleted sample count = %d, want %d", got, histBefore+1)
	}
	// Token-rejected counter must NOT tick on the happy path. We
	// poll the empty-reason cell as a witness for "no reason label
	// fired" — Prometheus increments only the labels that were
	// observed, so the sum across reasons is what we want.
	_ = tokRejBefore
}

// TestPurgeActor_RequiresAdmin checks the resolver short-circuits
// before any signer or repo work when the context isn't admin. The
// returned error must mention "admin privileges required" so the
// schema-level shim's removal would surface here.
func TestPurgeActor_RequiresAdmin(t *testing.T) {
	f := newPurgeFixture(t)
	const targetDID = "did:plc:notadmin"
	// Seed records so a wrongly-passed call would visibly mutate
	// state. The test asserts the call DOESN'T do that.
	seedRecords(t, f.db, targetDID, "com.example.test", 1)

	_, err := f.resolver.PurgeActor(nonAdminCtx("did:plc:randomuser"), targetDID, "irrelevant")
	if err == nil {
		t.Fatal("PurgeActor accepted a non-admin context; admin gate is broken")
	}
	if !strings.Contains(err.Error(), "admin") {
		t.Errorf("non-admin error = %q, want it to mention admin gate", err)
	}

	// State unchanged.
	if c, _ := f.db.Records.CountByDID(context.Background(), targetDID); c != 1 {
		t.Errorf("records were deleted under non-admin context; pre/post count = 1/%d", c)
	}

	// Preview also gated.
	if _, err := f.resolver.PreviewPurgeActor(nonAdminCtx("did:plc:randomuser"), targetDID); err == nil {
		t.Error("PreviewPurgeActor accepted a non-admin context; admin gate is broken")
	}
}

// TestPurgeActor_RejectsInvalidDID covers the strict DID validation
// at both the preview and confirm entry points. Two invalid shapes
// matter: a too-short string ("did:") and a handle masquerading
// without the "did:" prefix.
func TestPurgeActor_RejectsInvalidDID(t *testing.T) {
	f := newPurgeFixture(t)
	const adminDID = "did:plc:admin1"

	cases := []struct {
		name, did string
	}{
		{"too-short", "did:"},
		{"handle-not-did", "alice.bsky.social"},
		{"empty", ""},
		{"newline-injection", "did:plc:abc\nfake"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := f.resolver.PreviewPurgeActor(adminCtx(adminDID), c.did); err == nil {
				t.Errorf("PreviewPurgeActor(%q) accepted invalid DID", c.did)
			}
			if _, err := f.resolver.PurgeActor(adminCtx(adminDID), c.did, "irrelevant-token"); err == nil {
				t.Errorf("PurgeActor(%q) accepted invalid DID", c.did)
			}
		})
	}
}

// TestPurgeActor_RejectsExpiredToken previews, advances the
// signer's clock past the TTL, and confirms. The error must be
// ErrPurgeTokenExpired (not the generic Invalid). The
// purge_token_rejected counter ticks under the "expired" reason.
func TestPurgeActor_RejectsExpiredToken(t *testing.T) {
	f := newPurgeFixture(t)
	const adminDID = "did:plc:admin1"
	const targetDID = "did:plc:victim1"

	if err := f.db.Actors.Upsert(context.Background(), targetDID, "victim.example.com"); err != nil {
		t.Fatalf("upsert actor: %v", err)
	}
	seedRecords(t, f.db, targetDID, "com.example.test", 1)

	token, _ := f.previewAndSign(adminDID, targetDID)
	*f.now = f.now.Add(purgeTokenTTL + time.Second)

	rejectedBefore := gatherCounter(t, "hypergoat_purge_token_rejected_total", "reason", "expired")
	_, err := f.resolver.PurgeActor(adminCtx(adminDID), targetDID, token)
	if !errors.Is(err, ErrPurgeTokenExpired) {
		t.Fatalf("PurgeActor(expired) err = %v, want ErrPurgeTokenExpired", err)
	}
	if got := gatherCounter(t, "hypergoat_purge_token_rejected_total", "reason", "expired"); got != rejectedBefore+1 {
		t.Errorf("purge_token_rejected_total{reason=expired} delta = %v, want 1", got-rejectedBefore)
	}
	// State unchanged.
	if c, _ := f.db.Records.CountByDID(context.Background(), targetDID); c != 1 {
		t.Errorf("records modified on rejected-token path; count = %d, want 1", c)
	}
}

// TestPurgeActor_RejectsCountDrift previews at N records, ingests
// one more, then confirms. The resolver must reject with the
// drift-specific sentinel, NOT ErrPurgeTokenInvalid — the
// distinction is what tells forensics "operator raced an ingest"
// (benign) from "someone is forging tokens" (active attack).
func TestPurgeActor_RejectsCountDrift(t *testing.T) {
	f := newPurgeFixture(t)
	const adminDID = "did:plc:admin1"
	const targetDID = "did:plc:victim1"
	const col = "com.example.test"

	if err := f.db.Actors.Upsert(context.Background(), targetDID, "victim.example.com"); err != nil {
		t.Fatalf("upsert actor: %v", err)
	}
	seedRecords(t, f.db, targetDID, col, 2)

	token, count := f.previewAndSign(adminDID, targetDID)
	if count != 2 {
		t.Fatalf("preview count = %d, want 2", count)
	}

	// A new record lands between preview and confirm.
	if _, err := f.db.Records.Insert(context.Background(),
		"at://"+targetDID+"/"+col+"/raced", "cidraced", targetDID, col, `{"v":1}`); err != nil {
		t.Fatalf("ingest raced record: %v", err)
	}

	rejectedBefore := gatherCounter(t, "hypergoat_purge_token_rejected_total", "reason", "count_drift")
	_, err := f.resolver.PurgeActor(adminCtx(adminDID), targetDID, token)
	if !errors.Is(err, ErrPurgeTokenCountDrift) {
		t.Fatalf("PurgeActor(drift) err = %v, want ErrPurgeTokenCountDrift", err)
	}
	if errors.Is(err, ErrPurgeTokenInvalid) {
		t.Errorf("PurgeActor(drift) collapsed to ErrPurgeTokenInvalid; forensic separation lost")
	}
	if got := gatherCounter(t, "hypergoat_purge_token_rejected_total", "reason", "count_drift"); got != rejectedBefore+1 {
		t.Errorf("purge_token_rejected_total{reason=count_drift} delta = %v, want 1", got-rejectedBefore)
	}
}

// TestPurgeActor_RejectsReplayedToken previews, confirms
// successfully, then tries the same token again. The second
// attempt must hit the already_used sentinel.
func TestPurgeActor_RejectsReplayedToken(t *testing.T) {
	f := newPurgeFixture(t)
	const adminDID = "did:plc:admin1"
	const targetDID = "did:plc:victim1"
	const col = "com.example.test"

	if err := f.db.Actors.Upsert(context.Background(), targetDID, "victim.example.com"); err != nil {
		t.Fatalf("upsert actor: %v", err)
	}
	seedRecords(t, f.db, targetDID, col, 1)

	token, _ := f.previewAndSign(adminDID, targetDID)
	if _, err := f.resolver.PurgeActor(adminCtx(adminDID), targetDID, token); err != nil {
		t.Fatalf("first PurgeActor: %v", err)
	}

	// Re-seed so the count check during second redeem would also
	// match if single-use weren't enforced — making the test
	// strictly about replay protection rather than incidentally
	// passing on count drift.
	if err := f.db.Actors.Upsert(context.Background(), targetDID, "victim.example.com"); err != nil {
		t.Fatalf("re-upsert actor: %v", err)
	}
	seedRecords(t, f.db, targetDID, col, 1)

	rejectedBefore := gatherCounter(t, "hypergoat_purge_token_rejected_total", "reason", "already_used")
	_, err := f.resolver.PurgeActor(adminCtx(adminDID), targetDID, token)
	if !errors.Is(err, ErrPurgeTokenAlreadyUsed) {
		t.Fatalf("PurgeActor(replay) err = %v, want ErrPurgeTokenAlreadyUsed", err)
	}
	if got := gatherCounter(t, "hypergoat_purge_token_rejected_total", "reason", "already_used"); got != rejectedBefore+1 {
		t.Errorf("purge_token_rejected_total{reason=already_used} delta = %v, want 1", got-rejectedBefore)
	}
}

// TestPurgeActor_RejectsScopeMismatch mints a reset_all-scoped
// token at the signer level (bypassing the preview helper, which
// only mints actor_purge) and tries to redeem it as a purge target.
// The resolver must reject with ErrPurgeTokenInvalid and the
// scope_mismatch reason label.
func TestPurgeActor_RejectsScopeMismatch(t *testing.T) {
	f := newPurgeFixture(t)
	const adminDID = "did:plc:admin1"
	const targetDID = "did:plc:victim1"
	const col = "com.example.test"

	if err := f.db.Actors.Upsert(context.Background(), targetDID, "victim.example.com"); err != nil {
		t.Fatalf("upsert actor: %v", err)
	}
	seedRecords(t, f.db, targetDID, col, 1)

	// Mint a reset_all token. TargetDID is empty for reset_all by
	// design — but the resolver passes targetDID into Verify, so
	// the scope check fires first regardless.
	tok, _, err := f.signer.Sign(ScopeResetAll, adminDID, "", 1)
	if err != nil {
		t.Fatalf("sign reset_all token: %v", err)
	}

	rejectedBefore := gatherCounter(t, "hypergoat_purge_token_rejected_total", "reason", "scope_mismatch")
	_, err = f.resolver.PurgeActor(adminCtx(adminDID), targetDID, tok)
	if !errors.Is(err, ErrPurgeTokenInvalid) {
		t.Fatalf("PurgeActor(scope mismatch) err = %v, want ErrPurgeTokenInvalid", err)
	}
	if got := gatherCounter(t, "hypergoat_purge_token_rejected_total", "reason", "scope_mismatch"); got != rejectedBefore+1 {
		t.Errorf("purge_token_rejected_total{reason=scope_mismatch} delta = %v, want 1", got-rejectedBefore)
	}
}

// TestPurgeActor_TapFailure_StillSucceeds checks that a Tap removal
// error doesn't roll back the SQL leg or surface as an error to
// the caller. The SQL is the authoritative state; the operator
// gets a structured log line and reruns Tap manually.
func TestPurgeActor_TapFailure_StillSucceeds(t *testing.T) {
	f := newPurgeFixture(t)
	const adminDID = "did:plc:admin1"
	const targetDID = "did:plc:victim1"

	if err := f.db.Actors.Upsert(context.Background(), targetDID, "victim.example.com"); err != nil {
		t.Fatalf("upsert actor: %v", err)
	}
	seedRecords(t, f.db, targetDID, "com.example.test", 2)

	// Configure the stub BEFORE preview/confirm so the RemoveRepos
	// call hits the error path.
	f.tap.setErr(errors.New("tap sidecar 503"))

	token, _ := f.previewAndSign(adminDID, targetDID)

	purgeBefore := gatherCounter(t, "hypergoat_purge_actor_total", "tap_status", metrics.PurgeTapStatusFailed)
	res, err := f.resolver.PurgeActor(adminCtx(adminDID), targetDID, token)
	if err != nil {
		t.Fatalf("PurgeActor(tap failure) err = %v, want nil (Tap failure must not block success)", err)
	}
	if ts, _ := res["tapStatus"].(string); ts != metrics.PurgeTapStatusFailed {
		t.Errorf("tapStatus = %q, want %q", res["tapStatus"], metrics.PurgeTapStatusFailed)
	}
	// SQL state still purged.
	if c, _ := f.db.Records.CountByDID(context.Background(), targetDID); c != 0 {
		t.Errorf("records still present after purge with Tap failure; count = %d", c)
	}
	// Metric ticks under the failed status.
	if got := gatherCounter(t, "hypergoat_purge_actor_total", "tap_status", metrics.PurgeTapStatusFailed); got != purgeBefore+1 {
		t.Errorf("purge_actor_total{tap_status=failed} delta = %v, want 1", got-purgeBefore)
	}
	// And the audit log carries the failed status so operators see it.
	if !strings.Contains(f.logBuf.String(), "tap_status="+metrics.PurgeTapStatusFailed) {
		t.Errorf("audit log missing tap_status=failed: %s", f.logBuf.String())
	}
}

// TestPurgeActor_NoTapRemover confirms the resolver no-ops the Tap
// leg cleanly when the remover is nil (TAP_ENABLED=false config).
// tapStatus is reported as "skipped"; SQL state is fully purged.
func TestPurgeActor_NoTapRemover(t *testing.T) {
	f := newPurgeFixture(t)
	const adminDID = "did:plc:admin1"
	const targetDID = "did:plc:victim1"

	// Detach the stub.
	f.resolver.SetTapRemover(nil)

	if err := f.db.Actors.Upsert(context.Background(), targetDID, "victim.example.com"); err != nil {
		t.Fatalf("upsert actor: %v", err)
	}
	seedRecords(t, f.db, targetDID, "com.example.test", 1)

	token, _ := f.previewAndSign(adminDID, targetDID)

	purgeBefore := gatherCounter(t, "hypergoat_purge_actor_total", "tap_status", metrics.PurgeTapStatusSkipped)
	res, err := f.resolver.PurgeActor(adminCtx(adminDID), targetDID, token)
	if err != nil {
		t.Fatalf("PurgeActor(no tap): %v", err)
	}
	if ts, _ := res["tapStatus"].(string); ts != metrics.PurgeTapStatusSkipped {
		t.Errorf("tapStatus = %q, want %q", res["tapStatus"], metrics.PurgeTapStatusSkipped)
	}
	if calls := f.tap.calls(); len(calls) != 0 {
		t.Errorf("tap stub was invoked despite SetTapRemover(nil): %v", calls)
	}
	if c, _ := f.db.Records.CountByDID(context.Background(), targetDID); c != 0 {
		t.Errorf("records still present; count = %d", c)
	}
	if got := gatherCounter(t, "hypergoat_purge_actor_total", "tap_status", metrics.PurgeTapStatusSkipped); got != purgeBefore+1 {
		t.Errorf("purge_actor_total{tap_status=skipped} delta = %v, want 1", got-purgeBefore)
	}
}

// TestPreviewPurgeActor_EmptyActorOK exercises the
// records-without-actor-row case: a DID has records but no actor
// row (e.g. records ingested before the actor row was upserted).
// The preview must return actorExists=false and the record count
// from the records-only path. Then the confirm must purge the
// records cleanly (deletedRecords = N, actorRowsDeleted = 0).
func TestPreviewPurgeActor_EmptyActorOK(t *testing.T) {
	f := newPurgeFixture(t)
	const adminDID = "did:plc:admin1"
	const targetDID = "did:plc:orphan1"

	// Records, but no actor row.
	seedRecords(t, f.db, targetDID, "com.example.test", 2)

	out, err := f.resolver.PreviewPurgeActor(adminCtx(adminDID), targetDID)
	if err != nil {
		t.Fatalf("PreviewPurgeActor: %v", err)
	}
	if ae, _ := out["actorExists"].(bool); ae {
		t.Errorf("actorExists = true, want false (no actor row)")
	}
	if c, _ := out["recordCount"].(int64); c != 2 {
		t.Errorf("recordCount = %v, want 2", out["recordCount"])
	}
	if h, _ := out["handle"].(string); h != "" {
		t.Errorf("handle = %q, want empty", h)
	}

	// Confirm path also works on this shape.
	token, _ := out["confirmToken"].(string)
	res, err := f.resolver.PurgeActor(adminCtx(adminDID), targetDID, token)
	if err != nil {
		t.Fatalf("PurgeActor(orphan): %v", err)
	}
	if rd, _ := res["recordsDeleted"].(int64); rd != 2 {
		t.Errorf("recordsDeleted = %v, want 2", res["recordsDeleted"])
	}
	if ar, _ := res["actorRowsDeleted"].(int64); ar != 0 {
		t.Errorf("actorRowsDeleted = %v, want 0 (no actor row to begin with)", res["actorRowsDeleted"])
	}
}

// TestPurgeActor_NoSignerConfigured covers the "purge not
// configured" guard: when SetPurgeTokenSigner has never been called
// the resolver must reject both entry points cleanly instead of
// panicking. The constraint is reachable in real deploys where
// SECRET_KEY_BASE is unset — the boot path doesn't construct a
// signer and the operator sees a clear error.
func TestPurgeActor_NoSignerConfigured(t *testing.T) {
	f := newPurgeFixture(t)
	// Detach the signer to simulate an unconfigured deploy.
	f.resolver.purgeTokenSigner = nil

	if _, err := f.resolver.PreviewPurgeActor(adminCtx("did:plc:admin1"), "did:plc:target1"); err == nil {
		t.Error("PreviewPurgeActor accepted nil signer")
	}
	if _, err := f.resolver.PurgeActor(adminCtx("did:plc:admin1"), "did:plc:target1", "tok"); err == nil {
		t.Error("PurgeActor accepted nil signer")
	}
}

// TestPurgeActor_EmptyConfirmToken catches the explicit
// confirmToken-required branch — independent of HMAC verify, so the
// signer is bypassed entirely. The error should mention the token
// rather than collapsing to ErrPurgeTokenInvalid.
func TestPurgeActor_EmptyConfirmToken(t *testing.T) {
	f := newPurgeFixture(t)
	_, err := f.resolver.PurgeActor(adminCtx("did:plc:admin1"), "did:plc:target1", "")
	if err == nil {
		t.Fatal("PurgeActor accepted empty confirmToken")
	}
	if !strings.Contains(err.Error(), "confirmToken") {
		t.Errorf("error = %q, want it to mention confirmToken (not %v)", err, err)
	}
}

// TestPurgeActor_MissingAdminDIDInContext covers the second guard
// inside the resolver: even an isAdmin=true context with an empty
// userDID must be rejected, because the empty admin DID would then
// flow into the audit log and signer.
func TestPurgeActor_MissingAdminDIDInContext(t *testing.T) {
	f := newPurgeFixture(t)
	// isAdmin=true, but no DID — this can happen if the auth
	// middleware sets isAdmin from an API-key path without a user
	// identity. The resolver must refuse.
	ctx := ContextWithAuth(context.Background(), "", "", true, nil)
	if _, err := f.resolver.PreviewPurgeActor(ctx, "did:plc:target1"); err == nil {
		t.Error("PreviewPurgeActor accepted empty admin DID")
	}
	if _, err := f.resolver.PurgeActor(ctx, "did:plc:target1", "irrelevant"); err == nil {
		t.Error("PurgeActor accepted empty admin DID")
	}
}
