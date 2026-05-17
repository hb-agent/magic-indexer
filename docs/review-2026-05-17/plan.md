# Implementation plan — review-2026-05-17

Based on `external-audit.md` (2026-05-17). Operator asked for an
implementation plan covering **only** the audit recommendations
that, on independent verification, hold up. This document records
the agree/reject decision per item, the proposed tracks, and the
open questions that need operator sign-off before implementation.

## Larger goal

Reduce maintenance gravity in the two files every PR currently
touches (`admin/resolvers.go`, `cmd/hypergoat/main.go`), close the
specific robustness gaps the audit named with evidence (Tap ack
race, JKT/ATH timing, missing observability on ingestion stalls,
unwired JTI cleanup), and decide the long-deferred SQLite question
either by deleting the tree or finishing it. Net effect: same
binary, same features, smaller blast radius for the next contributor
and one fewer "wait, is this dialect-portable?" question per repository
edit.

Out of scope by design: splitting the binary, removing the OAuth
provider or notifications subsystem, swapping the GraphQL library.
The audit lists these as evolutionary possibilities, not load-bearing
fixes. Including them would turn a tactical cleanup into a
multi-month rewrite.

## Verdict per audit item

Format: ID, audit-source, verdict, one-line rationale. ACCEPT means
the recommendation goes into a track. REJECT means I disagree and
the rationale lives here. MODIFIED means I agree with the diagnosis
but propose a narrower or different fix than the audit suggested.

| ID  | Audit source                          | Verdict   | Rationale |
|-----|---------------------------------------|-----------|-----------|
| A1  | Drop notifications / move behind tag  | REJECT    | Audit itself concedes every subsystem maps to a documented use case. Removing a working feature is scope deletion, not architecture improvement. |
| A2  | Document restart-on-upload contract   | ACCEPT    | The exit-code-42 + supervisor coupling is invisible from inside the code; documenting it is cheap and load-bearing for any future deploy-target change. |
| A3  | Decide the SQLite question            | ACCEPT    | 1 SQLite migration vs 26 Postgres confirms indecision. Two flavours considered under "open questions" below — minimal delete vs. full Placeholder() inlining. |
| A4  | Fix Tap ack ordering / activity log dedup | ACCEPT (reshaped by R1) | Verified at `tap/consumer.go:108-117`, `processor.go:123-146`. R1 broke the original dedup key; replaced with `source_event_id` (Tap `event.ID` / Jetstream `TimeUS`) + partial unique index. Cross-consumer fix, not Tap-only. |
| A5  | Activity logging fire-and-forget      | MODIFIED  | Bump warn→error + add `metrics.ActivityLogFailed()` counter; full retry would pollute the hot path. Once A4's idempotency lands, the duplicate-row half of the problem is also closed. |
| A6  | Constant-time JKT/ATH comparisons     | ACCEPT (expanded by R4) | Plus five more sites R4 found that the audit missed: `oauth_handlers.go:635, 639, 810` and `serviceauth.go:152, 177`. See Track 3. |
| A7  | Circuit breaker + queue-depth metric  | MODIFIED (corrected by R3) | Queue-depth metric: ACCEPT, but hooks in `jetstream/client.go` (where the buffer is), not `consumer.go`. Tap has no buffered channel — emit a dispatch-seconds histogram instead. Circuit breaker: REJECT. Rate-limit shape corrected to "first 5 loudly, then 1/min" per the existing `labeler/consumer.go` pattern. |
| A8  | Wire automated JTI cleanup            | REVISED (corrected by R4) | **Audit was wrong**: cleanup IS wired at `oauth_handlers.go:1162-1190` (hourly). One-line fix instead: tighten the floor from `now-3600` → `now-DefaultMaxDPoPAge(=300s)`. Plus add a `sync.WaitGroup` to `backgroundServices` so the goroutine drains before `db.Close()`. |
| A9  | Split `admin/resolvers.go`            | ACCEPT (repartitioned by R2) | Partition revised: `PopulateActivity` → activity file; reset-all family → `purge.go`; `AddAdmin`/`RemoveAdmin` → core; explicit helper enumeration; TOC comment at top of kept file. |
| A10 | Split `cmd/hypergoat/main.go`         | MODIFIED  | Narrow to extracting `/admin/labeler/{reset,pause}` and `/admin/label-chain` (main.go:430-468, 477-516, 523-543) + `checkAdminBearer` (412-428). Dependencies named: helper becomes package-level, accepting `cfg.AdminAPIKey`; `bg` plumbed in. |
| A11 | Contributor-index regression test     | ACCEPT    | Migration 024's index expression must match `buildContributorFilter`'s `indexedExpr` constant byte-for-byte; no test catches drift today. ~30-line file-read-and-compare test. |
| A12 | Share Jetstream/Tap consumer machinery | REJECT   | Speculative for a hypothetical third consumer. Two implementations is not duplication; three would be. Wait for the third. |
| A13 | Split the binary / remove subsystems  | REJECT    | Out of scope per "Larger goal". |

