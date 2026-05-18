# 03 — Implementation plan

Triage of the 02-findings into three tiers, ordered for the
overnight pass. Each MUST FIX item carries a per-item plan
section.

## MUST FIX TONIGHT (8 items)

Ordered by ascending risk: deletions first (build confidence,
shrink surface area), then load-bearing fixes.

### Order of operations

1. M1 — Q-5 small-helper deletions (3 micro-deletions).
2. M2 — Q-1 OAuth `errors.go` dead code.
3. M3 — Q-3 OAuth `scopes.go` dead code.
4. M4 — Q-2 + Q-4 dead OAuth generators + AuthMiddleware halves.
5. M5 — C-1 + T-1 activity cleanup worker fix + non-tautological test.
6. M6 — S-1 SQL-injection: `collection` argument validation.
7. M7 — C-2 Jetstream cursor-flusher per-generation lifetime.
8. M8 — C-3 WebSocket `Stop()` close-on-events race.
9. M9 — P-1 totalCount filter-aware (correctness fix) + T-8 regression test.

(C-4 / P-2 / P-3 / P-4 deferred — see WILL NOT FIX.)

### M1 — Q-5: Three small dead helpers

**Larger goal:** shrink dead surface area without touching
anything load-bearing.

**Scope:**
- Delete `database.DecodeError`.
- Delete `oauth.IsDIDPLC` and `oauth.IsDIDWeb`.
- Delete `lexicon.IsValidNSID`.

**Alternatives considered:**
- Keep them "in case someone wants them later." Rejected per
  directive — bias toward deletion.
- Mark deprecated. Rejected — they have zero callers; deprecated
  is for things that have callers we want to migrate.

**Acceptance criteria:**
- `go build ./...` clean.
- `go vet ./...` clean.
- `go test -race -short ./...` passes (or no new failures
  beyond known shared-Postgres flakes).
- `golangci-lint run ./...` returns 0 issues.

**Out of scope:** no rename or restructure of the remaining
helpers in those files.

**Rollback:** single `git revert`.

**Open questions:** none.

---

### M2 — Q-1: OAuth `errors.go` dead code

**Larger goal:** the OAuth package was scaffolded as a general-
purpose OAuth server but the deployment is "OAuth bridge to
upstream PDS." Remove the helpers that the bridge never uses.

**Scope:** delete `internal/oauth/errors.go` + its test
`errors_test.go` (157 LOC + the test).

**Alternatives considered:**
- Keep `OAuthError` type for callers writing custom errors.
  Rejected — zero production callers; production uses the
  private `writeOAuthError` in `oauth_handlers.go:1104-1110`
  which does not take an `*OAuthError`.
- Keep the typed constructors as a public API surface for the
  future. Rejected — directive forbids adding flexibility for
  hypothetical needs.

**Acceptance criteria:** same as M1.

**Risk consideration:** verify by grep that no file outside
`errors.go` and `errors_test.go` imports `oauth.OAuthError`,
`oauth.NewOAuthError`, or `oauth.WriteErrorResponse`. If grep
finds an external user (admin client repo, etc.) abandon this
fix and document in `02b-late-findings.md`.

**Rollback:** single `git revert`.

**Open questions:** none — external grep is the gate.

---

### M3 — Q-3: OAuth `scopes.go` dead code

**Scope:** delete everything in `internal/oauth/scopes.go`
EXCEPT `ParseScopes` (which is used twice in `middleware.go`).
Delete `scopes_test.go` entries that test deleted symbols.

**Alternatives:** keep the scope constants for future use —
rejected, same reason as M2.

**Acceptance:** same gates.

**Rollback:** single revert.

---

### M4 — Q-2 + Q-4: `token_generator.go` halves + `AuthMiddleware` halves

**Scope:**
- `token_generator.go`: delete `GenerateClientID`,
  `GenerateClientSecret`, `GenerateDPoPNonce`,
  `GeneratePARRequestURI`, `IsExpiredWithSkew`. Keep the four
  used `Generate*` helpers + supporting code.
- `middleware.go`: delete `RequireAuth`, `RequireScope`,
  `WithMaxDPoPAge`, `AccessTokenFromContext`,
  `ScopesFromContext`, `UseJTI`, `CleanupExpiredJTIs`,
  `validateBearerToken` (the private dispatch). Keep
  `OptionalAuth` (used in production).

**Alternatives:** same as M2 — keep the public API "for the
future." Rejected.

**Acceptance:** same.

**Risk consideration:** `AuthMiddleware` is a public API. Verify
by grep that the deleted methods have zero call sites in the
repo. External callers would need to import the type itself,
which is currently used only by `internal/server/` — confirm.

**Rollback:** single revert per file (two commits).

---

### M5 — C-1 + T-1: activity cleanup worker + non-tautological test

**Larger goal:** the activity cleanup worker has been silently
dead since it was added — `defer ticker.Stop()` is registered
in `Start()` instead of inside the goroutine, so the ticker is
stopped the moment `Start` returns. The test that should have
caught this (`TestActivityCleanupWorker_StartStop`) re-implements
a correct ticker loop inline rather than exercising production
`Start()`.

