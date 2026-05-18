# Final-pass correctness review — 2026-05-18

Cold review of `git diff origin/main..HEAD` (22 commits, ~30 files,
~1100 LOC removed, ~750 LOC added). I did not read any of the prior
overnight findings docs before walking the diff. Gates executed:

| Gate                                                  | Result   |
| ----------------------------------------------------- | -------- |
| `GOARCH=arm64 go build ./...`                         | clean    |
| `GOARCH=arm64 go vet ./...`                           | clean    |
| `GOARCH=arm64 golangci-lint run ./...`                | 0 issues |
| `CGO_ENABLED=1 GOARCH=arm64 go test -race -short -count=1 ./...` | 2 failures in `internal/graphql/admin` — `TestPreviewPurgeActor_EmptyActorOK` and `TestPurgeTokenSigner_RejectsTamperedSignature`. Both pass in isolation; these are the known TestPurge* shared-DB flakes the task notes as pre-existing. No new failures. |

Grep audits requested by the task:

| Grep                                                                                                             | Result                                                                                                                                                          |
| ---------------------------------------------------------------------------------------------------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `OAuthError\|GenerateClientID\|RequireScope\|UseJTI\|CleanupExpiredJTIs`                                         | Only `writeOAuthError` (private method in `internal/server/oauth_handlers.go`) — unrelated to the deleted `OAuthError` type. No dangling references.            |
| `BuildFieldFilterClause\|IsValidNSID\|IsDIDPLC\|IsDIDWeb\|DecodeError`                                           | No matches in the main tree (only in `.claude/worktrees/*` which are stale orphan checkouts, ignored).                                                          |
| `GetCollectionCount`                                                                                             | Two intentional callers in `records.go`: the unfiltered `GetCollectionCount` itself and the fast-path delegation from `CountByCollectionFiltered`. One test reference. One comment reference in `builder.go`. All expected. |
| `GenerateClientSecret\|GenerateDPoPNonce\|GeneratePARRequestURI\|IsExpiredWithSkew\|WithMaxDPoPAge\|AccessTokenFromContext\|ScopesFromContext\|ErrInsufficientScope\|ValidateScopeFormat\|ContainsScope\|IsScopeSubset\|FilterScopes\|JoinScopes` | No matches in main tree.                                                                                                                                        |

All four OAuth deletion commits (M1-M4) are clean — no dangling
references in production or test code.

Cross-commit interaction analysis (M5 + M7 + M8, M9 + M6, M2/3/4 vs
M5/M8) walked manually. M7's per-generation context composes correctly
with M8's defer-close-events: when client.Run returns (Stop, ctx
cancel, or websocket error), the deferred close(c.events) fires →
processEvents exits via channel close → deferred genCancel() fires →
cursor flusher reads genCtx.Done() and finalFlushes. The redundant
finalFlush on each reconnect is benign (idempotent write of the same
atomic cursor value). UpdateCollections and Stop both compose
correctly with the new shape. No goroutine leak, no panic vector
introduced.

M6's `validateCollectionName` regex (`^[a-z][a-z0-9]*(\.[a-z][a-zA-Z0-9-]*){2,}$`)
correctly rejects all 14 SQL-injection payloads in its test table and
accepts every production NSID shape. The 3-segment minimum is correct
for NSIDs. The 256-char cap is well above any real NSID. Inclusive
boundary on `MaxFilterDepth` (M5 add-on test W12) verified — `depth >
MaxFilterDepth` means depth=3 passes, depth=4 fails.

The OAuth deletion commits do not touch any consumer-goroutine code;
no incidental coupling with M5/M7/M8.

## Findings

Three concrete findings, all low/nit severity. Nothing material that
the prior reviewers missed. The diff is in good shape.

---