## Tracks (disjoint file ownership)

Tracks are scoped to be commit-sized and committed in the listed
order onto `staging` (per AGENTS.md project-specific override).
File ownership is disjoint within a track so individual commits do
not collide on the same file as the next track lands.

### Track 1 — Contributor-index regression test (A11)

- **Files**: `internal/database/repositories/filter_unit_test.go`
  (append a test); read-only access to
  `internal/database/migrations/postgres/024_*_up.sql` (file path to
  be confirmed during implementation — migration 024 is the one that
  creates `record_contributor_identities`).
- **Approach**: test reads the migration file, regexes out the
  function body or the indexed expression, and asserts the literal
  string matches `indexedExpr` in `filter.go:666`. If the migration
  file changes wording, the test fails with a clear "byte-for-byte
  coupling broken — update `indexedExpr`" message.
- **Acceptance**: `go test ./internal/database/repositories/...`
  passes; deliberately editing the migration to a non-matching
  expression breaks the test.

### Track 2 — Restart-on-upload doc (A2)

- **Files**: `docs/RUNBOOK.md` (new "Restart-on-exit contract" section
  near the existing lexicon-upload section ~L272); `AGENTS.md` (one-line
  pointer to the RUNBOOK section).
- **Approach**: 1–2 paragraphs in RUNBOOK explaining the exit-code-42
  contract, why it requires a restart-on-exit supervisor (Railway,
  Docker restart policies, systemd `Restart=on-failure`), and what
  breaks if it's deployed somewhere without one (lexicon uploads will
  appear to succeed but the GraphQL schema won't rebuild).
- **Acceptance**: doc reviewer accepts; no code changes; AGENTS.md gets
  a single-line pointer so agents reading it cold can find the
  authoritative source.

### Track 3 — OAuth defense-in-depth (A6 + A8, expanded by R4)

- **Files**:
  - `internal/oauth/middleware.go` — constant-time JKT compare at
    `:160` (preserve nil-check ordering at `:157`); collapse
    `ErrDPoPKeyMismatch` response shape to match other invalid-token
    error responses on the *public HTTP* surface (R4.5).
  - `internal/oauth/dpop.go` — constant-time ATH compare at `:365`.
  - `internal/oauth/oauth_handlers.go` — constant-time compares at
    `:635` (`ClientID != clientID`), `:639` (`RedirectURI != redirectURI`),
    `:810` (`oldRefreshToken.ClientID != clientID`); JTI-cleanup floor
    tightened at `:1181` from `now-3600` to
    `now - DefaultMaxDPoPAge - skew` (~330s total).
  - `internal/oauth/serviceauth.go` — constant-time compares at
    `:152` (audience claim) and `:177` (lexicon-method claim).
  - `cmd/hypergoat/main.go` — extend `backgroundServices` with a
    `sync.WaitGroup`; `StartCleanupWorker` calls `wg.Add(1)` /
    `defer wg.Done()`; `bg.Stop()` calls `wg.Wait()` after `cancel()`.
- **Approach**: replace `!=`/`==` string comparisons on attacker-
  controllable inputs with `subtle.ConstantTimeCompare(..., ...) != 1`.
  Preserve all existing nil-checks and error semantics; only the
  comparison primitive changes. JTI cleanup gets one floor change
  rather than a new ticker (existing hourly worker was unknown to the
  audit). Shutdown WaitGroup ensures the goroutine cannot be
  mid-DELETE when `defer svc.db.Close()` runs.
