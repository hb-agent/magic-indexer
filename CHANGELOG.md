# Changelog

## 2026-04-13/14 — Post-Port Feature Extensions

Follow-up session working through deferred items from the hyperindex port.
Each feature was planned, reviewed (5 reviewers × 3-5 rounds per the process),
implemented, and verified end-to-end against the live Railway deployment.

### Fully implemented and closed

- **#37** ([PR #45](https://github.com/hb-agent/magic-indexer/pull/45)) — Improved `createClientAssertion` test coverage with 6 new tests + `fetchTokens` error propagation test (claim verification, header verification including `alg=ES256`, exp-iat range, JTI uniqueness, wrong-key rejection, BridgeError propagation).
- **#38** ([PR #46](https://github.com/hb-agent/magic-indexer/pull/46)) — `_and`/`_or` boolean composition in field filters via `FilterGroup` tree. Self-referential WhereInput, recursive SQL builder with proper parenthesization, max depth 3, global condition count capped at 20.
- **#43** ([PR #49](https://github.com/hb-agent/magic-indexer/pull/49)) — Admin `createFieldIndex`/`dropFieldIndex` mutations for managing partial expression indexes: `CREATE INDEX CONCURRENTLY ON record ((json->>'field')) WHERE collection = 'nsid'`. Accelerates comparison/pattern filters the GIN index can't serve.

### Partially implemented (follow-ups remain open)

- **#39** ([PR #47](https://github.com/hb-agent/magic-indexer/pull/47) + [PR #50](https://github.com/hb-agent/magic-indexer/pull/50)) — Single-column sort-aware keyset pagination now functional in the SQL layer (`orderBy` and `orderDirection` wire through to `ORDER BY` and the keyset cursor comparison). Multi-column sort deferred due to ROW() comparison complexity with mixed directions and NULL handling.
- **#40** ([PR #48](https://github.com/hb-agent/magic-indexer/pull/48)) — SQL layer supports nested path extraction via `__` separator (`metadata__source` → `json->'metadata'->>'source'`). `eq` uses nested JSONB containment. Auto-generating nested WhereInput fields from lexicon schemas deferred.

### Deferred (commented, not merged)

- **#41** — Tap signature verification: premature until Tap is actually deployed. Trust boundary documented.
- **#42** — Multi-relay Tap: single-instance approach sufficient for current ATProto relay landscape; alternative is running multiple magic-indexer instances sharing one Postgres.

### Bug fix