### F-1: misleading "comes from lexicon registry" comment on CountByCollectionFiltered
**Severity:** low (comment-only; no functional bug)
**Commit:** `3ea2c55 fix(repo,schema): totalCount respects filters — correctness defect`
**Location:** `internal/database/repositories/records.go:806-811`
**Concern:** The comment says "No collection-name validation: this method is internal to the resolver layer; collection arrives via the GraphQL resolver which sourced it from the lexicon registry." That is false for one of the two call paths. `createCollectionResolver` (`builder.go:943`) does source `collection` from the lexicon registry (passes `lexiconID`), but `createGenericRecordsResolver` (`builder.go:921`) calls `resolveRecordConnection(p, collection, ...)` with `collection := p.Args["collection"].(string)` — pure user input from the public `records(collection: String!)` query.

The code is in fact safe: every use of `collection` inside `CountByCollectionFiltered` / `buildFilteredWhereForCount` flows through `ph()` parameter binding (`r.collection = $1`), so SQL injection isn't possible. But the comment's rationale ("trusted source") is the wrong reason. The right reason is "uses parameter binding throughout, unlike the DDL paths."

**Risk if unchanged:** A future maintainer who adds string interpolation to `buildFilteredWhereForCount` will read this comment and conclude that adding raw concatenation is safe because "collection is trusted." It isn't.

**Recommendation:** defer — single-line comment fix; not urgent. When touched next, replace with: "No collection-name validation needed: every use of `collection` is via parameter binding. The DDL paths (`CreateFieldIndex`/`DropFieldIndex`) need `validateCollectionName` because they interpolate the value into raw SQL; this method does not."

---

### F-2: WHERE-clause "extraction" is actually duplication; in-lockstep claim is false
**Severity:** low (drift hazard, not a current bug)
**Commit:** `3ea2c55 fix(repo,schema): totalCount respects filters — correctness defect`
**Location:** `internal/database/repositories/records.go:497-662` (`GetByCollectionFiltered`) and `internal/database/repositories/records.go:857-955` (`buildFilteredWhereForCount`)
**Concern:** The commit body says the WHERE-clause logic was "extracted into a new private helper `buildFilteredWhereForCount`" so the two stay "in lockstep — any future filter axis added to the SELECT path automatically participates in the COUNT." This is not what happened. `GetByCollectionFiltered` still inlines its WHERE-clause construction (lines 497-662). `buildFilteredWhereForCount` is a near-verbatim duplicate (~95 LOC) called only by the new `CountByCollectionFiltered`. I read both implementations line-by-line; they are shape-equivalent today (same clauses in the same order with the same parameter binding), but no shared code holds them in lockstep.

Concrete consequence: a future contributor adding (say) an `excludeDIDs` filter to `GetByCollectionFiltered` will not get an automatic update to the count path. The comment on `buildFilteredWhereForCount` ("The cursor + ORDER BY apply only to SELECTs and are added by the caller `(GetByCollectionFiltered)`") reinforces the false belief that the helper is shared with `GetByCollectionFiltered`.

**Risk if unchanged:** Future filter axes added to the SELECT path silently bypass the COUNT path, reintroducing the same shape of bug P-1/M9 fixed — `totalCount` drifts away from the result-set size.

**Recommendation:** defer — refactoring `GetByCollectionFiltered` to call `buildFilteredWhereForCount` is the right fix but out of scope for the overnight diff. At minimum, correct the commit-body claim and the helper's comment so the next reader knows the two are duplicates. The drift hazard is real but slow-moving (new filter axes are rare).

---

### F-3: metrics.RecordSearchApplied() now fires twice per filtered+totalCount request
**Severity:** low (metric inflation, not a correctness defect)
**Commit:** `3ea2c55 fix(repo,schema): totalCount respects filters — correctness defect`
**Location:** `internal/database/repositories/records.go:539` (in `GetByCollectionFiltered`) and `internal/database/repositories/records.go:908` (in `buildFilteredWhereForCount`)
**Concern:** Both the SELECT path and the new COUNT path call `metrics.RecordSearchApplied()` when `filter.Search != ""`. A single GraphQL request that supplies `search` and selects `totalCount` will now bump the search-applied counter twice. Other metric calls in the WHERE builder (none — only `RecordSearchApplied` appears) are unaffected.

