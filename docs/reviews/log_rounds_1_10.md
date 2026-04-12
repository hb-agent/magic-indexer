# Overnight Review Log

Branch: `per-labeler-definitions` (PR #3)
Base: `6ff40c6` (post-PR #1 merge)
Baseline state: build/vet/test/lint all clean.

## Round summary table

| Round | Perspectives | Reviewers | Critical | Major | Minor | Nice-to-have | Fixed | Verified |
|-------|--------------|-----------|----------|-------|-------|--------------|-------|----------|
| 1 | broad sweep (20 lenses) | 20 | 3 | 12 | 19 | 8 | 12 | build+vet+test+lint green, pushed b5d08ef |
| 2 | deep-dives on weakest areas (20 lenses) | 20 | 12 | 24 | 15 | 0 | 18 | green, pushed 80a9c4e |
| 3 | adversarial / malicious inputs (20 lenses) | 20 | 8 | 21 | 18 | 3 | 14 | green, pushed 68c526e |
| 4 | production readiness (20 lenses) | 20 | 8 | 28 | 14 | 0 | 4 | green, pushed 322ad09 |
| 5 | end-to-end integration (20 lenses) | 20 | 4 | 9 | 6 | 0 | 2 | green, pushed a7aae92 |
| 6 | code quality / API hygiene (20 lenses) | 20 | 0 | 3 | 9 | 6 | 0 | no code changes |
| 7 | test quality / coverage (20 lenses) | 20 | 0 | 1 | 9 | 0 | 3 tests added | green, pushed f9e8c11 |
| 8 | security deep-dive (20 lenses) | 20 | 0 | 2 | 1 | 0 | 2 | green, pushed 6d5e149 |
| 9 | performance at scale (20 lenses) | 20 | 0 | 0 | 4 | 2 | 0 | no code changes |
| 10 | final sanity pass (20 lenses) | 20 | 0 | 0 | 0 | 0 | 0 | clean — ship-ready |

### Round 10 highlights
- Zero new findings. Rounds 9 and 10 both returned effectively clean, satisfying the early-stop criterion but the user asked for all 10 rounds so all 10 were run.

### Round 9 highlights
- 16 of 20 perspectives came back clean for target scale (10k-100k records/day, 100 GraphQL reads/sec, single instance).
- Flagged DPoP JTI cleanup missing — false positive; it's already wired in OAuthHandlers.StartCleanupWorker at oauth_handlers.go:1100.
- Deferred: slow query logging middleware, concurrent labeler backfill pagination, SQLite ANALYZE after index create. None are blockers at target scale.

### Round 8 highlights
- Fixed: refresh token rotation ordering — old refresh token is now revoked only after both new access and refresh tokens are safely stored
- Fixed: /oauth/authorize previously leaked DNS / auth-server resolver errors back to the caller via `error_description`; now logged internally and replaced with a generic message
- 17 of 20 security surfaces came back clean on this pass (token lifetimes, CSRF, timing, path traversal, XXE, log injection, SSRF, TLS, session fixation, open redirect, mass assignment, JWT alg, cookie security, Content-Type, partial auth, admin DID freshness, secrets in env)

### Round 7 highlights
- Added regression tests for the Round 5 DPoP InsertIfNew race fix
- Added regression tests for the Round 2/3 admin POST-only + unauthenticated rejection gates
- Added variableKeys helper unit test (Round 3 log-injection fix)
- Larger coverage gaps (end-to-end Jetstream→GraphQL, labeler reconnection) documented but out of scope for the SQLite-only test harness

### Round 6 highlights
- No critical or actionable issues surfaced. The few items flagged as MAJOR were false positives on verification:
  - `context.Background()` in labeler/jetstream finalFlush is intentional — we want the flush to survive parent ctx cancellation.
  - Subscription handler's detached context is by design (WS lifetime ≠ HTTP request lifetime).
- Remaining findings are nit-level (sentinel errors in dpop.go, slice preallocation in batch insert, map-alloc in empty-URIs path). Not worth the change risk at this stage.

### Round 5 highlights
- Fixed: DPoP JTI replay detection was racy (Exists-then-Insert TOCTOU) — now uses atomic InsertIfNew with ON CONFLICT DO NOTHING
- Fixed: migrations.Rollback ran DownSQL and schema_migrations delete in separate statements — now transactional
- Noted (by design, not a bug): takedown enforcement is opt-in / label-neutral per user requirement
- Many flagged cursor / shutdown / batch-load items turned out to be false positives on verification (cursor already updates only after successful handleCommit; labelerMu is released before Stop; batch-load filters via GetByURIs which already filters negations)

### Round 4 highlights
- Fixed: security headers middleware (nosniff, DENY, no-referrer, HSTS-if-https)
- Fixed: Dockerfile -trimpath + -buildvcs=false for reproducible builds
- Fixed: getEnvInt now logs a warning on malformed ints instead of silent fallback
- Many reported findings were false positives on re-verification (labelerMu scope, cursor race, migration 009 backfill, FIFO eviction, version sort). Those are filed for reference but not code changes.
- Deferred to future work: metrics/Prometheus, distributed tracing, query depth limits, admin runbook knobs (pause labeler, purge labels), CSP, rate limiting on token endpoint.

### Round 3 highlights
- Critical: DID/bridge HTTP bodies were unbounded (OOM via hostile upstream)
- Critical: PLC directory redirect chain could bypass private-host rejection
- Critical: Jetstream client had no websocket read limit
- Critical: admin GraphQL accepted GET (leaked mutations + vars into access logs)
- Critical: admin mutation log dumped raw variables (log injection vector)
- Major: PKCE used `==` instead of constant-time compare
- Major: UpdateSettings accepted any URL without scheme/host validation
- Major: admin Labels/Reports had no upper bound on `first`
- Major: migrations were not transactional per-file (partial-state risk)
- Major: X-User-DID header was trusted without DID-format validation

### Round 2 highlights
- Critical: missing exp filter in label active-set queries (records filter, GetByURIs, HasTakedown, GetTakedownURIs)
- Critical: GraphQL POST body uncapped (OOM vector); websocket idle connections never timed out; max subs per client uncapped
- Critical: OAuth bridge accepted non-https endpoints from DID documents (downgrade / file:/ftp: surface)
- Critical: /admin/graphql allowed unauthenticated introspection
- Major: /health wasn't DB-aware; labeler consumer goroutines lacked panic recovery
- Major: Postgres pool missed ConnMaxLifetime; SQLite busy_timeout not set
- Major: jetstream UpdateCollections used context.Background instead of parent
- Cleanup: labeler client frame handling (empty body, non-normal close codes, #info decode warn), cursor gap detection, Dockerfile Go 1.25

### Round 1 — highlights

**Critical:**
- SSRF in `oauth/did.go` `resolveWebDID` — no private-IP rejection
- Data race in `consumer.go ensureDefinition` — Insert can fail on concurrent Exists+Insert across consumers
- `/stats` reads `bg.labelerConsumers` without mutex

**Major selections fixed this round:**
- DID validation on `/admin/labeler/reset`
- Delete error logging on the same endpoint
- ClampPageSize in admin labels/reports
- CreateLabelDefinition length bounds on val/description
- Backfill + dynamic-jetstream goroutines get tracked contexts
- Shutdown ordering: HTTP drain before bg.Stop
- Jetstream finalFlush bounded timeout
- Labels field non-null in schema
- ensureDefinition ON CONFLICT DO NOTHING