- **[PR #50](https://github.com/hb-agent/magic-indexer/pull/50)** — Discovered during deploy verification: `GetByCollectionFiltered` fast path delegated to `GetByCollectionWithKeysetCursor` which always sorts by `indexed_at DESC`, silently ignoring custom `orderBy` on unfiltered queries. Added `hasCustomSort` check to the fast-path guard.

### Verified working in production

End-to-end tested against https://magic-indexer-dev.up.railway.app after merge+deploy:
- `where: { title: { startsWith: "H" } }` returns titles starting with H
- `where: { _or: [{ title: { contains: "doc" } }, { title: { contains: "forest" } }] }` returns records matching either
- `orderBy: "title", orderDirection: ASC` returns alphabetically sorted results
- `orderBy: "title", orderDirection: DESC` returns reverse-alphabetical
- `totalCount` returns 809 for `orgHypercertsClaimActivity`
- `last: 2` returns final records with `hasPreviousPage: true, hasNextPage: false`
- V2 cursor decodes as JSON array `["indexed_at", "2026-04-12T...", "at://..."]`
- Admin `createFieldIndex` successfully created `idx_record_org_hypercerts_claim_activity_createdAt`
- Admin `dropFieldIndex` successfully dropped the index

## 2026-04-13 — Hyperindex Feature Port

**Scope:** Port key features from GainForest/hyperindex to magic-indexer, based on a 50-reviewer implementation plan.

### Phase 0: Shared Infrastructure Extraction
- Extract `RecordProcessor` into `internal/ingestion/` — shared by Jetstream and Tap consumers
- Extract `CursorFlusher` into `internal/cursor/` — atomic.Int64 cursor tracking with skip-on-idle
- Extract `RunWithReconnect` into `internal/consumer/` — exponential backoff (1s-2min)
- Jetstream consumer refactored to use shared packages (no behavior change)

### Phase 1: Rich GraphQL Filtering
- Per-collection `where` argument with per-field filter inputs
- Operators: eq, neq, gt, lt, gte, lte, in, contains, startsWith, isNull
- `eq` uses JSONB containment (`@>`) for GIN index support
- `neq` includes records where field is absent (NULL semantics)
- `contains`/`startsWith` escape `\`, `%`, `_` correctly
- `in` uses `= ANY($N::text[])` single array parameter
- Field name validation via regex (defense-in-depth)
- Migration 013: GIN jsonb_path_ops index (non-transactional migration support added)
- Deferred: `_and`/`_or` composition (#38), nested field filtering (#40), expression indexes (#43)

### Phase 2: Sorting / orderBy
- `orderBy` and `orderDirection` (ASC/DESC) arguments on collection queries
- `SortOption` type with `BuildSortExpr()` for SQL expression generation
- Cursor format upgraded to `["sortField", "sortValue", "uri"]` JSON array
- Backward-compatible cursor decoding (legacy pipe-delimited format accepted)
- Cursor sort-field mismatch detection with clear error message
- Deferred: multi-column sort (#39)

### Phase 3: totalCount
- `totalCount` field on connection types (lazy — only computed when requested via AST check)
- `GetCollectionCount()` in RecordsRepository
- Returns null on error (does not fail the query)

### Phase 4: Backward Pagination
- `last`/`before` arguments for reverse traversal
- Mixed forward+backward rejected with clear error message
- Results reversed in-memory to maintain correct edge order
- `hasPreviousPage`/`hasNextPage` per Relay spec

### Phase 5: Tap Consumer
- New `internal/tap/` package for crypto-verified event ingestion via Bluesky Tap sidecar
- Connection/Dialer interfaces for testability
- Synchronous dispatch (correct backpressure for ack-based delivery)
- Panic recovery, exponential retry (1s/2s/4s), per-event context timeout
- IndexHandler delegates to shared RecordProcessor
- Admin client for Tap HTTP API (health, repos/add, repos/remove)
- Config: TAP_ENABLED, TAP_URL, TAP_ADMIN_PASSWORD, TAP_DISABLE_ACKS, TAP_COLLECTION_FILTERS, TAP_MAX_RETRIES
- Migration 014: is_active column on actors table
- docker-compose.tap.yml for local development
- Trust boundary: Tap verifies MST inclusion proofs, NOT signing key vs DID document
- Deferred: signature verification (#41), multi-relay (#42)

### Phase 6: Tap/Jetstream Toggle
- TAP_ENABLED=true starts Tap consumer instead of Jetstream
- Collection allowlist enforced via RecordProcessor
- Jetstream cursor preserved for rollback

---

## 2026-04-13 — Security & Code Quality Audit

**Scope:** Full codebase security audit covering the Go backend (hypergoat), Next.js admin client, Docker/CI infrastructure, and all dependencies.

### Critical Fixes
- **CS-001** Pin Go version to 1.23 (was referencing non-existent 1.25); upgrade Alpine to 3.21; set GOTOOLCHAIN=local
- **CS-002** Remove hardcoded cookie secret default from client env.ts — app now fails loudly if COOKIE_SECRET is missing
- **CS-003** Remove hardcoded production Railway URLs from docs pages — derive from request URL at runtime
- **CS-004** Stop exposing OAuth client secrets to browser in GET_OAUTH_CLIENTS query

### High-Priority Fixes
- **CS-005** Add gosec and bodyclose security linters to golangci-lint
- **CS-006** Add `permissions: read-all` to GitHub Actions CI workflow
- **CS-008** Require session authentication on admin GraphQL proxy before forwarding ADMIN_API_KEY
- **CS-009** Replace hand-rolled JWT signing with golang-jwt/jwt/v5 library
- **CS-013** Pin opencode-anthropic-auth plugin to specific version (was @latest)

### Medium-Priority Fixes
- **CS-007** Harden session cookie: explicit httpOnly, sameSite=lax, reduce maxAge from 30 to 7 days
- **CS-010** Stop silently swallowing createClientAssertion errors in OAuth token exchange
- **CS-011** Add security response headers (HSTS, X-Frame-Options, etc.) to Next.js client via vercel.json
- **CS-012** Clean up .env.example: remove real admin DID default, un-comment ADMIN_API_KEY
- **CS-014** Add 1 MiB request body size limit to public GraphQL proxy

### Known Issues Requiring Follow-Up
- `golang.org/x/crypto v0.21.0` has CVE-2024-45337 (SSH auth bypass) — upgrade to >= v0.31.0 requires Go toolchain
- Label signature verification not implemented (labeler consumer trusts WebSocket connection)
- DPoP refresh token key rebinding (#24) still deferred
- No CSP header on Go backend GraphiQL (uses CDN-loaded assets from unpkg.com)

---

## 2026-04-13 — Full-text search

**PR:** [#35](https://github.com/hb-agent/magic-indexer/pull/35)

Add a `search: String` parameter to all typed collection queries and the generic `records` query. Uses Postgres `tsvector` with a GIN index for fast, stemmed full-text search.

- **Searched fields**: title (A), shortDescription (B), description (C), workScope (D)
- **Behavior**: space-separated terms are implicitly ANDed, English stemming applied
- **Combinable with**: `authors`, `labels`, `excludeLabels` filters
- **Max query length**: 500 characters

### Breaking changes

None. The `search` parameter is optional — existing queries work unchanged.

### Deployment notes

- Migration 012 adds a `search_vector` generated column and GIN index. Runs automatically on startup. At ~5000 records, takes under a second.
- Requires an `immutable_to_tsvector` wrapper function (created by the migration) because `to_tsvector` is STABLE not IMMUTABLE.

---

## 2026-04-13 — Record validation against lexicon schemas

**PR:** [#28](https://github.com/hb-agent/magic-indexer/pull/28)

Records with missing required fields no longer crash GraphQL responses. Query-time sanitization (always on) filters out bad records silently. Ingestion-time validation is configurable via `VALIDATION_MODE` env var.

---

## 2026-04-12 — Deep Code Review: 42 fixes across security, concurrency, performance

**PR:** [#25](https://github.com/hb-agent/magic-indexer/pull/25)
**Deployed to:** Railway (`magic-indexer-dev`)

### Breaking changes

- **GraphQL HTTP status codes**: queries now always return HTTP 200 per the GraphQL-over-HTTP spec. Errors are in the response body `errors` array (unchanged). Clients that checked `status === 400` for error detection should check the `errors` field instead. Most GraphQL client libraries already do this.

### Non-breaking changes visible to clients

- **WebSocket subscriptions** stay alive longer. The server now sends periodic pings, so idle subscriptions no longer timeout after 60 seconds.
- **OAuth `redirect_uri`** matching is now exact-only. The prefix-match path was an open-redirect risk. All existing registered clients use exact matching, so no impact expected.

### Internal improvements (invisible to clients)

**Security (10 fixes)**
- PAR endpoint validates `redirect_uri` against registered URIs
- DPoP access token hash (ATH) verification uses the verified JWT parse instead of a fragile re-parse
- OAuth client registration body capped at 1 MiB
- DID document ID validated against queried DID
- Token exchange errors logged server-side, generic message to client
- Subscription queries depth-checked (prevents resource abuse)
- Backfill responses bounded with `io.LimitReader`
- Grant type value no longer echoed in error responses
- OAuth cleanup worker errors now logged
- ATP session update errors now logged

**Concurrency (7 fixes)**
- WebSocket write races fixed in Jetstream and Labeler clients (mutex held across writes)
- Subscription `close()` uses `sync.Once` to prevent double-close panic
- Subscription `event.Record` cloned before mutation (prevents concurrent map write panic with multiple subscribers)
- `Collections()` reads under mutex
- `conn.Close` moved inside mutex in subscription handler
- `processEvents` receives client as explicit parameter (eliminates data race)

**Performance (7 fixes)**
- DID cache uses `singleflight.Group` to collapse concurrent resolutions (no more thundering herd)
- Redundant SELECT before INSERT eliminated (`ON CONFLICT WHERE cid`)
- `BatchInsert` skips no-op overwrites via CID guard
- `ensureActor` uses direct Upsert (removed redundant pre-check SELECT)
- Per-event Info logs downgraded to Debug (significant I/O reduction under load)
- `PublishRecord` skips JSON unmarshal when zero subscribers
- Double `IsCommit()` check merged

**Resilience (7 fixes)**
- `UpdateCollections` wrapped in reconnection loop (dynamic lexicon changes no longer kill event ingestion)
- Exponential backoff resets after 30-second stable connection (Jetstream + Labeler)
- Labeler cursor not advanced on full-batch failure (prevents data loss during transient DB issues)
- Labeler backfill sentinel uses -1 instead of 1 (prevents OutdatedCursor infinite loop)
- Postgres connection pool increased 25 to 50
- Backfill pagination capped at 10k pages (prevents infinite loop from misbehaving relay)
- CORS config comment corrected

**Correctness (5 fixes)**
- `createdAt` returns int64 (Unix epoch) not RFC3339 string in OAuth client mutations
- `capitalizeFirst` uses `unicode.ToUpper` instead of ASCII arithmetic
- OAuth client create/update responses include `scope` field
- DPoP ATH field carried through validation result struct

**Postgres compatibility (6 fixes)**
- All boolean columns (`neg`, `revoked`, `used`, `require_redirect_exact`) use `database.Bool` and scan into `bool` instead of int
- `InsertNegation` uses dialect-correct boolean literal
- Test assertions use JSON-semantic comparison (Postgres JSONB reorders keys)
- Test cursor format uses microsecond precision for Postgres timestamps
- `resetBetweenTests` fixed: correct table names, all OAuth tables included

### Deployment notes

- Zero config changes required. All fixes are code-level.
- Migrations run automatically on startup (no new migrations in this release).
- Brief (~10-30s) downtime during Railway container restart. Jetstream resumes from last-flushed cursor.
- Existing OAuth sessions, tokens, and registered clients are unaffected.

### Known deferred items

- **[#24](https://github.com/hb-agent/magic-indexer/issues/24)**: DPoP refresh token key rebinding (requires DB migration)
- **[hypercerts-org/certs-social#45](https://github.com/hypercerts-org/certs-social/issues/45)**: Client `COOKIE_SECRET` defaults to public value (Next.js fix)