This is fallout from F-2 (the duplicated implementation). The counter is a Prometheus monotonic counter, so dashboards that compare request rate to search-applied rate will now overcount by a factor of ~2 for any client that requests `totalCount` with `search`.

**Risk if unchanged:** Search-feature metrics drift relative to actual request counts. Doesn't break anything in production; misleads anyone trying to attribute load via the metric.

**Recommendation:** fix now — one-line change: drop the `metrics.RecordSearchApplied()` call from `buildFilteredWhereForCount` (line 908). The SELECT-path emission at line 539 already covers the user-visible event. Leaving the helper silent is consistent with the principle that COUNT-side computation is internal bookkeeping, not a separate user action.

---

## Things I checked and ruled out (one-liners)

- **M5 (activity cleanup)**: ticker is now correctly inside the goroutine; new test (`TicksPeriodically`) would catch the bug if reintroduced — it's not a tautology. The `activityCleaner` interface extraction is minimal and well-scoped. Initial `w.cleanup(ctx)` still runs synchronously in Start's caller goroutine (unchanged from before).
- **M6 (validateCollectionName)**: regex covers production NSIDs, rejects all SQL-injection payloads, length-cap defended. `CountByCollectionFiltered`'s deliberate skip is safe (see F-1).
- **M7 (per-generation cursor flusher)**: composes correctly with M8. Redundant finalFlush per reconnect is benign (atomic, monotonic cursor → idempotent writes).
- **M8 (Run owns events-close)**: single-writer invariant holds in both `jetstream` and `labeler` packages. processEvents on both consumer sides handles channel close cleanly. The labeler's `runOnce` still calls `client.Stop()` at line 289 after Run returns; this is now redundant for events-close but still needed to close `c.done` so the pingLoop exits — keep it. (Minor nit: the comment at `internal/labeler/consumer.go:286-288` "Always stop the client so its events channel closes" is stale post-M8; the events channel closes via Run's defer. Not flagging as a separate finding — the call itself is still correct, just for a different reason.)
- **M9 (totalCount filter-aware)**: observable behaviour change is in the correct direction (filtered count ≤ unfiltered count, strict equality when no filters). Authors empty-but-non-nil short-circuit semantics match `GetByCollectionFiltered`. Fast-path delegation to unfiltered `GetCollectionCount` is preserved.
- **OAuth deletions (M2/M3/M4)**: zero dangling references. `RequireAuth` correctly identified as still-live and kept. `AccessTokenKey`/`ScopesKey` exported keys kept for raw-context-value access (sensible).
- **Config / ADMIN_DIDS validation (W7)**: uses canonical `internal/atproto/did.IsValid`, not the deleted `internal/oauth/did` helpers. Empty allowed (sensible, ADMIN_API_KEY fallback). Test coverage includes SQL-injection payload rejection.
- **OAuth audit-log scrub (W1)**: two flagged sites correctly wrap user-controlled values in `logsafe.String`/`logsafe.DID`. Token-issued / token-refreshed logs (lines 755, 929) still log `user_id` raw, but that value comes from already-persisted code/token rows that were validated at insert time — defensible scope.
- **BuildFieldFilterClause deletion (W2)**: dead code with zero callers; comment cross-reference correctly updated to point at `BuildFilterGroupClause`.
- **awardCount description (W6)**: pinned-string change is paired with the drift test; no other consumers of the constant.
- **BatchInsert atomicity test (W11)**: `GetByURI` returns `sql.ErrNoRows` for missing records → `if err == nil` is the correct assertion.
- **Depth-cap defense-in-depth tests (W12)**: `at3` correctly builds 3 levels (depth 0→1→2→3 in `buildFilterGroupRecursive`), `at4` correctly fails (depth 4 > MaxFilterDepth=3). Inclusive-boundary claim is right.