- **Acceptance**: `go test ./internal/oauth/...` passes; new test
  asserts nil `DPoPJKT` returns `ErrTokenNotDPoPBound` (not panic) at
  the JKT path; `bg.Stop()` returns only after the cleanup goroutine
  exits (testable with a short-interval test cleanup function); the
  public 401 response for "key mismatch" is byte-for-byte identical
  to the response for "post-key generic invalid"; verified via curl
  trace.

### Track 4 — Ingestion idempotency + observability (A4 + A5 + A7-narrowed, reshaped by R1+R3)

- **Files**:
  - `internal/database/migrations/postgres/027_*.up.sql` /
    `.down.sql` — new migration adding `source_event_id BIGINT NULL`
    column to `jetstream_activity` and a partial unique index
    `WHERE source_event_id IS NOT NULL`. The name `jetstream_activity`
    is now slightly misleading (covers both consumers) but renaming is
    out of scope.
  - `internal/database/repositories/jetstream_activity.go` —
    `LogActivity` gains a `sourceEventID *int64` parameter; SQL
    becomes `INSERT ... ON CONFLICT (source_event_id) WHERE
    source_event_id IS NOT NULL DO NOTHING RETURNING id`, with a
    UNION SELECT fallback to fetch the existing row's id on conflict
    (R1.5: so the `updateStatus` call still has a valid id and
    redelivered events don't get marked orphaned by the janitor).
  - `internal/ingestion/processor.go` — accept `SourceEventID *int64`
    on `ProcessOp`; pass through to `LogActivity`. Bump
    `slog.Warn` → `slog.Error` on `LogActivity` failure; add
    `metrics.ActivityLogFailed()` counter increment (R1+A5).
  - `internal/jetstream/consumer.go` — populate
    `op.SourceEventID = &event.TimeUS`; rate-limited error logging
    using the existing `labeler/consumer.go:399, 427-432` pattern
    (first 5 errors at error severity, then 1/min with
    `occurrences_since_last_log` field); counter is `atomic.Int64`
    to be safe against `/metrics` readers (R1.6).
  - `internal/tap/consumer.go` — populate
    `op.SourceEventID = &event.ID`. Ack ordering stays as-is
    (R1.3: 4-B is theatre under reconnect semantics).
  - `internal/tap/handler.go` — same rate-limited error logging
    pattern applied symmetrically.
  - `internal/jetstream/client.go` — emit
    `hypergoat_jetstream_event_buffer_depth` (Gauge from `len(c.events)`)
    and `hypergoat_jetstream_event_buffer_capacity` (Gauge constant
    = 1000) at appropriate hooks (R3.1).
  - `internal/metrics/metrics.go` — new metric registrations:
    `hypergoat_activity_log_failed_total` (Counter),
    `hypergoat_ingestion_error_total{consumer="jetstream|tap"}` (Counter),
    `hypergoat_jetstream_event_buffer_depth` (Gauge),
    `hypergoat_jetstream_event_buffer_capacity` (Gauge),
    `hypergoat_tap_event_dispatch_seconds` (Histogram, for the
    consumer with no channel to gauge — R3.1).
- **Approach**: dedup on the upstream event identifier rather than
  the record content. Tap's `event.ID` and Jetstream's `TimeUS` are
  per-event monotonic identifiers from the upstream protocol;
  acked-then-redelivered events carry the same id, but
  same-record-different-event-bytes (legitimate updates, re-creates
  after delete) carry different ids and produce different audit
  rows — which is the correct behaviour for an audit log. Cross-
  consumer fix (R1.4).
- **Acceptance**:
  1. Unit test: simulate redelivery of the same `(consumer, event_id)`
     N times → exactly one `jetstream_activity` row, and the row's
     final status reflects the latest processing outcome (not
     stuck-pending → orphaned).
  2. Unit test: two genuinely distinct events for the same `(did, rkey)`
     with same/different CIDs both produce their own audit rows
     (regression on R1.1's silent-drop hazard).
  3. `/metrics` endpoint includes the four new metrics with
     `hypergoat_` prefix; metric labels do not embed unbounded
     strings (per the existing convention).
  4. Under a simulated DB-outage loop (10k consecutive insert
     failures), error-level log lines ≤ 5 + (duration_in_minutes)
     and the counter metric reflects the real failure count.
  5. Race detector (`go test -race`) clean across consumer + metric
     paths.

### Track 5 — Admin resolver split (A9, repartitioned by R2)

- **Files**: `internal/graphql/admin/resolvers.go` → split into
  feature files inside the same package. Final partition (after
  R2's dissents accepted):
  - `resolvers.go` — `NewResolver`, the `Set*Callback` methods,
    `Statistics`, `CurrentSession`, `Settings`, `UpdateSettings`,
    `AddAdmin`, `RemoveAdmin` (R2.3: these mutate `admin_dids` and
    belong with `UpdateSettings`), `IsBackfilling`,
    `SetBackfillActive`. Plus the helpers: `validateOperatorURL`,
    `validateJetstreamURL`, `auditSettingsChanged`. **Add a TOC
    comment at the top** listing every split file and what lives
    where (R2.5).
  - `resolvers_lexicons.go` — `Lexicons`, `UploadLexicons`,
    `RegisterLexicon`, `DeleteLexicon`, `CreateFieldIndex`,
    `DropFieldIndex`, plus the constants `maxLexiconUploadBytes`,
    `maxLexiconFileCount`, `maxLexiconFileSize`,
    plus `notifyLexiconChange` (R2.4: helpers travel with callers).
  - `resolvers_oauth_clients.go` — `OAuthClients`, `CreateOAuthClient`,
    `UpdateOAuthClient`, `DeleteOAuthClient` only (R2.3: admin DID
    mutations moved out).
  - `resolvers_backfill.go` — `TriggerBackfill`, `BackfillActor` only
    (R2.1 + R2.2 move the rest out).
  - `resolvers_activity.go` — `ActivityBuckets`,
    `CollectionOverview`, `RecentActivity`, `ValidationStats`,
    plus `PopulateActivity` (R2.1: misfiled by the original plan).
  - `resolvers_labels.go` — `LabelDefinitions`,
    `ViewerLabelPreferences`, `Labels`, `Reports`, `CreateLabel`,
    `NegateLabel`, `CreateLabelDefinition`, `ResolveReport`, plus
    the helpers `maxAdminPageSize` and `clampAdminPageSize` (R2.4).
  - `purge.go` *(existing file, additions only)* — append
    `PreviewResetAll`, `ResetAll`, `resetAllCounts`,
    `resetAllTables`, `quoteIdent` (R2.2: co-locate destructive
    operations; comment at L1338 already points here).
- **Approach**: pure motion (cut-paste). No behaviour change. One
  commit per file extracted so review is `git log --stat` friendly;
  the reset-all migration into `purge.go` gets its own commit.
  All existing tests (`handler_test.go`, `purge_test.go`,
  `purge_resolver_test.go`) keep passing without modification.
- **Acceptance**: `go test ./internal/graphql/admin/...` passes;
  `go vet`, `golangci-lint run` clean; the largest remaining file is
  under ~500 lines; no behaviour change visible from the GraphQL
  schema (golden-file test if one exists, otherwise the existing
  handler tests); the new TOC comment is current.

### Track 6 — Extract inline admin HTTP from setupRouter (A10-narrowed, sharpened by R3)

- **Files**: `cmd/hypergoat/main.go` — remove these exact ranges:
  `:412-428` (`checkAdminBearer` helper),
  `:430-468` (`/admin/labeler/reset`),
  `:477-516` (`/admin/labeler/pause`),
  `:523-543` (`/admin/label-chain`).
  Move to new file `cmd/hypergoat/admin_http.go` in the same `main`
  package.
- **Dependencies (R3.5)**:
  - `checkAdminBearer` is a closure over `cfg.AdminAPIKey` →
    converted to a package-level helper
    `func checkAdminBearer(adminAPIKey string, w http.ResponseWriter,
    r *http.Request) bool`.
  - `/admin/labeler/pause` closes over `bg.labelerMu` and
    `bg.labelerConsumers` → handler factory accepts `*backgroundServices`.
  - `/admin/labeler/reset` similarly accesses background-services
    state — same handler-factory pattern.
- **Approach**: each extracted endpoint becomes a
  `func newXxxHandler(cfg *config.Config, bg *backgroundServices)
  http.HandlerFunc`; `setupRouter` calls these factories and mounts
  the returned handler at the existing path. No URLs, response
  shapes, or auth wrapping change. Router-level middleware
  (CORS, security headers, metrics — `setupRouter:356-371`) is
  applied at mount time, not per-route, so the move does not affect
  middleware ordering.
- **Acceptance**: `setupRouter` shrinks by ~130 lines; the three
  endpoints respond byte-identically (test by `curl` against the
  running dev binary with a valid and invalid `AdminAPIKey`);
  `go vet`, `golangci-lint run` clean; existing integration coverage
  (if any) unchanged.

### Track 7 — SQLite tree decision (A3)

- **Files** depend on the choice — see "open question SQL-A vs SQL-B"
  below. Either:
  - **SQL-A (minimal delete)**: `internal/database/migrations/sqlite/`
    deleted; the `Dialect`, `Placeholder()`, `ParseDialect()` surface
    stays as-is.
  - **SQL-B (full inline)**: deletes SQLite tree + removes
    `Placeholder()` indirection from all ~10 repository files +
    removes `Dialect` and `ParseDialect` from
    `internal/database/executor.go` and
    `internal/database/postgres/executor.go` + drops
    `internal/database/executor_test.go`'s dialect tests.
- **Approach**: whichever the operator picks. SQL-B is mechanical
  but wide; commit per repository file to keep diffs reviewable.
- **Acceptance**: build green, tests green, no remaining references
  to SQLite anywhere in the tree (`grep -ri sqlite .` returns only
  expected hits like upstream attribution).

## Alternatives considered

| Alternative                              | Why not                                                                                                                                                                                            |
|------------------------------------------|----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------|
| Single mega-commit, all tracks at once   | Loses the per-track scope tag the project's recent PR history (#79/#80) standardised on; conflicts with deep-flow's "atomic commits with clear scope tag" rule.                                    |
| Split each track into its own PR         | Project override in AGENTS.md is "work directly on staging"; per-PR overhead would slow review without buying isolation.                                                                            |
| Rewrite the admin resolver to gqlgen     | Audit doesn't recommend it; would invalidate every existing admin test; "swap the GraphQL library" is explicitly listed as expensive evolutionary work.                                             |
| Move Tap consumer to a sidecar binary    | Would solve "the binary is too big" but not the ack race; out of scope per "Larger goal".                                                                                                          |
| Build a feature-flag system to gate the new admin file split | Pure motion doesn't need a flag; rollback is `git revert` of the split commits.                                                                                                  |

## Acceptance criteria (overall)

1. All four quality gates pass on staging head:
   `go build ./...`, `go vet ./...`, `go test -race ./...`,
   `golangci-lint run ./...`.
2. Pre-existing lint/test baseline captured before track 1, compared
   after each track — "no new errors" claim has to be measurable.
3. `wc -l internal/graphql/admin/resolvers.go` after track 5: under
   550 lines. (Relaxed from <500 per implementation-review round 2
   item IR1.B — the ~50-line TOC comment is load-bearing for
   preventing partition drift, which was the R2.5 obligation from
   plan-review round 1; trimming the TOC would undo that fix.)
4. `wc -l cmd/hypergoat/main.go` after track 6: under 1400 lines.
5. Tap redelivery test (track 4) passes; queue-depth metric appears
   in `/metrics`; sustained DB-outage simulation logs at error
   severity but rate-limited.
6. Constant-time-compare paths covered by a unit test (track 3); JTI
   cleanup ticker visible in main and shuts down on context cancel.
7. SQLite decision (track 7) implemented one way or the other; no
   half-state remains.
8. Draft PR `staging → main` opens cleanly, all CI checks green.

## Rollback plan

Per-track rollback:

- **Tracks 1, 2, 5, 6**: pure motion or additive — `git revert
  <track-commits>` and the prior behaviour is restored exactly.
- **Track 3**: `subtle.ConstantTimeCompare` is functionally equivalent
  to `!=` for these values; revert is byte-equivalent. JTI cleanup
  ticker revert leaves the helper unwired (the audit's starting
  state).
- **Track 4 Option 4-A**: requires a down-migration for the unique
  index; standard migration revert. Option 4-B revert restores the
  outside-transaction ack.
- **Track 7 SQL-A**: revert restores the deleted directory from git.
  SQL-B revert is wider but mechanical — same per-file commit
  pattern means revert is per-file too.

Production rollback at the Railway level is the standard Railway
deployment-rollback flow; nothing track-specific is needed beyond
the per-track git revert.

## Out of scope

- Splitting the `hypergoat` binary into multiple processes.
- Removing the OAuth provider, notifications, labeler, or Tap
  consumer.
- Sharing consumer machinery between Jetstream and Tap (audit
  recommendation A12).
- Swapping the GraphQL library or schema generator.
- Hot-reload of anything beyond the GraphQL schema rebuild that the
  exit-code-42 contract already covers.
- Performance work beyond the queue-depth metric (no benchmark
  regressions tracked here).
- Documentation rewrites beyond track 2.

## Open questions for the operator

> Many of the original open questions were closed in plan-review
> round 1 — see `review-round-1.md` for the rationale. Only items
> that genuinely need operator input remain.

1. **Critical adjacent finding from R4.7**: `client_secret` is
   never verified on `/oauth/token` — confidential OAuth clients
   are not actually authenticated. This is an auth-bypass, not a
   Track 3 item. Three options:
   - **(a)** File as a separate critical issue *now* and pause
     review-2026-05-17 implementation until that fix lands.
   - **(b)** File as a separate critical issue *now* and proceed
     with review-2026-05-17 in parallel.
   - **(c)** Roll a `client_secret` verification into Track 3 of
     this batch (expands Track 3's scope and review surface).
   - My recommendation: **(b)** — review-2026-05-17 doesn't touch
     `/oauth/token`'s client-auth path, so the work is genuinely
     independent. File as a P0 with its own deep-flow plan.

2. **Track 7 SQLite scope**: SQL-A (delete the migration tree only,
   keep `Placeholder()` indirection), or SQL-B (full inline of `$N`
   across all repositories and removal of the `Dialect` abstraction)?
   SQL-A is one commit; SQL-B is ~10 commits but actually closes the
   "paying the cost daily" complaint the audit raised.
   My recommendation: **SQL-B** for the architectural value, **as
   its own part-2 PR** following the #79/#80 precedent — see
   sequencing below.

3. **JTI cleanup floor (Track 3)**: tightening from `now-3600` to
   `now - DefaultMaxDPoPAge - skew`. What value for `skew`? I'll
   default to **30s** unless operator prefers wider/narrower. (The
   value bounds worst-case row count between cleanup runs.)

The previous open questions on Tap-ack approach, JTI ticker interval,
rate-limiting shape, and reviewer mix are all closed by round 1:

- Tap-ack: dedup on `source_event_id`, ack ordering unchanged
  (R1.1–R1.4).
- JTI: no new ticker; tighten the existing one's floor (R4.1).
- Rate-limiting: first 5 loudly, then 1/min, atomic counter (R1.6 + R3.3).
- Reviewer mix: four lenses were sufficient — round 2 not warranted.

## Sequencing

Per R3.6, the SQLite refactor (Track 7) is split into a separate
part-2 PR matching the #79/#80 precedent so its blast radius is
isolated.

**Part 1 PR — `staging → main` (this batch)**

Commit order on `staging`:

1. Track 1 (regression test) — safest first, no behaviour change.
2. Track 2 (doc) — independent.
3. Track 3 (OAuth defense-in-depth) — small, surgical, but expanded
   by R4; 7 comparison sites + cleanup-floor change + shutdown
   WaitGroup + error-response collapse. One commit per logical
   sub-track (compares / cleanup / shutdown / response collapse) so
   the diff is reviewable per concern.
4. Track 6 (extract inline admin HTTP) — moves easy lines out first
   so Track 5's larger split lands against a slightly smaller
   main.go.
5. Track 5 (admin resolver split) — pure motion, large diff;
   one commit per extracted file.
6. Track 4 (idempotency + observability) — actual logic change; lands
   after structural cleanups so review attention isn't split. One
   commit for the migration, one for the repository + processor
   changes, one for the metrics + rate-limited logging.

Then Draft PR `staging → main` per AGENTS.md.

**Part 2 PR — `staging → main` (follow-up)**

7. Track 7 (SQLite decision). Single commit for SQL-A, or one commit
   per repository for SQL-B (~10 commits).

Each track gets its own commit (Tracks 3, 4, 5 get multiple commits
per concern as noted above). Draft PR with same review-decision-docs
links.