**Scope:**
1. `internal/workers/activity_cleanup.go`: move the `defer
   ticker.Stop()` inside the goroutine, after the goroutine is
   launched. Likely 2-line diff.
2. `internal/workers/activity_cleanup_test.go`: replace the
   tautology with a test that calls production `Start()`, waits
   for the cleanup to fire at least once (or asserts the
   internal counter advanced), and confirms `Stop()` does halt
   the ticker.

**Alternatives:**
- Keep the existing test as a baseline + add a second one.
  Rejected — the existing test is dishonest (it pretends to
  exercise production but doesn't); leaving it in suggests
  coverage that doesn't exist.

**Acceptance:** same gates + the new test must actually fail if
the production `defer ticker.Stop()` is moved back outside the
goroutine.

**Rollback:** single revert.

**Open questions:** none.

---

### M6 — S-1: validate `collection` in `CreateFieldIndex` / `DropFieldIndex`

**Larger goal:** the only admin-authenticated SQL-injection
surface in the codebase. `collection` is interpolated raw into
a DDL string while the adjacent `field` parameter is validated
via `ValidateFieldName`. The shape is single-quote-wrapped, so
exploitation requires breaking out of the quotes (`';--`), but
the validation gap is real.

**Scope:** add `validateCollectionName(string) error` that
matches the lexicon NSID shape (segments of `[a-z][a-z0-9]*`
separated by `.`, total length bounded). Call from
`CreateFieldIndex` and `DropFieldIndex` BEFORE the raw
interpolation. Add tests for the validator + a regression test
that confirms a malicious `collection` is rejected.

**Alternatives:**
- Use parameterised query for the DDL. Rejected — Postgres
  doesn't parameterise object identifiers; this would require
  `pg_quote_ident()` and still has the same validation
  obligation upstream.
- Whitelist of known collections. Rejected — collections are
  user-uploaded via the lexicon admin path; there's no closed
  set.

**Acceptance:** same gates + the new regression test fails if
validation is removed.

**Rollback:** single revert.

**Open questions:** none.

---

### M7 — C-2: Jetstream cursor-flusher per-generation lifetime

**Larger goal:** each call to `startInternal` spawns
`go c.cursorFlusher.Run(ctx)` with the outer `c.ctx`. The
flusher only exits on `ctx.Done()`, so flushers accumulate
across reconnects. Labeler got this right via per-generation
`genDone`; jetstream did not.

**Scope:** `internal/jetstream/consumer.go`: introduce a per-
generation cancel context (sibling to the websocket connect
context), wire the flusher's `Run(genCtx)` to it, ensure
`Stop()` and reconnect both fire the genCancel. Mirror
labeler's pattern.

**Alternatives:**
- Single flusher launched in `New()` instead of `startInternal`.
  Rejected — flushers need access to the connection state which
  is per-generation in jetstream's design.

**Acceptance:** same gates + a regression test that exercises
3 consecutive reconnects and asserts at most 1 flusher
goroutine alive after the last reconnect.

**Rollback:** single revert.

**Open questions:** none.

---

### M8 — C-3: WebSocket `Stop()` close-on-events race

**Larger goal:** both `internal/jetstream/client.go:288` and
`internal/labeler/client.go:288` do `close(c.events)` from
`Stop()` while `Run()` may be blocked at
`case c.events <- event:`. Stop is called from a different
goroutine than Run with no synchronisation. The send-on-closed-
channel panic crashes the process at shutdown / collection
update.

**Scope:** let `Run()` own the channel lifecycle. Move the
`close(c.events)` to a `defer` inside `Run()` so it fires
exactly when `Run()` returns. `Stop()` only fires the cancel
signal that `Run()` selects on; it no longer touches the
channel.

**Alternatives:**
- Add a `sync.Mutex` to guard the close. Rejected — the
  ownership pattern is the cleaner fix; locking around a close
  is a code smell.
- Use a buffered channel + `select { case ... default: }`.
  Rejected — silently drops events on shutdown, contradicts the
  cursor advance contract.

**Acceptance:** same gates + a regression test that exercises
`Stop()` while `Run()` is blocked on the events channel and
asserts the process does not panic. (Race-detector-friendly.)

**Risk consideration:** shutdown ordering matters here. Need to
verify all consumers of `c.events` handle the channel close as
"upstream is done" rather than treating it as an error.

**Rollback:** single revert per file (two commits).

**Open questions:** does the consumer of `c.events` in
`processor.go` actually exit when the channel closes? Verify
during implementation; if not, a corresponding fix lands.

---

### M9 — P-1 + T-8: filter-aware totalCount

**Larger goal:** `totalCount` returns the unfiltered
`SELECT COUNT(*) FROM record WHERE collection = $1` regardless
of `where`/`authors`/`labels`/`search`/`excludePds`. This is a
correctness defect — every filtered GraphQL query that requests
`totalCount` gets a wrong number.

**Scope:**
1. `internal/database/repositories/records.go`: add
   `CountByCollectionFiltered(ctx, collection, filter
   RecordFilter)` that reuses the existing WHERE-builder used
   by `GetByCollectionFiltered`.
2. `internal/graphql/schema/builder.go:763`: replace the call
   to `GetCollectionCount` with `CountByCollectionFiltered`,
   passing the request's `RecordFilter`.
3. Regression test (T-8): assert `totalCount` matches the
   number of rows returned by an equivalent filtered query.

**Alternatives:**
- Mark `totalCount` deprecated and have clients page until
  `hasNextPage: false`. Rejected — load-bearing for
  certified-app's "page 1 of 47" UI.
- Cache the count per request to avoid double-emission of the
  filter SQL. Rejected — premature optimisation; the COUNT
  uses the same indexes as the SELECT.

**Acceptance:** same gates + the regression test fails if
`GetCollectionCount` is restored.

**Risk consideration:** changing a `totalCount` value that's
been wrong since launch is observable to clients — the new
value will be SMALLER for filtered queries. This is the
correct behaviour but worth calling out in the PR body.

**Rollback:** single revert (atomic across all three files).

**Open questions:** none.

---

## WILL FIX IF TIME PERMITS (12 items)

If the MUST FIX tier closes with budget remaining, work
through these in order. Each is small + clean. Stop the moment
budget closes — leave the WILL FIX items for the operator.

| # | Source | Description | Effort |
|---|---|---|---|
| W1 | S-4 | Switch oauth_handlers.go raw-loginHint/did logs to `logsafe.String` | S |
| W2 | R-1 | Delete `BuildFieldFilterClause` (zero callers) | S |
| W3 | R-3 | Delete `GetByCollection` and `GetByCollectionWithCursor` (only test callers; tests migrate to filtered variant) | M |
| W4 | R-6 | Collapse `GetCIDsByURIs` / `GetExistingCIDs` duplication | S |
| W5 | O-1 | Add "Recovering from an INVALID CONCURRENTLY index" section to RUNBOOK | S |
| W6 | P-5 | Fix `awardCount` description's "sub-millisecond" claim → "indexed lookup" | S |
| W7 | S-5 | Validate `ADMIN_DIDS` via `did.IsValid` at startup | S |
| W8 | S-3 | Make `ALLOWED_ORIGINS` unset a startup error when `EXTERNAL_BASE_URL` is https | S |
| W9 | R-2 | Collapse `buildBadgeAwardSubjectFilter` / `buildStringSubjectFilter` | M |
| W10 | R-5 | Extract `TextINClause` helper for six near-identical IN-clause loops | S |
| W11 | T-3 | Add BatchInsert partial-failure test | S |
| W12 | T-4 | Add direct depth-cap test on `buildFilterGroupRecursive` | S |

## WILL NOT FIX TONIGHT (defer to operator)

Each of these is HIGH or MEDIUM but requires operator input or
is too risky to land at 3am without verification.

| # | Source | Reason for deferral |
|---|---|---|
| C-4 | UploadLexicons partial-success | Requires operator policy decision on partial-vs-atomic upload semantics. Two valid fixes; choosing between them needs product input. |
| P-2 | jetstream_activity duplicates JSON | The activity table's production use isn't visible from the code alone; dropping the JSON column could break a debugging workflow. Defer pending operator confirmation. |
| P-3 | 4 sequential DB round-trips per ingest | Reorder ingestion sequence — touches the hottest path in the codebase. Wants a controlled rollout, not 3am yolo. |
| P-4 | UpsertActor LRU coalescing | New caching layer (DID popularity LRU). Introduces a new abstraction; directive forbids. |
| C-5 to C-11 | Various medium correctness issues | Each is fixable; most have nuance (panic-recover policy, slow-subscriber handling). Best discussed before landing. |
| T-2 / T-7 / T-15 | Consumer unit-test gap | Requires new test harness for jetstream/tap/labeler. Significant scope. |
| Q-4 (partial) | Some `AuthMiddleware` deletions | Some methods have plausible external-caller paths (`RequireScope`); conservative deletion only — covered by M4 with careful grep. |
| The full nit/low set | Per directive | "Bias toward deletion, don't manufacture work." |

## Time budget reminder

- Phase 4 wall budget: ~3h (from review plan §"Time budget").
- 9 MUST FIX items × ~15-20 min average = ~2.5h.
- Mini re-review every 3-5 items.
- Stop the moment budget closes OR I hit a problem I can't
  cleanly resolve.

## Mini-review cadence

Per directive §Phase 4d: every 3-5 items, write
`04-mini-review-N.md` answering:
- Did any of these commits introduce a new problem?
- Did any commit regress a test?
- Did any commit contradict an earlier fix?
- Are the commits actually atomic per the scope ceiling?

Mini-reviews after: M5 (the activity-cleanup fix), then M8
(after WebSocket fix), then end of MUST FIX.
