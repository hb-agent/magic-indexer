# Independent Technical Review — magic-indexer

_Audit performed 2026-05-17. This is an external review, not a PR review for the author's benefit._

## 1. Understanding

**Problem this code solves.** An AT Protocol AppView: subscribe to the firehose, validate incoming records against a user-uploaded set of Lexicon schemas, persist them to Postgres (JSONB + GIN indexes for full-text and containment queries), and expose a GraphQL API that is *dynamically generated* from those lexicons at boot. Bundled on top: labeler subscriptions for Bluesky moderation, an OAuth 2.0 provider for the admin UI (with DPoP, PKCE, PAR), ATProto service-auth JWT verification for a third-party notifications endpoint, an activity-feed fan-out, and a historical backfill path that can pull whole repos via CAR.

**Core approach.** Single Go binary (`cmd/hypergoat/main.go`) on chi + pgx + slog + prometheus. Repository pattern over a thin dialect-aware executor. Two interchangeable firehose consumers (Jetstream WebSocket vs. Tap sidecar). Idempotency rides on CID-based `ON CONFLICT WHERE record.cid IS DISTINCT FROM EXCLUDED.cid` semantics rather than dedup-before-insert. Schema rebuild on lexicon upload is handled by sending a graceful-shutdown signal to `serve()` and exiting with code 42 so the supervisor restarts the process (`main.go:55-68`, `main.go:947-958`). Filter expressions go through three validation layers (GraphQL input types → field-name regex → SQL parameter binding) before they touch the DB.

**Visible constraints.** Solo/small-team velocity with heavy AI assistance — `AGENTS.md` is 50KB and documents a 23-round review history. Deployed on Railway (cheap PaaS, no native blue/green), which directly explains the process-restart-on-upload pattern and the docker-compose layout. The repo is a fork of `hypercerts-org/hyperindex`; the binary is still called `hypergoat` and a vestigial SQLite migration tree (1 file vs. 26 for Postgres) confirms the fork didn't finish what upstream started. Live at `magic-indexer-dev.up.railway.app`, so the operational expectations are real.

---

## 2. Five Dimension Scores

### Dimension 1 — Problem Framing: **7**

The problem is real and tightly defined: "user uploads lexicons, we index matching records and expose them via GraphQL." The README and `setupGraphQL()` (`main.go:966-1052`) keep that core front and centre. However, scope has crept outwards — a full OAuth provider (`internal/oauth/`), a service-auth verifier (`internal/oauth/serviceauth_es256k.go`), notifications fan-out (`internal/notifications/`), labeler ingestion (`internal/labeler/`), and a second protocol (Tap) all live in the same binary. Each is justified individually; collectively, the system is closer to "AT Protocol everything" than to "indexer." The framing is right; the scope is on the wider end of defensible.

- Evidence: `cmd/hypergoat/main.go:170-247` (the entire run sequence touches 8 subsystems); `README.md:50-60` (live endpoints span GraphQL, GraphiQL, admin, notifications, health, stats, metrics, well-known DID); `internal/notifications/` and `internal/labeler/` each ~700 lines for features adjacent to the core.
- One higher (8): drop notifications or move it behind a build tag; the indexer-plus-GraphQL story would be the headline rather than one of many.
- One lower (6): if any of these subsystems had been added without a clear use case from the parent project. They haven't — every subsystem maps to a documented feature.

### Dimension 2 — Approach / Architecture: **7**

The architectural skeleton is sound and shows deliberate choices: composite keyset cursors instead of OFFSET (`records.go:343-355`, `builder.go:1041-1069`); GIN indexes via immutable wrapper functions (`migrations/postgres/023_add_contributor_identities_function.up.sql:30-59`); a per-Postgres `CancelRequestContextWatcherHandler` with 100ms grace before `pg_cancel_backend` (`internal/database/postgres/executor.go:104-140`) to handle timeout storms; multi-layer query budgets (router timeout > per-handler timeout > DB statement timeout) explicitly ordered in `config.Validate()`.

Two choices have real ongoing costs. First, **lexicon upload triggers a process restart via exit-code-42 contract** (`main.go:55-68`, `main.go:947-958`). It's clever, but it bets the system on having a supervisor that does the right thing — which works on Railway and any container orchestrator, but is a tight coupling that's invisible from inside the code. Second, **the dialect abstraction is half-built**: `ParseDialect()` returns -1 for non-Postgres (`internal/database/executor.go:201-207`), there is one SQLite migration vs. 26 Postgres ones, and yet every repository goes through `Placeholder()`/`Placeholders()` indirection as if portability mattered. The cost is paid every day; the benefit is not.

The Jetstream ↔ Tap duality is justified (different upstream APIs and ack semantics) but produces duplicated consumer wiring (`internal/jetstream/consumer.go` vs. `internal/tap/consumer.go`).

