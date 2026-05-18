# 02 — Testing-Quality Findings

Date: 2026-05-18 (overnight pass).
Scope: failure-mode coverage on load-bearing paths, tautological
tests, brittle implementation pinning, setup-only tests, regression
gaps for recently-fixed bugs, suite story / discoverability, shared-
state flake hazards.
Calibration:
- high = load-bearing path with NO failure-mode coverage; a bug
  here is invisible to CI.
- medium = happy-path covered but the failure path that's most
  likely to ship is uncovered.
- low/nit = preference / cosmetic.

Hard ceiling: 15. Honest empties valued — see end of file for the
lenses that produced no finding.

Per-finding format: title, severity, location (production code,
not the missing test), problem (specific failure mode uncovered),
realistic likelihood, proposed fix (specific test shapes), effort,
risk, reversibility.

---

### T-1: `ActivityCleanupWorker.Start` has no test that exercises the actual ticker loop — the existing test reimplements a correct version of the bug
**Severity:** high
**Location:** `internal/workers/activity_cleanup.go:33-60` (production
code with the C-1 stopped-ticker bug); test at
`internal/workers/workers_test.go:229-298` (the tautology).
**Problem:** `TestActivityCleanupWorker_StartStop` does NOT call
`w.Start()`. It manually constructs an `ActivityCleanupWorker` literal
and runs its own goroutine with `time.NewTicker(w.interval)` declared
INSIDE the goroutine (lines 274-275) — i.e., the correct version of
the code. The test then verifies `w.Stop()` returns. So even though
production's `Start()` has the stopped-ticker bug (`defer
ticker.Stop()` fires the moment `Start()` returns, killing the
ticker), this test passes. The bug C-1 catalogues as "the activity
cleanup never runs after startup" would slip through CI even if
someone added an assertion about ticker firing — because the
production path simply isn't exercised. Comment at line 234 explicitly
acknowledges this is a workaround for the testutil cycle. Net: the
test is a pure tautology.
**Realistic likelihood:** the bug is already shipped on staging today.
The test gave operators false confidence that the worker is
exercised by CI.
**Proposed fix:** add an _test-package test in `internal/workers/`
(import cycle is avoidable — the test only needs an `Activity` repo,
or a tiny fake satisfying the cleanup-method subset, or a build-tag
`integration_test.go` that uses `testutil.SetupTestDB`). Test
shape: construct the real worker with a 20ms interval, run
`w.Start(ctx)`, wait ~80ms, verify `cleanup` was invoked ≥3 times
(via a counted-call wrapper on the Activity), then call `w.Stop()`.
The C-1 bug would fail this test (cleanup count = 1, the boot-time
invocation, not 3+). Delete the existing reimplementation test.
**Effort:** S
**Risk:** low
**Reversibility:** easy

---

### T-2: `jetstream.Consumer` and `jetstream.Client` have ZERO tests; the only `_test.go` in the package covers event-parsing
**Severity:** high
**Location:** `internal/jetstream/consumer.go` (11 functions, 350
LOC); `internal/jetstream/client.go` (9 functions). The sole test
file is `internal/jetstream/event_test.go` covering `ParseEvent`,
`IsCommit`, `IsCreate`, `IsUpdate`, `IsDelete`, `CommitEvent.URI`
— pure event-DTO methods.
**Problem:** None of the consumer's load-bearing behaviour is
covered: `Start` / `startInternal` (reconnect spawning, flusher
launch — the C-2 leak); `processEvents` (the cursor-advance-on-
success contract per C-17 / C-28); `Stop` (the C-3 close-on-events
race); `UpdateCollections` (the lexicon-upload restart path that
fires on every admin upload); `loadCursor` / `saveCursor` (round-
trip integrity of the int64 ↔ JSON-encoded config value).
Specifically: the cursor advance logic (the brief's question 1) is
NOT test-covered. A regression that moved `c.cursorFlusher.SetCurrent`
above the `ProcessRecord` call would advance the cursor on failure
and silently drop records — and no test would catch it.
**Realistic likelihood:** every code change touching jetstream
ships untested. The package has shipped 30+ commits since cursor
flusher was introduced; the brief's correctness pass identified
two real bugs in this file (C-2, C-3). The risk of the next bug is
not hypothetical.
**Proposed fix:** add `consumer_test.go` with at minimum:
(a) `TestConsumer_CursorAdvancesOnlyAfterSuccess` — fake processor
returns error on a sentinel collection, then success on the next;
assert `cursorFlusher.GetCurrent()` after the failure is the
PREVIOUS event's TimeUS (not the failing one).
(b) `TestConsumer_NonCommitAdvancesCursor` — identity event flows
through to `SetCurrent` even though no record is written (pins
C-17's intentional behaviour).
(c) `TestConsumer_MalformedRecord_DoesNotCrash` — a commit with a
non-JSON `Record` body still leaves the consumer alive (recover
contract — currently absent from main.go's launcher, see C-5).
(d) `TestConsumer_LoadCursor_RoundTrip` — Save then Load returns
the same int64; an unparseable config value returns (0, error) not
silent zero. Decouple the test from real DB via the `configRepo`
interface (it's already an interface boundary).
**Effort:** M (need a small fake Client that emits events on a
channel — the existing `Client.Events()` is already a `<-chan
Event`).
**Risk:** low
**Reversibility:** easy

---

### T-3: `BatchInsert` has no test that proves "partial batch failure rolls everything back"
**Severity:** medium
**Location:** `internal/database/repositories/records.go:182-213`
(uses `tx.Rollback()` via defer; commits only after all batches
succeed). Test at `records_test.go:137-201`.
**Problem:** The existing test covers empty/1/5-record happy paths
and verifies each record is retrievable after commit. There is NO
test that (a) one record in the middle of the batch fails (e.g.,
violates a NOT NULL constraint or has invalid JSON) AND (b)
records BEFORE the failure are absent from the table after the
error. The implementation's contract (single-transaction, all-or-
nothing) is unverified. A future refactor that switched to per-
record commits, or that moved the transaction lifecycle outside
this method, would not fail any existing test.
**Realistic likelihood:** Low in steady state (callers feed validated
records); medium during backfill (`internal/backfill/`) which is the
primary BatchInsert consumer and is exactly where partial-failure
semantics matter most — a partial-batch insert during backfill
recovery would leave the operator unable to deduce where backfill
stopped.
**Proposed fix:** add `TestRecordsRepository_BatchInsert_PartialFailureRollsBack`:
seed 3 valid records + 1 with an invalid JSON body (`{"unclosed`) +
1 more valid; call `BatchInsert`; assert (a) error is non-nil,
(b) `GetByURIs` for the first 3 URIs returns empty, (c) the table's
COUNT(*) is zero. The Postgres JSONB parse will fail the whole
multi-VALUES INSERT, which is the all-or-nothing semantic worth
pinning.
**Effort:** S
**Risk:** low
**Reversibility:** easy

---

### T-4: `buildFilterGroupRecursive`'s `MaxFilterDepth` boundary is not directly tested — only the extractor-side cap is
**Severity:** medium
**Location:** `internal/database/repositories/filter.go:342-345`
(the `depth > MaxFilterDepth` check inside the SQL builder itself).
**Problem:** The extractor-side cap (`where.go:384-386`) has two
tests (`TestExtractFieldFilters_NestedBadgeWhere_DepthCap`,
`TestExtractFieldFilters_NestedItemsWhere_DepthCap`). But the
defense-in-depth cap inside `buildFilterGroupRecursive` is not
directly tested by any caller that constructs a deeply-nested
`FilterGroup` programmatically and calls `BuildFilterGroupClause`.
The brief's question (1c) — "what happens when MaxFilterDepth is
exceeded by exactly 1" — is covered ONLY when the extractor is the
construction path. A programmer-built `FilterGroup` (the awardCount
resolver, future derived fields, future internal use) could exceed
the cap and rely on this SQL-builder guard, which has no
regression test. Also: `MaxFilterConditions` has two great
boundary tests (Joined/Arrays at cap+1), but no test at exactly the
cap (proving cap+0 is allowed, cap+1 rejected — boundary pinning
in the standard sense).
**Realistic likelihood:** today the extractor is the only construction
path, so the builder cap is belt-and-suspenders. Medium for the
future: the brief calls out the registry-first pattern and a new
derived field could synthesise a FilterGroup. If the builder cap is
silently broken, the extractor cap still holds — so the exposure is
"defense in depth is undefended."
**Proposed fix:** add `TestBuildFilterGroupClause_DepthCapEnforcedDirectly`
in `filter_unit_test.go`: programmatically construct a `FilterGroup`
with `Children` nested MaxFilterDepth+1 deep (each child is a one-
leaf group), call `BuildFilterGroupClause`, assert the error
contains "exceeds maximum depth". Also add the symmetrical
`TestBuildFilterGroupClause_MaxConditionsAtBoundary`: exactly
`MaxFilterConditions` leaves should succeed; `MaxFilterConditions+1`
should fail. The existing CapEnforced tests are at cap+1 only;
proving cap itself succeeds prevents an off-by-one drift in either
direction.
**Effort:** S
**Risk:** low
**Reversibility:** easy

---

### T-5: `RecordsRepository.Insert` has no test for malformed JSON in the record body, statement_timeout, or pool-exhausted failure modes
**Severity:** medium
**Location:** `internal/database/repositories/records.go:129-178`
(`Insert` + `InsertWithParams`). Tests at `records_test.go:43-135`.
**Problem:** The existing tests cover the three happy-path
results (`Inserted` new, `Skipped` same-CID, updated different-CID).
Missing:
(a) Malformed JSON body — `Insert(ctx, uri, cid, did, col, "{not
json")`. The Postgres `::jsonb` cast will reject; the function
returns (Skipped, err). No test pins the returned `InsertResult`
vs. the error semantics. A future refactor that changed the result
on error from `Skipped` to `Inserted` (because of the named-return
default) would silently regress.
(b) Context-cancelled mid-statement. The CI's per-DB
`statement_timeout` is 30s; tests run with 0 (no cap, per
`testutil/db.go:111`), so this codepath is genuinely never
exercised in CI.
(c) The brief's pool-exhausted scenario — equally untested. The
function relies on pgx returning a pool-exhaustion error, which the
caller (ingestion processor) translates to a metric. No test pins
that contract.
**Realistic likelihood:** (a) medium — every malformed jetstream
event flows here through `processor.SanitizeRecord`, but a corrupt
record body that passes the sanitizer (top-level object) could
still be invalid JSONB (e.g., `{"text": "\uD83D"}` — an isolated
high surrogate). (b)/(c) low in steady state, high during
incidents — which is exactly when the contract matters most.
**Proposed fix:** add three small tests:
(1) `TestRecordsRepository_Insert_MalformedJSON_ReturnsError` —
asserts error is non-nil and result is `Skipped` (or whatever the
intended contract is — pinning either is better than nothing).
(2) `TestRecordsRepository_Insert_CancelledContext_ReturnsError` —
context.WithCancel cancelled immediately; assert ctx.Err() in the
returned error chain via `errors.Is`.
(3) Skip pool-exhaustion as a direct test (hard to stage). Cover
indirectly via a "fake executor that returns ErrPoolExhausted"
test if the executor interface allows.
**Effort:** S (each)
**Risk:** low
**Reversibility:** easy

---

### T-6: `TestExtractFieldFilters_NestedJoined_Rejected` (#87) and `TestExtractFieldFilters_NestedArrayWhere_Rejected` (#88) are acknowledged tautologies — and `TestArrayWhereRegistry_CollectionItems`'s negative entries fall into the same pattern
**Severity:** medium
**Location:** `internal/graphql/schema/where_test.go:408-435` (#87),
`where_test.go:662-679` (#88), and the "negative entries" tail of
`where_test.go:483-488` + `where_test.go:272-276`.
**Problem:** The wave-2 directive already calls out the two
explicit tautologies (#87/#88 nested-rejected). Two further tests
match the same pattern in spirit — `TestArrayWhereRegistry_CollectionItems`
and `TestJoinedWhereRegistry_BadgeAwardBadge` each end with a loop:
"the registry must not have entries for these other lexicons." That
loop asserts the absence of a registration that was never added; it
fails only if a new registration appears for one of those specific
lexicons. With a 2-entry registry today, the loop is testing the
test, not the code: if someone added a third registry entry under
`org.hypercerts.collection.title`, the negative loop wouldn't fire
because it doesn't enumerate that lex/field pair. The intended
intent — "the registry is closed to additions without test
review" — is unreachable from this shape.
**Realistic likelihood:** the explicit tautologies are sleeping
until a second registry entry lands. The negative-entry loops are
already not testing anything load-bearing.
**Proposed fix:** two parts.
(a) Delete `TestExtractFieldFilters_NestedJoined_Rejected` and
`TestExtractFieldFilters_NestedArrayWhere_Rejected`. Add a TODO
comment at the registry-lookup site (`where.go:435`,
`where.go:473`) saying "extend with a real two-level payload when
a second registry entry lands whose target/element type also
participates in joined/array-where." The acknowledgment is already
in the test bodies — moving it to the production source is the
honest place.
(b) Replace the negative-entry loops with a single
`TestRegistryEntryCount` that asserts `len(joinedWhereRegistry) ==
N` (currently N=1) and `len(arrayWhereRegistry) == 1`. Any addition
becomes a test fail with a clear message ("update this test AND
add a coverage test for the new entry"), which is the actual
intent.
**Effort:** S
**Risk:** low
**Reversibility:** easy

---

### T-7: `tap.Consumer` has no tests — the C-15 dispatch budget (120s worst-case retry math) and the per-event recover are unverified
**Severity:** medium
**Location:** `internal/tap/consumer.go` (the `Run` + `dispatch`
loop). The only `_test.go` in the package covers `ParseEvent`.
**Problem:** Combined with T-2 (jetstream consumer is also
untested), the entire ingestion edge of the indexer ships with
NO consumer-level tests. Specifically uncovered:
(a) The per-event recover in `dispatch` (the brief's C-29 cousin
for the tap side) — if it stops recovering, one bad event crashes
the consumer. No test pins the recover.
(b) The retry / timeout math (C-15) — 30s × 4 attempts. A future
edit that changes the retry count or the per-attempt timeout has
no regression guard. Operators rely on the documented "max 120s
stall" behaviour for incident playbooks.
(c) The ack-on-success contract — if `dispatch` returns success
but the ack-write fails, the upstream redelivers. Today's code
proceeds to the next event regardless; no test pins that.
**Realistic likelihood:** medium. Tap is the optional cryptographic-
verified sidecar; less hot than jetstream but takes a different
code path. The next bug here is invisible to CI.
**Proposed fix:** add `consumer_test.go` with a fake `conn`
satisfying the interface that produces events on demand. Three
tests minimum: panic-recover (handler panics → next event is still
read), retry-then-fail (handler returns error 4× → ~7s elapsed,
exact count depends on backoff), success-after-retry (handler
fails 2× then succeeds → ack fires once). All testable in <1
second wall time via a stub `time.Sleep` or short backoff values.
**Effort:** M (requires a `conn` interface — may need a tiny
refactor to extract it).
**Risk:** medium (the refactor risks regressing real behaviour
for testability; consider keeping the existing direct conn usage
and adding the seam minimally).
**Reversibility:** easy

---

### T-8: `connection.totalCount` resolver has no test — the P-1 filter-ignoring bug ships untested
**Severity:** medium
**Location:** `internal/graphql/schema/builder.go:762-769` (the
totalCount resolver), `internal/graphql/query/connection.go:286`
(the field definition).
**Problem:** P-1 in the performance findings catalogues that
`GetCollectionCount` is called regardless of `where` / `authors` /
`labels` filters, producing wrong counts. The brief's question 1
asks: "is the cursor advance logic test-covered?" — for the
totalCount resolver, the answer is no. The test suite has no
GraphQL-level test that issues a filtered connection query with
`totalCount` selected and asserts the returned count matches the
filtered edge count. The bug (counter and pager disagree) is
exactly the kind a test would catch trivially. Note: P-1 is
already an open finding in the performance pass; this finding
addresses the *test gap* — even after P-1 is fixed, a regression
that re-introduces the unfiltered path has no guard.
**Realistic likelihood:** the bug is shipped. The fix in P-1 needs
a regression guard.
**Proposed fix:** as part of the P-1 fix, add
`TestConnectionResolver_TotalCount_RespectsWhereFilter` in a new
`internal/graphql/query/connection_integration_test.go` (or fold
into an existing `_test.go` that already has a schema/registry
fixture): seed 10 records, half matching a filter; issue a GraphQL
query with `where: {...}, first: 2 { totalCount, edges { uri } }`;
assert `totalCount == 5`, `len(edges) == 2`.
**Effort:** M (needs a schema+repo+resolver test harness — but
similar harnesses already exist for the awardCount resolver tests).
**Risk:** low
**Reversibility:** easy

---

### T-9: Process-level race tests use panic-as-signal — brittle and confusing
**Severity:** low
**Location:** `internal/ingestion/processor_test.go:62-83`
(`TestProcessOp_NilAllowlistAllowsAll`) and
`internal/ingestion/processor_test.go:164-183`
(`TestProcessOp_DeleteSkipsJSONValidation`).
**Problem:** Both tests construct a `RecordProcessor{}` with nil
repositories, then assert that a panic occurs ("got the expected
panic — allowlist check was skipped"). This couples the test to
the panic shape of the production code path — a future change that
made `Actors.Upsert(ctx, nil-DID)` return an error instead of
panicking, or that added a nil-check in `ProcessRecord`, would
silently break the test's signal without breaking the contract it
claims to verify (the allowlist OR delete-JSON-check skipping). The
test ends up coupled to a side-effect (panic) rather than the
observable contract (allowlist passed → next step ran).
**Realistic likelihood:** any tightening of the processor's nil-
safety is the easy refactor that breaks these tests for the wrong
reason.
**Proposed fix:** replace with a tiny fake `ActorsRepository` whose
`UpsertWithPDS` records whether it was called. Assert
`fakeRepo.upsertCalls == 1` in the positive case, `== 0` in the
allowlist-blocked case. Removes the panic-as-signal coupling and
the test failure message becomes "Upsert was not called" instead
of "expected panic from nil repos." Same shape for the delete
path: a fake `Records.Delete` that records calls. The
`RecordProcessor` already uses interface-typed fields for testability
— this is a minor follow-through.
**Effort:** S
**Risk:** low
**Reversibility:** easy

---

### T-10: `cursor.Flusher.Run` has no test for concurrent re-runs (the C-2/C-13 hazard) and no test for save errors
**Severity:** low
**Location:** `internal/cursor/flusher.go:47` (the `Run` loop). Tests
at `flusher_test.go:10-108`.
**Problem:** Two narrow gaps:
(a) The C-2 / C-13 finding established that calling `Run` multiple
times in parallel produces N flushers each tracking its own
`lastFlushed`. There's no test that pins the intended single-run
contract — either by `Run` returning immediately on re-entry, or by
documenting that the caller must guarantee single-Run. Today both
contracts are unstated and unenforced.
(b) The save function can return an error (the configRepo write
fails). All four tests pass a save function that returns nil. The
error-handling branch in `Run` (currently: log and continue) is
untested. A regression that propagated the error and exited Run
silently would leave the consumer alive with no cursor save loop.
**Realistic likelihood:** (a) sleeps until C-2 is fixed; the fix
would benefit from this test. (b) low — DB writes for cursor save
rarely fail in steady state.
**Proposed fix:**
(a) After C-2 is fixed, add a test that asserts the chosen
contract — either two concurrent `Run` goroutines result in one
exiting immediately, OR document that concurrent Run is
caller-policed and add a `// MUST NOT be called concurrently`
comment + a `sync.Once`-style guard.
(b) Add `TestFlusher_SaveError_KeepsLoopAlive`: save returns an
error twice then succeeds; assert the loop kept ticking and the
final cursor was saved. Verifies the "log and continue" policy.
**Effort:** S
**Risk:** low
**Reversibility:** easy

---

### T-11: `UploadLexicons` has no test — the C-4 partial-success deploy-wedge has no regression coverage path
**Severity:** medium
**Location:** `internal/graphql/admin/resolvers_lexicons.go:73-174`.
The admin test files cover handler / auth / purge but not the
upload resolver.
**Problem:** C-4 in the correctness pass catalogues the staged
write loop (validate-all then persist-each, returning early on
per-row failure). Beyond C-4's specific bug, the entire upload
flow has no test: the zip extraction, the JSON parse loop, the
validation callback dispatch, the per-row persistence, the restart
callback. The most-likely failure modes — a malformed zip
("zip: not a valid zip file"), a lexicon with an invalid schema
(should fail validation, not persist), and the C-4 partial-write —
are all untested. Operators have no way to know the resolver works
beyond manual smoke tests against the running admin endpoint.
**Realistic likelihood:** medium. Lexicon uploads are operator
actions during maintenance windows, but the project has automation
around them (npm + admin GraphQL per the orientation doc) and the
flow has shipped 30+ entries. The C-4 fix will need a test;
adding the broader coverage at the same time is cheap.
**Proposed fix:** add `resolvers_lexicons_test.go`. Three tests
minimum:
(a) `TestUploadLexicons_HappyPath` — well-formed zip with 2 lexicons,
mock Lexicons.Upsert, assert both upserts called, restart callback
called exactly once.
(b) `TestUploadLexicons_MalformedZip_NoUpsertNoRestart` — base64
of `not a zip`, assert error, assert 0 upserts and 0 restart calls.
(c) `TestUploadLexicons_PartialPersistFailure_DoesNotRestart` —
mock Lexicons.Upsert to succeed for lex1 and fail for lex2;
assert error AND restart callback NOT called (today fails because
the code doesn't roll back lex1; this test is the regression guard
once C-4 is fixed).
**Effort:** M
**Risk:** low
**Reversibility:** easy

---

### T-12: `subscription.PubSub`'s slow-subscriber drop policy (C-12) is not pinned by any test
**Severity:** low
**Location:** `internal/graphql/subscription/pubsub.go:91-111` (the
non-blocking send with default-drop). Tests at `pubsub_test.go`.
**Problem:** Six tests cover Subscribe/Unsubscribe, Publish,
filtering, concurrency. None verify the non-blocking-drop
contract: if a subscriber's `Events` channel is full, Publish
must NOT block and must drop the event. A future change that
removed the `default:` branch (e.g. someone "fixing" the silent
drop by switching to a blocking send) would silently regress —
one slow subscriber would block all Publish calls, stalling the
whole pubsub. The C-12 finding builds on this gap by recommending
a force-disconnect policy; either way, the current drop
behaviour is contract that should be pinned.
**Realistic likelihood:** low for "someone changes the policy",
but the failure mode (one slow subscriber stalls all subscribers)
is exactly the kind of cross-tenant impact that's a 3am page.
**Proposed fix:** add `TestPubSub_Publish_DropsWhenBufferFull`:
subscribe with a buffer size of 1, publish 1 event without reading
(fills the buffer), publish 100 more events with a deadline,
assert no event-publish call blocked > 1ms and that the subscriber's
channel still has exactly 1 buffered event. Cheap, pure-Go, no DB.
**Effort:** S
**Risk:** low
**Reversibility:** easy

---

### T-13: `records_filter_test.go` (1,971 LOC, 52 tests) lacks any orienting comment — a new contributor has no map of the contract
**Severity:** low
**Location:** `internal/database/repositories/records_filter_test.go:1-12`
(the file header — currently bare package+imports).
**Problem:** The brief's question 6 asks whether the test suite
tells a coherent story. The file is the largest test file in the
repo and reads, top-to-bottom, as a flat list: AuthorsNil_MatchesAll
→ AuthorsEmpty → AuthorsSingleDID → AuthorsAtCap → … → Contributor
… → BadgeAwardSubject … → Eqi … . The naming is consistent
within each section but the section boundaries are implicit (no
section comment, no group prefix beyond the test name). A new
contributor opening this file gets no signal about what's covered
where. By contrast, `filter_unit_test.go` (1,684 LOC) has
descriptive comment headers per test that read as a story.
**Realistic likelihood:** every contributor opening this file
pays the orientation cost. With a 1.9k LOC file, that's 5–10
minutes per visit per person.
**Proposed fix:** add a file header comment listing the test
sections in order (`// Authors filter: 40-220`, `// PDS-exclude:
326-450`, `// Contributor filter: 550-915`, `// BadgeAwardSubject
generated column: 1039-1198`, `// Contributor production shapes:
1200-1485`, `// Case-insensitive eqi/ini operators: 1484-1893`,
`// Recursive Validate: 1905-1971`). Optionally split the file by
section once a follow-up makes physical sense; not in scope for
the immediate fix.
**Effort:** S
**Risk:** low
**Reversibility:** easy

---

### T-14: Shared-Postgres test state is not a t.Parallel hazard today, but is undocumented and one t.Parallel away from CI flake
**Severity:** low
**Location:** `internal/testutil/db.go:43-90` (`SetupTestDB` — the
shared-DB harness) + `.github/workflows/ci.yml:50-66` (the `-p 1`
workaround).
**Problem:** Every DB-using test in the repo touches the same
`hypergoat_test` schema. `resetBetweenTests` runs `DELETE FROM` on
every mutable table at the start of each test, so single-threaded
test order works. The CI uses `-p 1` to serialise packages, with a
clear comment explaining why. But:
(a) NOTHING in the test source files (or in `testutil/db.go`)
warns against `t.Parallel()`. A contributor adding `t.Parallel()`
to a DB-using test would get green CI on their machine (one
package, one test running parallel-with-itself is fine when
`SetupTestDB` truncates upfront) and then flake under load. The
operator-visible failure would be intermittent, hard to attribute.
(b) `TestMigrations_Rollback` (`internal/database/migrations/migrations_test.go:161`)
deliberately leaves the schema in a partially-rolled-back state.
The package-local `newTestExecutor` drops all tables next call, so
within-package ordering self-heals. But: a developer running
`go test ./internal/database/migrations/... ./internal/database/repositories/...`
without `-p 1` would observe the repositories package's migration
re-run hitting a half-rolled-back schema — confusing failure mode,
hard to diagnose.
**Realistic likelihood:** the brief mentioned reviewers flagging
flakes for TestPurgeTokenSigner, TestPurgeActor, TestMigrations_Rollback.
Today's `-p 1` masks them. Tomorrow's contributor adds t.Parallel
and the flakes return.
**Proposed fix:** add a header comment in `testutil/db.go`
spelling out the contract: "This helper shares a single Postgres
DB across all tests; DO NOT call `t.Parallel()` in a test using
this helper; CI relies on `-p 1` for cross-package serialisation."
Optionally: detect `t.Parallel()` at test start (Go's `testing`
package doesn't expose this directly, but a fail-fast `t.Cleanup`
could check `testing.CoverMode()` or use a tiny build-tag guard).
The header comment is the minimum-viable warning.
**Effort:** S
**Risk:** low
**Reversibility:** easy

---

### T-15: `labeler.Consumer` has no tests for `processEvents`, `upsertLabels`, `handleLabelMessage`, or the C-3 close-on-events race
**Severity:** medium
**Location:** `internal/labeler/consumer.go` (26 functions, 720 LOC).
Tests in the package cover `validateLabel`, `parseLabelTime`, and
frame decoding — pure functions only.
**Problem:** Parallel to T-2 (jetstream) and T-7 (tap). The labeler
consumer is the third ingestion edge, with the largest LOC and
most internal state: cursor management, backfill, known-vals LRU
(C-10), per-generation `genDone` (the C-3 right-pattern that
jetstream got wrong), pause/resume via admin endpoint. None of
that logic is exercised. Specifically: the brief's "reconnect-
mid-event" failure mode for the labeler is uncovered; the C-3
analogue (Stop close-on-events race) is uncovered; the C-10 LRU
slice-front leak is uncovered. Combined T-2 + T-7 + T-15: the
entire ingestion edge ships untested at the consumer level.
**Realistic likelihood:** the labeler subsystem has shipped most
of the recent changes (#79, #84 in the open-issues list per the
orientation). Every change ships untested.
**Proposed fix:** add `consumer_test.go` with at minimum:
(a) `TestConsumer_HandleLabelMessage_InsertsValidLabel` —
construct a label message, fake `Labels.Insert`, assert insert
called once with the right params.
(b) `TestConsumer_HandleLabelMessage_RejectsInvalidLabel` —
invalidates via `validateLabel`, assert Insert NOT called and
rejected counter incremented.
(c) `TestConsumer_KnownValsCacheBoundedToMax` — push 2×MaxKnownVals
distinct vals, assert `len(c.knownVals) <= MaxKnownVals`
(regression guard for C-10's bound, which holds; the slice-leak
itself is a separate concern).
(d) `TestConsumer_Stop_NoCloseOnEventsRace` — start, push events
fast, call Stop concurrently, assert no panic from a closed-channel
send. The per-generation `genDone` pattern means this should pass
today; the test pins the contract so future refactors can't break
it (the C-3 case where jetstream got the wrong shape).
**Effort:** M (needs a fake `Client` satisfying the labeler
client surface).
**Risk:** medium (consumer is complex; tests may need careful
isolation).
**Reversibility:** easy

---

## Severity tally

| Severity | Count |
|----------|------:|
| high     | 2     |
| medium   | 8     |
| low      | 5     |
| **total** | 15    |

## Lenses with no material finding (honest empties)

- **Regression coverage for #86 R1.1 (predicate_implied_by) and
  #89 R1.1 (signature mismatch).** Solid drift tests exist:
  `TestCountAwardsByBadgeURI_IndexExpressionMatchesMigration030`
  cross-checks both the JSON-path expression and the collection
  literal between migration 030 and `records.go`. Same pattern
  for migrations 026/029 (`buildStringSubjectFilter` drift test).
  These pins ARE the load-bearing kind the brief calls out as
  GOOD pinning.
- **Regression coverage for #87 R1.1 (CountConditions cap bypass
  via Joined) and #88 R1.1 (via Arrays).** Both have explicit
  end-to-end tests (`TestJoinedFilter_CountConditions` +
  `TestBuildFilterGroupClause_JoinedFilter_CapEnforced`;
  `TestArrayFilter_CountConditions` +
  `TestBuildFilterGroupClause_ArrayFilter_CapEnforced`). The
  cap+1 boundary is tested; the cap+0 boundary is the only
  off-by-one gap (rolled into T-4).
- **Brittle log-message pinning.** Searched for tests asserting
  on `slog` message strings or comment contents — none found.
  The drift tests assert on SQL fragments (load-bearing) and
  schema descriptions (consumer-facing contract), both correctly
  pinned.
- **Setup-only tests.** Walked the top-50 test bodies looking for
  the pattern "construct → assert no error → return." The
  `TestSetupTestDB` shape (which would be the natural candidate)
  doesn't exist — `SetupTestDB` is called by every other test, so
  any breakage manifests as that test failing, which is the right
  signal. No standalone setup-only tests found.

## Top three to fix in the morning

1. **T-1** (ActivityCleanupWorker tautology) — directly enables C-1
   to ship undetected. Replace with a real Start-exercising test;
   that test will fail until C-1 is fixed, then guard the fix.
2. **T-2** (jetstream consumer untested) — the most-load-bearing
   subsystem with zero unit coverage; the cursor-advance-on-success
   contract is the highest-stakes one.
3. **T-3** (BatchInsert partial-failure) — single small test, pins
   the all-or-nothing transaction contract the implementation
   promises but doesn't verify.

T-6 (tautology cleanup) is the highest-leverage zero-cost win: deleting
the two acknowledged-tautology tests and adding a registry-size pin
is ~10 lines and removes ongoing maintenance overhead.