- Evidence: `main.go:947-958` (restart channel pattern); `internal/database/executor.go:201-207` and the missing SQLite migration tree; `internal/database/postgres/executor.go:104-140` (production-grade cancellation); `internal/database/migrations/postgres/023_add_contributor_identities_function.up.sql:30-59`.
- One higher (8): SQLite branch decisively deleted or actually implemented; the restart contract explicitly documented as an operator dependency in `SECURITY.md` / `RUNBOOK.md`.
- One lower (6): if the restart-on-upload pattern were actually used to paper over state that should be hot-reloadable (it isn't — only the GraphQL schema, which is genuinely structural).

### Dimension 3 — Code Quality: **6**

Code reads, naming is consistent, error wrapping uses `%w`, and comments are unusually load-bearing — most long blocks justify *why*, not *what*. But the codebase has a size problem that isn't an artifact:

| File | Lines | Notes |
|---|---|---|
| `internal/graphql/admin/resolvers.go` | **1729** | 48 functions; some test coverage exists in `handler_test.go`/`purge_test.go` but no `resolvers_test.go` next to it |
| `cmd/hypergoat/main.go` | **1497** | `setupRouter()` alone is 271 lines (`main.go:336-606`), mixing CORS, security headers, metrics, health, raw admin HTTP handlers, and stats |
| `internal/server/oauth_handlers.go` | **1190** | OAuth flow, token issuance, refresh, revoke, JWKS |
| `internal/database/repositories/records.go` | **1158** | One Go function (`GetByCollectionFiltered`, `records.go:460-702`) builds ~350 lines of dynamic SQL |
| `internal/graphql/schema/builder.go` | **1079** | `resolveRecordConnection` is 202 lines (`builder.go:486-687`) |
| `internal/graphql/admin/schema.go` | **902** | Admin GraphQL schema-as-code |
| `internal/database/repositories/filter.go` | **798** | Has `filter_unit_test.go` neighbour, so coverage is real |

That's seven files past the comfortable size threshold for a single-author Go module. None of them is a god function in the strict sense — internal structure is reasonable — but every change to the admin surface, the boot sequence, the OAuth provider, or the records query path lands in one of these files. Inline raw-HTTP admin handlers in `setupRouter()` (`main.go:430-543`) are an example of accreted scope: pause/reset/label-chain endpoints that should have lived next to the rest of the admin surface.

Positive signals: no `time.Sleep` outside tests (uses `consumer.RunWithReconnect` with proper backoff); 3 `panic()` calls and they're all init-time assertions; metrics cardinality is bounded by constant label sets (`internal/metrics/metrics.go` — purge reasons, validation modes), not `err.Error()`.

- Evidence: file sizes verified by `wc -l`; `main.go:430-543` (three raw HTTP admin endpoints inline in `setupRouter`); `records.go:460-702` (single function building most of the public read path).
- One higher (7): split `resolvers.go` and `main.go` along feature seams; the rest of the code is already at that level of clarity.
- One lower (5): if those large files contained the kind of hidden coupling the size implies. They mostly don't — the size is sprawl, not entanglement.

### Dimension 4 — Robustness: **7**

A lot of the right things are done. CID-based dedup gives at-least-once delivery a clean idempotency story (`internal/jetstream/consumer.go:280` advances cursor only after successful insert; `Records.InsertWithParams` uses `WHERE record.cid IS DISTINCT FROM EXCLUDED.cid`). Replay caches are bounded (`internal/oauth/jti_replay.go:39-43`, min-heap eviction at 76-84). The SSRF defenses in DID resolution explicitly reject loopback/private/link-local (`internal/oauth/did.go:351-368`). DPoP signature validation enforces ES256 + P-256 only (`internal/oauth/dpop.go:276,282`). The labeler consumer correctly handles `OutdatedCursor` by clearing both cursors and re-backfilling (`internal/labeler/consumer.go:188-200`). Panic recovery wraps every long-running goroutine (`main.go:1304-1309`, `internal/ingestion/processor.go:295-302`).

The robustness gaps are specific and named:

1. **Tap ack races the DB commit.** The ack write happens after `dispatch()` returns successfully, not as part of the insert transaction (`internal/tap/consumer.go:98-110`). A successful insert followed by a network failure on the ack means the event is redelivered and inserted-again; CID dedup catches the duplicate but the activity log row will be written twice unless idempotency extends there too. Tap was *picked* for stronger ordering and ack-based delivery — losing that to a timing bug is the most consequential robustness issue I found.
2. **Activity logging is fire-and-forget.** `internal/ingestion/processor.go:133-145` logs at warn and continues. A DB outage on the activity path produces silent inconsistency between `record` and `jetstream_activity` rows that operators can't easily detect.
3. **JKT and ATH comparisons are not constant-time** (`internal/oauth/middleware.go:160` — `*accessToken.DPoPJKT != result.JKT`; `internal/oauth/dpop.go:365` — `result.ATH != expectedATHStr`). Both values are public (derived from the proof itself), so the actual exploit surface is small, but PKCE uses `subtle.ConstantTimeCompare` for similarly-public values; the inconsistency is defense-in-depth left on the floor.
4. **No circuit breaker on persistent ingestion errors.** `processEvents` (`internal/jetstream/consumer.go:253`) logs at warn and continues forever — a multi-hour DB outage produces millions of log lines and a sustained zombie consumer with no `len(c.events)` queue-depth metric to alert on.
5. **No automated JTI cleanup hook.** A helper exists (`internal/oauth/middleware.go:424-429`) but the operator must call it. In-process cache is bounded; the DB-backed JTI store is not.

- Evidence: cited above.
- One higher (8): fix Tap ack ordering (or document it as best-effort and rely on dedup for correctness); constant-time JKT/ATH; queue-depth metric.
- One lower (6): if the codebase didn't already bound replay caches, validate inputs at three layers, and use parameter binding consistently. It does — the failure modes that remain are real but specific.

### Dimension 5 — Evolvability: **6**

The expected near-term changes — add a lexicon, add a filter operator, add an admin mutation, add a metric, add a migration — are easy. Lexicon upload is hot-pluggable via the schema-validate-then-restart contract. Migrations are numbered, idempotent, transactional, and the test `migrations_indexnames_test.go` catches duplicate-name regressions (the codebase has *already* hit this and corrected it in migration 021). Repository pattern keeps SQL out of the resolvers.

But the structural changes you'd predict from the trajectory of the project — splitting the binary, deciding the dialect question, taking the OAuth provider out, swapping the GraphQL library — are all expensive because they pile into the same handful of large files:

1. Splitting `admin/resolvers.go` is *the* prerequisite for adding any sizable admin surface without making the file unreadable. It's 1729 lines today and growing by feature.
2. `main.go` will resist any meaningful test for the boot sequence — `setupRouter`, `setupGraphQL`, `setupAdmin` are all called positionally and pass state via closures into callbacks (`adminHandler.Resolver().SetSchemaValidateCallback(...)`, `main.go:924-945`). Refactoring this requires breaking those callbacks apart first.
3. The `KindArrayContributor` filter has a *byte-for-byte coupling* between `filter.go:661-665` and the migration that defines the index. A reasonable change to the contributor extraction function will silently regress query performance with no test that catches it.
4. Two consumer types (Jetstream/Tap) share a processor but not their cursor/ack/reconnect machinery. A third consumer would have to choose which pattern to copy.

- Evidence: `internal/graphql/admin/resolvers.go` size; `main.go:924-945` (callback-passing setup); `internal/database/repositories/filter.go:661-665` (the contributor-index brittleness it documents); the SQLite-tree decision deferred indefinitely.
- One higher (7): split the admin resolver file; remove the SQLite tree (or actually finish it).
- One lower (5): if the GraphQL schema couldn't be hot-rebuilt and required code changes per lexicon. It can be — the restart-on-upload pattern is genuinely load-bearing for evolvability.

---

## 3. Verdict

This is competent, security-conscious, operationally serious work that ships and runs in production for a small team. The auth and data layers are unusually well-thought-out for a project this size — fail-closed defaults, multi-layer filter validation, parameterized SQL throughout, replay caches with bounded memory, keyset cursors, GIN-indexed JSONB with immutable wrapper functions. The metrics discipline (bounded cardinality, not `err.Error()` labels) and config validation (rejects dev placeholders for `SECRET_KEY_BASE`, requires `OAuthLegacyDPoPJKTCutoff`) signal someone who has run production. At the same time, scope is creeping into a system that takes a lot to understand from cold — eight subsystems wired together in a 1497-line `main.go`, seven files past 800 lines, and an admin resolver file that's already at the size where contributors will be afraid to touch it. The robustness gaps that remain are specific (Tap ack ordering, JKT/ATH timing, no circuit breaker on DB outage, no automated JTI cleanup) and look like the kind of thing that would surface in a third review round, not blocking issues.

**Single biggest risk.** Maintenance gravity in `admin/resolvers.go` (1729 lines, 48 functions) and `cmd/hypergoat/main.go` (1497 lines). These two files are on the critical path for almost every feature and almost every PR conflict, and they will be the first place a second contributor stalls.

**Single highest-leverage change.** Split `admin/resolvers.go` along feature seams (purge, lexicon upload, settings, backfill, notification passthrough) into separate files in the same package — every test currently in `handler_test.go` / `purge_test.go` already proves the package is set up for it. That single act unlocks parallel work, isolates test surface, and makes every other listed improvement cheaper.

**One thing this code does notably well.** The auth layer — DPoP proof verification with strict algorithm/curve allowlists (`internal/oauth/dpop.go:276,282`), a min-heap replay cache with expiry-priority eviction (`internal/oauth/jti_replay.go:76-84`), SSRF-hardened DID resolution (`internal/oauth/did.go:351-368`), service-auth JWT verification that pins `lxm` per endpoint (`internal/oauth/serviceauth.go:177-179`), Bearer-vs-DPoP scheme enforcement (`internal/oauth/middleware.go:156-204`). This is genuinely careful crypto-adjacent code, and it's the part of the system where carelessness would hurt the most.

---

## 4. Score Summary

| Dimension | Score |
|---|---|
| Problem Framing | 7 |
| Approach / Architecture | 7 |
| Code Quality | 6 |
| Robustness | 7 |
| Evolvability | 6 |
