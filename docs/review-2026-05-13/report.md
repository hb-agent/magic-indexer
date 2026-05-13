# Magic-Indexer code review — 2026-05-13

**Scope**: every code change merged today across five workstreams.
**Method**: two rounds, six independent reviewer passes.
- **Round 1** (parallel, broad): three lenses — security, code quality, performance.
- **Round 2** (parallel, focused): verification stress-test of round-1's highest-impact claims; test-coverage and observability audit; architectural cross-cutting review.

This document synthesizes the six reviewer outputs into one prioritized record. **Severity here reflects round-2 verification** — round-1 claims that didn't survive a code-trace are downgraded, with the surviving claim kept and the mitigation noted.

---

## 1. Scope reviewed

Five workstreams landed on `main` today (`fc1ccc0` → `a9d54df`):

| # | Workstream | Headline change |
|---|---|---|
| 1 | **Issue #64** (#69) | Contributor-identity filter on `orgHypercertsClaimActivity`; strict `did.IsValid` predicate replaces `oauth.HasDIDMethodPrefix`; CASE-WHEN guard on the EXISTS subquery; notifications-extractor object-shape fix. |
| 2 | **Upstream-adoption Tracks B–H** (#74, #77) | Zero-property union-builder fix; `EXTERNAL_BASE_URL` normalization; admin-DID gate on settings; HMAC-bound `previewPurgeActor` / `purgeActor` with SQL-only + best-effort Tap; batch lexicon registration; OAuth callback uses `PUBLIC_URL`; self-referential backend URL detection; SECURITY.md actor-purge contract. |
| 3 | **Issue #65** (#75) | `BadgeAward` subject filter (three subject shapes: bare string, strongRef object, defs#did object). |
| 4 | **Issue #71** (#76) | Layered query budgets on `/graphql` — pool-default `statement_timeout` + per-request middleware deadline. |
| 5 | **Process** | AGENTS.md deep-flow process documented. |

---

## 2. Executive summary

The day's changes raise the bar substantially in the two highest-risk surfaces: **destructive operations** (the HMAC-bound purge flow is genuinely well-designed) and **DID input validation** (the `did.IsValid` migration closes real attacks). But the rollout is uneven — three destructive-op patterns coexist in one resolver file, three resolver entry points still use prefix-only DID checks, and the very migration that justified the rename left the new `purgeActor` resolvers themselves unvalidated.

**Top priorities (P0 — fix this week):**

1. **`ResetAll` is the most destructive admin mutation and the least hardened.** No audit log, no token, no admin binding, an out-of-date deletion list. Strictly worse than `purgeActor` for the same operator surface.
2. **Settings page form hydration is broken** (`useState(() => …)` misused as `useEffect`). Admins can't edit basic settings; only saved by the defensive `|| undefined` + resolver `!= nil` guard preventing data loss.
3. **Migration 013 is a silent no-op** — same index name as 001, but down migration drops 001's index. First production rollback degrades query performance permanently.
4. **DID validation gap on three admin paths** (`previewPurgeActor`, `purgeActor`, `AddAdmin`, `BackfillActor`) — the same `did.IsValid` migration that closed this elsewhere skipped these. Audit-trail integrity depends on it.
5. **OAuth `returnTo` is attacker-controlled** via `POST /api/login`; the single `//`-prefix guard does not cover `\` (browser-normalised to `/`), URL-encoded forms, or whitespace-prefix tricks. Mitigated by browser same-origin semantics today but the right shape is a strict path regex or `new URL().origin` comparison.

**Top priorities (P1 — next two weeks):**

6. **Index the new filter shapes.** Contributor `EXISTS (jsonb_array_elements …)` and BadgeAward subject `LIKE 'at://$1/%'` cannot use any current index — both are O(collection size) per query. First production query against a busy collection will trip `QUERY_TIMEOUT`.
7. **Switch pgx to `CancelRequestContextWatcherHandler`.** The 5s budget works, but the first burst of slow queries will churn TCP connections and amplify the latency hit it's protecting against.
8. **Promote filter builders to a `FilterKind` enum + per-lexicon descriptor registry.** Two consumers today; the in-code TODO explicitly predicted this entrenchment. The third filter is the entrenchment point.
9. **Purge mutation metrics.** Zero Prometheus signal today. A burst of `purge_token_rejected{reason=invalid}` is the canonical signal of attack or UI bug; you can't alert on logs.
10. **Close audit-log asymmetry.** `purgeActor` has a strong `event=actor_purge` line + 90d retention contract. `UpdateSettings` / `ResetAll` / `AddAdmin` are silent. The strictly-more-dangerous mutations are less observable.

---

## 3. Findings — security

Sourced from Security-R1; verified or downgraded by Verify-R2. Severity reflects post-verification reality.

### 3.1 HIGH

#### SEC-1 — OAuth `returnTo` allow-list is fragile
- **Where**: `client/src/app/api/login/route.ts:21-24`; `client/src/app/api/oauth/callback/route.ts:96, 103`
- **What**: `session.returnTo` is set verbatim from a JSON `body.returnTo` in `POST /api/login`. The callback's only defense is `returnTo.startsWith("/") && !returnTo.startsWith("//")`. Round-2 verification confirms the field is attacker-controlled but **downgrades the live exploitability**: browsers normalise `\` to `/` only within same-origin paths; percent-encoded slashes (`%2f%2f`) are not re-interpreted as path delimiters by `Response.redirect`'s `Location:` header in modern browsers; iron-session's `httpOnly + sameSite=lax` cookie constrains the channel. So this is **defense-in-depth**, not a live RCE.
- **Recommendation**: In `/api/login`, parse `new URL(returnTo, env.PUBLIC_URL)` and reject anything where `parsed.origin !== new URL(env.PUBLIC_URL).origin`. Replace the callback's `startsWith` check with the same parse. One helper, two call sites, ~12 lines.

#### SEC-2 — `ResetAll` is the most destructive mutation and the least hardened
- **Where**: `internal/graphql/admin/resolvers.go:1172-1195`; schema `internal/graphql/admin/schema.go:403-409`
- **What**: `resetAll(confirm: "RESET")` wipes every table on a literal-string compare. No HMAC token, no admin-DID binding, no `event=…` structured log line, no operator audit contract. Verified: the schema is `requireAdmin` only; the resolver doesn't even emit an INFO line. The deletion list is also out of sync with the schema (notifications, oauth grants, labeler config rows are not touched — see SEC-7).
- **Recommendation**: Mirror `purgeActor`: HMAC preview/confirm token bound to admin DID + a `confirmCount` of total rows. Emit `event=reset_all requested_by_did=… rows_deleted=…`. Document the operator log-retention contract in SECURITY.md alongside the actor-purge entry.

#### SEC-3 — Three admin destructive paths skip strict `did.IsValid`
- **Where**: `internal/graphql/admin/purge.go:208-209, 272-275` (purge resolvers); `resolvers.go:599-603` (`AddAdmin` uses `strings.HasPrefix`); `resolvers.go:466-470` (`BackfillActor` uses `strings.HasPrefix`)
- **What**: The same `did.IsValid` migration (commit `c069afa`) that closed prefix-only checks across the codebase left these untouched. Round-2 confirms slog text-handler quoting neutralises the live log-injection vector, so this is **hygiene**, not exploit — but the audit trail's evidentiary value depends on the input being a syntactically valid DID. `AddAdmin` can write a comma-separated-list-poisoning value (`"did:plc:foo,did:plc:victim"`) that silently grants admin to another DID.
- **Recommendation**: A single `admin.requireValidDID(ctx, did, fieldName) error` helper at the resolver-layer boundary, called by every DID-taking admin mutation. Add a custom golangci-lint rule banning `strings.HasPrefix(s, "did:")` outside `internal/atproto/did`. Same commit closes all three call sites.

### 3.2 MEDIUM

#### SEC-4 — Admin GraphQL behind `OptionalAuth`; body decode + depth check run before auth
- **Where**: `cmd/hypergoat/main.go:741-742`; `internal/graphql/admin/handler.go:108-121`
- **What**: The router mounts `/admin/graphql` behind `OptionalAuth` (not `RequireAuth`). The handler enforces auth at line 169, but the body-decode error at line 108 and the depth check at 114-121 both fire **before** any auth check. Unauthenticated probes can measure body-size limits, depth limits, and burn lexer CPU.
- **Recommendation**: Switch to `adminHandler.RequireAuth()`, or move the auth check before the body decode in the handler.

#### SEC-5 — `validateOperatorURL` accepts internal IPs (SSRF surface on relay/PLC config)
- **Where**: `internal/graphql/admin/resolvers.go:35-69`
- **What**: Validates `scheme == "https"` and non-empty host. Does **not** block `169.254.169.254`, `127.0.0.1`, `10.0.0.5`, `[::1]`. A compromised admin can set `relay_url` to a cloud-metadata endpoint and leak IAM responses into the indexer's logs.
- **Recommendation**: After parsing, reject hosts that `net.ParseIP(host)` identifies as `IsLoopback() || IsPrivate() || IsLinkLocalUnicast() || IsUnspecified()`. Apply to `validateJetstreamURL` too.

#### SEC-6 — Admin actor-purge resolver returns success even when Tap cleanup fails silently mid-process
- **Where**: `internal/graphql/admin/purge.go:316-326`
- **What**: Best-effort Tap call uses `context.Background()` rather than deriving from the request context. The justification ("the mutation has already returned success for the SQL leg") is wrong — the mutation hasn't returned yet; it's still inside `PurgeActor`. If the admin retries on a hung Tap, every retry queues another 5-second Tap call. Detached context also means client cancellation doesn't propagate.
- **Recommendation**: Derive from `ctx` with a 5s timeout. Or move the Tap call to a fire-and-forget goroutine (capturing the values it needs and respecting an outer shutdown signal).

#### SEC-7 — `ResetAll` deletion list is out of sync with the schema
- **Where**: `internal/graphql/admin/resolvers.go:1177-1192`
- **What**: Deletes from activity, reports, labels, records, actors. Migrations 015 (notifications), 016 (oauth grants), labeler config rows, notification preferences are not touched. After-the-fact `purgeActor` for the leftover OAuth grant DIDs cleans some but not all.
- **Recommendation**: Either introspect the schema (list all tables, delete in FK-respecting order) or delete `ResetAll` from the admin schema and document `docker-compose down -v` as the canonical reset path.

### 3.3 LOW

#### SEC-8 — BadgeAward subject `LIKE` accepts unescaped `_` from `did:web` DIDs
- **Where**: `internal/database/repositories/filter.go:599-610`
- **What**: SQL is `… LIKE 'at://' || $1 || '/%'` with `$1` being a validated DID. `did.IsValid` permits `_` in the method-specific identifier; `_` is a single-char wildcard in LIKE. `did:web:foo_bar` silently matches strongRef URIs whose authority is `did:web:fooXbar`. Bounded to `did:web:*` (PLC DIDs use base32 without `_`).
- **Recommendation**: Apply the existing `escapeLike` helper (same file, line 637) and append `ESCAPE '\'` to the LIKE clauses.

#### SEC-9 — `clampOperationName` has a duplicate space-rejection clause
- **Where**: `internal/graphql/handler.go:83`
- **What**: `r == ' ' || r == ' '` — both clauses are ASCII space (0x20). The comment claims U+2028 / U+2029 / control chars are rejected; the code rejects the same char twice and leaves the Unicode line-separator class unhandled.
- **Recommendation**: Change one clause to `r == ' ' || r == ' '`, or use `unicode.IsControl(r)`.

#### SEC-10 — `injectStatementTimeout` regex is case-sensitive; Postgres GUC names are not
- **Where**: `internal/database/postgres/executor.go:46-91`
- **What**: `statementTimeoutRegex` matches lowercase `statement_timeout=` only. An operator setting `STATEMENT_TIMEOUT=` in `DATABASE_URL` won't match; the injector then appends its own `-c statement_timeout=N`; Postgres applies last-write-wins, silently overriding the operator's intent.
- **Recommendation**: Add `(?i)` to the regex, or lowercase `existing` before matching.

#### SEC-11 — Used-token set is unbounded under hostile preview-spam
- **Where**: `internal/graphql/admin/purge.go:42-48, 150-164`
- **What**: `usedSigs` is pruned only inside `Verify`, and only entries whose `Exp <= now` are dropped. Sign() doesn't prune. An admin with API-key access can flood `previewPurgeActor` without confirming, but tokens never land in `usedSigs` until verified — so growth is actually bounded by verify rate × TTL. Edge concern only.
- **Recommendation**: Add a hard cap (`if len(s.usedSigs) > 10_000`) and a periodic sweep goroutine started in `NewPurgeTokenSigner` to drop the lock-on-every-verify pattern.

---

## 4. Findings — code quality

Sourced from Quality-R1; verified or downgraded by Verify-R2.

### 4.1 MAJOR

#### Q-1 — Settings page form hydration is broken
- **Where**: `client/src/app/settings/page.tsx:121-129`
- **What**: `useState(() => { if (settings) { setDomainAuthority(…); … } })` uses `useState`'s lazy initializer as if it were `useEffect`. Runs once on first render when `settings` is still `undefined` (React Query is pending). The form inputs never populate from server state. Round-2 verification: **does not destroy data** — the `|| undefined` guard at lines 207-211 plus the resolver's `!= nil` checks (resolvers.go:1110-1162) collectively make empty-field saves no-ops. So this is **UX broken, data safe**.
- **Recommendation**: Replace with `useEffect(() => { if (settings) { setDomainAuthority(settings.domainAuthority); … } }, [settings])`. One-character fix in concept; ~8 lines in practice.

#### Q-2 — Two duplicate filter builders that the in-code TODO explicitly anticipated
- **Where**: `internal/graphql/schema/where.go:307-415` (`buildContributorFieldFilter` + `buildBadgeAwardSubjectFieldFilter`)
- **What**: 54-line and 56-line functions that differ in four points: field name string, `FieldFilter` marker boolean, error-message prefix, list-size cap call. Round-2 confirms the structural similarity; the comments next to the boolean markers (`filter.go:42-60`) explicitly anticipated promotion to a `Kind` enum when a second collection adopted the shape. The second collection has now landed and neither marker was promoted.
- **Recommendation**: Replace `IsArrayContributor` / `IsBadgeAwardSubject` with `FieldFilter.Kind FilterKind` (`KindScalar`, `KindArrayContributor`, `KindUnionSubject`). Collapse the two builders into one `buildDIDOnlyEqInFilter(kind, fieldName, errPrefix, filterMap)`. ~150 LOC saved + one canonical extension point for the third filter.

#### Q-3 — `PreviewPurgeActor` collapses every DB error to "actor not in index"
- **Where**: `internal/graphql/admin/purge.go:217-225`
- **What**: `actor, err := r.repos.Actors.GetByDID(ctx, did)` sets `actor = nil` on *any* non-nil err — connection errors, query timeouts, scan failures, anything. The operator sees `actorExists=false` and proceeds against a phantom zero count.
- **Recommendation**: `if err != nil && !errors.Is(err, sql.ErrNoRows) { return nil, fmt.Errorf("get actor: %w", err) }`. Match the pattern at `actors.go:209-220`.

#### Q-4 — `extractFieldFiltersRecursive` silently drops non-map field bodies
- **Where**: `internal/graphql/schema/where.go:177-181`; same shape at lines 158-161 for `_and`/`_or`
- **What**: A client sending `where: { contributor: "did:plc:alice" }` (string instead of `{ eq: "…" }`) fails the `filterInput.(map[string]interface{})` cast and the loop `continue`s without surfacing an error. Result: every record in the collection is returned.
- **Recommendation**: Replace `continue` with `return nil, fmt.Errorf("unexpected filter shape for %s: want object, got %T", fieldName, filterInput)`. Same for the `_and` / `_or` paths.

#### Q-5 — Audit log has `actor_did` and `target_did` set to the same value
- **Where**: `internal/graphql/admin/purge.go:328-336`
- **What**: Both fields hold the input `did`. The commit message implied they would be distinct (likely `actor_did` was meant to be the actor row's resolved DID and `target_did` the input). Downstream SIEM tooling that keys on `actor_did != target_did` is silently broken.
- **Recommendation**: Drop `actor_did` entirely, or set it from `actor.DID` only when `actorExists`.

### 4.2 MINOR

#### Q-6 — Slog-injection vector on the operator-supplied `options` string
- **Where**: `internal/database/postgres/executor.go:71-82`
- **What**: When `options=` from `DATABASE_URL` is preserved, the entire string is logged. The 256-byte truncation is good; no control-char scrubbing. `DATABASE_URL` is operator-set today, but the same code path could land elsewhere in a future refactor.
- **Recommendation**: Lift the control-char scrub from `clampOperationName` into a shared `internal/logsafe` helper. Call from both sites.

#### Q-7 — Stale comment claims a removed shim still exists
- **Where**: `internal/notifications/extractors/endorsement_test.go:141`
- **What**: Comment references `isValidDID in shared.go`; commit `4768da5` retired that shim.
- **Recommendation**: Update to "DID validation lives in `internal/atproto/did`; tests there cover it."

#### Q-8 — `purgeTokenClaims` has no version field
- **Where**: `internal/graphql/admin/purge.go:67-72`
- **What**: The struct's JSON shape *is* the security boundary (the HMAC is over its bytes). Adding a field later silently invalidates every outstanding token at deploy.
- **Recommendation**: Add `V int \`json:"v"\`` (= 1) now. Reject `v != 1` in `Verify`.

#### Q-9 — Count-drift and token-tamper map to the same `ErrPurgeTokenInvalid`
- **Where**: `internal/graphql/admin/purge.go:142-144`; client mapping in `settings/page.tsx:188-200`
- **What**: A real signature-tamper attack and a benign ingest-race both surface as `purge_token_invalid`. Forensics can't distinguish them.
- **Recommendation**: Add `ErrPurgeTokenCountDrift` as a distinct sentinel; return it after HMAC validates but before the count check.

#### Q-10 — CHANGELOG missing entries for two of four workstreams
- **Where**: `CHANGELOG.md`
- **What**: Actor-purge subsystem (#77) and BadgeAward subject filter (#75) have no `Unreleased` entries.
- **Recommendation**: Add two `## Unreleased — …` blocks before the next deploy.

#### Q-11 — `BatchItem` type declared inside the React component
- **Where**: `client/src/app/lexicons/page.tsx:257-263`
- **What**: `type BatchStatus` and `interface BatchItem` defined inside the function body. Re-bind on every render (TS strips at compile time, so zero runtime cost, but inconsistent with the project pattern of types at module top-level).
- **Recommendation**: Hoist to module scope.

#### Q-12 — Two near-identical `wantsXFilter` one-liners
- **Where**: `internal/graphql/schema/where.go:22-41`
- **What**: Two `func wantsXFilter(lexID string) bool` returning a single-collection match. Subsumed by the `FilterKind` enum refactor (Q-2).

---

## 5. Findings — performance

Sourced from Perf-R1; verified or refined by Verify-R2.

### 5.1 WARM (visible under load)

#### P-1 — Migration 013 is a silent no-op against migration 001
- **Where**: `internal/database/migrations/postgres/001_initial_schema.up.sql:20`; `013_add_gin_jsonb_index_concurrent.up.sql:5`
- **What**: 001 creates `idx_record_json_gin USING GIN(json)` with default `jsonb_ops`. 013 creates the same name with `jsonb_path_ops` and `IF NOT EXISTS` — silent no-op in every environment that ran 001. **Worse**: the 013 down-migration drops the 001 index. First production rollback to 012 and re-application leaves the index permanently absent.
- **Recommendation**: `DROP INDEX idx_record_json_gin; CREATE INDEX CONCURRENTLY idx_record_json_gin_path_ops ON record USING gin (json jsonb_path_ops);` as a new migration. Fix the 013 down to either recreate the 001 index or leave it alone.

#### P-2 — Contributor `EXISTS` subquery cannot use any index
- **Where**: `internal/database/repositories/filter.go:528-551`
- **What**: SQL is `CASE WHEN jsonb_typeof(...) = 'array' AND jsonb_array_length(...) <= 200 THEN EXISTS(SELECT 1 FROM jsonb_array_elements(...) WHERE ...) ELSE FALSE END`. None of `jsonb_typeof` / `jsonb_array_length` / `jsonb_array_elements` is GIN-indexable. For `org.hypercerts.claim.activity` with 10k+ rows, every contributor query is O(n) on the collection. First query for `contributor: {eq: did:plc:rare}` against a busy collection will trip `QUERY_TIMEOUT`.
- **Recommendation**: Partial GIN expression index on the extracted contributor identities, keyed by the new `FilterKind`:
  ```sql
  CREATE INDEX CONCURRENTLY idx_record_contributors_dids
    ON record USING gin ((
      ARRAY(SELECT COALESCE(c->>'contributorIdentity', c->'contributorIdentity'->>'identity')
            FROM jsonb_array_elements(json->'contributors') c)
    ))
    WHERE collection = 'org.hypercerts.claim.activity'
      AND jsonb_typeof(json->'contributors') = 'array';
  ```
  Rewrite the filter to `<extracted_array> @> ARRAY[$1]::text[]` (or `&&` for `in`). The `CASE` correctness guard stays for non-array rows (filtered out by the partial-index `WHERE`).

#### P-3 — BadgeAward `subject in [...]` is a per-row cross product
- **Where**: `internal/database/repositories/filter.go:603-613`
- **What**: For `subject: {in: [did1, …, did50]}` the SQL is `… OR EXISTS (SELECT 1 FROM unnest($n::text[]) AS d WHERE subjectURIExpr LIKE 'at://' || d || '/%')`. Postgres runs up to 50 `LIKE` evaluations per candidate row with a per-row-derived pattern that no index can serve. At `MaxInListSize=50` × 500k rows = 25M `LIKE` evaluations. Combined with the new 5s budget, the first cold `in` filter against a popular subject will trip `QUERY_TIMEOUT`.
- **Recommendation**: Materialize subject DIDs at ingestion into a generated column or a `record_subject_did(record_uri, did)` sidecar table indexed on `(did)`. Short-term mitigation: rewrite the LIKE half as prefix-equality on `substring(subjectURIExpr from 6 for ?) = ANY(...)`. Real fix is materialization.

#### P-4 — `record(did)` btree scanned twice in `purgeActor`
- **Where**: `internal/graphql/admin/purge.go:285-291, 299-303`; `internal/database/repositories/records.go:759-781`
- **What**: `PurgeActor` runs `CountByDID(did)` outside the transaction (for HMAC verify), then `DeleteByDIDTx` inside. Both scan the `idx_record_did` btree. For a DID with 100k records, the count is hundreds of milliseconds and the DELETE re-scans the same index.
- **Recommendation**: Inside the txn, issue `WITH d AS (DELETE … RETURNING uri) SELECT count(*) FROM d` and bind the HMAC verify against the returned count. Or accept that ingest-race-between-preview-and-confirm is the existing contract (which it is) and live with the double scan.

#### P-5 — pgx v5 churns TCP connections on cancel; layer-2 timeout amplifies the latency hit it's protecting against
- **Where**: `internal/database/postgres/executor.go:101-126`; commit message of `76d96df` explicitly flags this
- **What**: pgx v5's default `DeadlineContextWatcherHandler` sends `pg_cancel_backend` then `asyncClose`s the underlying TCP connection on every context cancel. With `MaxOpenConns=50` and a moderate timeout rate (5/s), every timeout churns one TCP+TLS handshake. Sustained timeouts saturate the re-establishment loop and push p99 *up*.
- **Recommendation**: Switch to `CancelRequestContextWatcherHandler` (pgx 5.3+). File the follow-up issue mentioned in the commit; link from CHANGELOG.

#### P-6 — `record_table()` always LEFT JOINs `actor` even when no caller wants `pds`
- **Where**: `internal/database/repositories/records.go:97-108`
- **What**: Every record read joins `actor`, including the unfiltered fast path. Paid on every read whether the response needs `pds` or not. Layer-2's 5s budget now polices this fixed cost.
- **Recommendation**: Conditional join — fall through `recordTable()` without the join when neither the resolver selects `pds` nor `PDSExclude` is set. Or denormalize: copy `actor.pds` into `record.pds` at insert (the actor-first ordering already ensures the actor row exists).

### 5.2 COLD (theoretical or admin-surface)

#### P-7 — `usedSigs` lazy-prune is O(N) under contention
- **Where**: `internal/graphql/admin/purge.go:151-159`
- **What**: Every Verify call iterates the entire map to evict expired entries. Cheap today (admins call this rarely); the *pattern* hidden in a hot path would be a footgun.
- **Recommendation**: Periodic sweeper goroutine started in `NewPurgeTokenSigner`; Verify lock takes lookup-and-insert only.

#### P-8 — Best-effort Tap call blocks the resolver for up to 5s
- **Where**: `internal/graphql/admin/purge.go:316`
- **What**: Synchronous 5s timeout. Operator closing the browser still holds an HTTP handler goroutine + TCP connection to Tap. Under Tap-down conditions, every purge blocks 5s.
- **Recommendation**: Fire-and-forget goroutine with a bounded pool. Or move to a `pending_tap_removals` table reconciled by a worker.

#### P-9 — Pool sizing not operator-tunable
- **Where**: `internal/database/postgres/executor.go:114-117`
- **What**: `MaxOpenConns=50` hardcoded. Issue #71 spent careful prose on the `statement_timeout` knob but the pool size — the actual contention point — needs a recompile.
- **Recommendation**: `DB_MAX_OPEN_CONNS` / `DB_MAX_IDLE_CONNS` env vars.

#### P-10 — Lexicons UI registers serially in a `for await` loop
- **Where**: `client/src/app/lexicons/page.tsx:337-353`
- **What**: For N NSIDs, the UI awaits each registration sequentially. Backend resolution hits DNS + a network registry. A bounded concurrency limiter (3-5) would cut latency 3-5× without exhausting rate limits.
- **Recommendation**: Bounded concurrency via a small `pLimit`-style helper.

#### P-11 — Purge UI countdown re-renders the entire settings page every second
- **Where**: `client/src/app/settings/page.tsx:90-118`
- **What**: `setInterval(tick, 1000)` calls `setPurgeTokenSecondsLeft` at the page-component level. The whole settings tree diffs every second for up to 5 minutes. Low impact on modern laptops; admin-only.
- **Recommendation**: Extract into a `<TokenCountdown expiresAt={…} onExpire={…} />` child so only that subtree re-renders.

---

## 6. Cross-cutting / architectural observations

Sourced from Arch-R2.

#### A-1 — Three destructive-op patterns in one resolver file
The codebase ships three confirmation models simultaneously: `purgeActor` (HMAC + count-bind + audit log + 5-min TTL + replay set), `resetAll` (literal-string compare), `addAdmin`/`removeAdmin` (prefix-only DID check or none). The drift is within a single mutation set. **Unifying shape**: extract `internal/admin/destructive.go` exposing `Op(ctx, name, dids, fn) (auditLine, err)` that handles strict DID validation, structured audit emission, and optional confirm-token verification. Migrate all five mutations to it.

#### A-2 — Per-collection filter builders are entrenching
Two single-collection special cases today. Each new filter-needing collection adds another `wants…Filter` predicate + `IsX` boolean marker + `buildXFieldFilter` + `buildXFilter`. By the fifth collection the four-file footprint is unmaintainable. The in-code TODOs explicitly said "rename when a second collection adopts" — the second has now landed. **Unifying shape**: `FieldFilter.Kind FilterKind` enum + per-lexicon descriptor registry `map[lexID]map[fieldName]filterDescriptor`. Dispatcher becomes a table lookup.

#### A-3 — `did.IsValid` rollout is uneven
Round 1 surfaced gaps in `AddAdmin`, `BackfillActor`, and the purge resolvers. **Unifying shape**: one `admin.requireValidDID` helper + a lint rule banning `strings.HasPrefix(s, "did:")` outside `internal/atproto/did`.

#### A-4 — Query-budget layering has three real gaps
SECURITY.md acknowledges one: `/admin/graphql` and `/graphql/ws` rely on Layer-1's 30s `statement_timeout` rather than the new 5s middleware. Two more in practice: subscription `/graphql/ws` runs N=64 queries per connection × 30s = 1920 connection-seconds per misbehaving client; `cmd/backfill_pds` shares the same pool defaults, so legitimate large operations now run into the 30s Layer-1 timeout. **Unifying shape**: per-route configured budget table; CLI commands get their own pool with no/longer `statement_timeout`.

#### A-5 — Migration discipline slipped on uniqueness and idempotency
The 013/001 collision is the symptom. **Unifying shape**: `TestMigrations_UniqueIndexNames` that parses all SQL files and fails on duplicate `CREATE INDEX … <name>` across the set.

#### A-6 — Env-var contract between server and client is proliferating
`EXTERNAL_BASE_URL`, `HYPERGOAT_URL`, `NEXT_PUBLIC_API_URL`, `PUBLIC_URL` — four names for two roles. `client/.env.example` lists `NEXT_PUBLIC_API_URL` AND `HYPERGOAT_URL` pointing at the same URL; one is unused except in `docs/agents/route.ts`. **Unifying shape**: canonical names per role (`BACKEND_PUBLIC_URL`, `CLIENT_PUBLIC_URL`); deprecation aliases for one release; `docs/env-vars.md` matrix.

#### A-7 — Operator docs are split across SECURITY.md / RUNBOOK.md / AGENTS.md / .env.example without cross-references
New env vars (`DB_STATEMENT_TIMEOUT_MS`, `GRAPHQL_PUBLIC_QUERY_TIMEOUT_MS`) appear in SECURITY.md but NOT in `.env.example` or RUNBOOK.md. The 90-day actor-purge retention contract is in SECURITY.md only — operators read RUNBOOK first when something breaks. **Unifying shape**: `.env.example` becomes the source of truth, with a `make verify-docs` target that greps every `getEnv(…)` against it.

#### A-8 — `clampOperationName` is the only structured-logging defense; auditing posture is uneven
Issue #71 added it for the operation-name field. The purge audit-log line and the admin path's `X-User-DID` logging do NOT use the same scrub helper. **Unifying shape**: `internal/logsafe` package with `DID(s)` and `String(s)` helpers, applied at every user-controllable slog attribute site.

#### A-9 — Round-1/round-2 review docs are accumulating without an open-debt index
Four issue directories with `plan.md` + `review-round-N.md`, each carrying "rejected with rationale" entries. No single place to check "what did reviewers raise that we deferred?" — and entries like A-2 ("rename when a second collection adopts") were silently overdue. **Unifying shape**: append-only `docs/open-debt.md` with one line per deferred finding + a trip-wire condition.

---

## 7. Test coverage and observability gaps

Sourced from Tests-R2.

### 7.1 High-severity coverage gaps

- **T-COV-1**: No resolver-level test for `PurgeActor` / `PreviewPurgeActor`. `purge_test.go` covers only the signer. A regression in transaction boundary, tap-status classification, or audit-log shape would not show up in any test.
- **T-COV-2**: No race test for HMAC used-sig set. Single-use guarantee depends on a mutex; two goroutines calling `Verify` concurrently must yield exactly one success. Not tested under `-race`.
- **T-COV-3**: Contributor / BadgeAward SQL tested at substring level only. PR #75 shipped a `subject` filter whose SQL didn't match 70% of real records; substring assertions let it through. Add a Postgres-backed table-driven test with three subject shapes (bare string, strongRef, defs#did) and two contributor shapes (bare string in array + `{$type, identity}` object).
- **T-COV-4**: pgx cancel/asyncClose race not exercised. `handler_test.go` pre-expires the context — but the real production behaviour is a slow SQL query killed mid-flight by Postgres `statement_timeout` while pgx asyncCloses the connection. CI silently passes.
- **T-COV-5**: Zero client tests exist. The countdown effect, batch lexicon flow, admin-DID gate flicker — all untested behaviour. Vitest + Testing Library setup.

### 7.2 High-severity observability gaps

- **T-OBS-1**: Purge operations have zero Prometheus signal. Add `hypergoat_purge_token_rejected_total{reason}` (invalid/expired/already_used/wrong_admin/count_drift), `hypergoat_purge_actor_total{tap_status}`, `hypergoat_purge_records_deleted` histogram.
- **T-OBS-2**: `UpdateSettings` / `ResetAll` / admin-membership mutations are not audit-logged. The most security-sensitive admin actions are *less* observable than `purgeActor`. Emit `event=admin_settings_changed actor_did=… field=admin_dids before_count=… after_count=…`; equivalent `event=reset_all`.
- **T-OBS-3**: `/health` returns healthy while Tap is down. Add `/health/deep` probing Postgres + Tap (if enabled) + Jetstream cursor freshness.
- **T-OBS-4**: No query-duration histogram on `/graphql`. Only a binary timeout counter. Tuning the new budget is impossible without distribution data.
- **T-OBS-5**: No tracing spans through GraphQL → SQL → cancel path. Layered timeouts can't be attributed to the killing layer without log forensics.

---

## 8. Verification notes

Round-2's verifier stress-tested round-1's six highest-impact claims. Summary:

| Round-1 ID | Claim | Verified? | Adjustment |
|---|---|---|---|
| S-001 | OAuth open-redirect via `\` + percent-encoding | **PARTIALLY** | `\` normalization is same-origin only; percent-encoded slashes not re-interpreted by browsers; iron-session cookie binds. Defense-in-depth, not RCE. **Kept as HIGH** because fixing is cheap and the right shape. |
| S-002 / Q-2 | Purge DID log-injection | **TRUE-WITH-CAVEATS** | slog `TextHandler` escapes control chars; HMAC JSON encoding tolerates weird DIDs. Log forgery doesn't materialise. **Downgraded to MEDIUM**, kept as hygiene fix. |
| Q-1 | Settings useState bug destroys data | **TRUE-but-narrower** | Form hydration genuinely broken. `\|\| undefined` + resolver `!= nil` prevent data loss. **Downgraded from BLOCKER to MAJOR**; trivial fix. |
| P-002 | Migration 013 silent no-op | **TRUE** | Both index names identical with `IF NOT EXISTS`. Worse: down-migration drops 001's index. Kept WARM. |
| P-003 / P-004 | New filter shapes unindexed | **TRUE** | Confirmed no index can serve either; even the hypothetical 013 GIN wouldn't help `EXISTS (jsonb_array_elements …)`. Kept WARM. |
| S-007 | AddAdmin uses HasPrefix | **TRUE-but-narrower** | Caller is already admin; bigger primitives (ResetAll) available. Hygiene fix, **kept as HIGH** to land with S-002 + S-003 in one commit. |
| S-010 | ResetAll unaudited | **TRUE** | Confirmed; the most destructive mutation in the admin surface has no audit, no token, no admin binding. **Escalated**. |
| Q-7 | Duplicate filter builders | **TRUE** | 90% identical; in-code TODO explicitly predicted entrenchment. Kept MAJOR. |

**Round-1 mitigations missed**:
1. iron-session `httpOnly + sameSite=lax` cookie on OAuth return path.
2. slog `TextHandler` quoting neutralises newline injection into audit lines.
3. JSON-encoded HMAC payload tolerates weird DIDs without breaking signature math.
4. `MaxArrayContributorScan` cap bounds per-row work on the contributor filter.
5. `|| undefined` + resolver `!= nil` guard prevents settings-form data-loss even with broken hydration.

---

## 9. Prioritized action plan

### P0 — fix this week (high impact, low cost)

| # | Finding | Effort | Owner |
|---|---|---|---|
| 1 | Q-1 settings page `useState → useEffect` form hydration | <1 hr | Client |
| 2 | P-1 migration 013 collision — new migration to drop + recreate with unique name | ~2 hr | DB |
| 3 | SEC-2 + A-1 `ResetAll` — apply purge-style HMAC + audit log + complete deletion list | ~4 hr | Server |
| 4 | SEC-3 + A-3 close `did.IsValid` rollout on `previewPurgeActor` / `purgeActor` / `AddAdmin` / `BackfillActor` + lint rule | ~3 hr | Server |
| 5 | SEC-1 OAuth `returnTo` `new URL().origin` comparison; replace string-prefix check at both `/api/login` and `/api/oauth/callback` | ~2 hr | Client |

### P1 — next two weeks

| # | Finding | Effort | Owner |
|---|---|---|---|
| 6 | P-2 + P-3 partial-GIN expression index for contributors; materialize subject DID via generated column or sidecar table | ~1 d | DB |
| 7 | P-5 switch pgx to `CancelRequestContextWatcherHandler` | ~3 hr | Server |
| 8 | Q-2 + A-2 promote to `FilterKind` enum + per-lexicon descriptor registry; collapse duplicate builders | ~1 d | Server |
| 9 | T-OBS-1 + T-OBS-2 purge metrics + audit log for `UpdateSettings` and `ResetAll` | ~4 hr | Server |
| 10 | SEC-4 admin GraphQL behind `RequireAuth`, not `OptionalAuth`; auth before body decode | ~2 hr | Server |
| 11 | Q-3 / Q-4 / SEC-7 purge resolver hardening (DB-error masking, dropped filter shapes, complete reset deletion list) | ~4 hr | Server |
| 12 | T-COV-1 + T-COV-3 resolver-level tests for `PurgeActor`; Postgres-backed shape tests for contributor + subject filters | ~1 d | Server |

### P2 — backlog (weeks 3-6)

| # | Finding | Effort |
|---|---|---|
| 13 | A-4 extend Layer-2 query budget to `/admin/graphql` and `/graphql/ws` via per-route table | ~1 d |
| 14 | T-OBS-3 / T-OBS-4 / T-OBS-5 deep health, query-duration histogram, OTel tracing | ~2 d |
| 15 | A-6 env-var canonical naming + deprecation aliases + `docs/env-vars.md` matrix | ~0.5 d |
| 16 | A-7 + A-9 RUNBOOK / SECURITY cross-references + `docs/open-debt.md` register | ~0.5 d |
| 17 | A-5 `TestMigrations_UniqueIndexNames` + extend round-trip to include columns and indexes | ~0.5 d |
| 18 | T-COV-2 + T-COV-4 race tests for HMAC; pgx cancel/asyncClose Postgres-backed test | ~1 d |
| 19 | A-8 / SEC-9 `internal/logsafe` helper + uniform application | ~0.5 d |
| 20 | P-9 + P-10 + P-11 perf cleanup: pool tunables, batch concurrency, countdown component | ~0.5 d |

### Skipped / accepted

- **SEC-5** (`validateOperatorURL` accepts internal IPs) — defer until self-hosted-by-customers becomes a real shape; operator gate is currently sufficient.
- **SEC-11** (used-token set unbounded) — bounded by verify rate × TTL in practice. Address as part of T-OBS-1.
- **P-7** (lazy prune O(N)) — current call rate makes this a non-issue. Pattern fix lives with the unbounded-set work.

---

## 10. Bottom line

Today's work is **strong on the high-risk surfaces and uneven at the boundaries**. The HMAC purge plumbing, the `did.IsValid` migration intent, the layered query budgets, and the CASE-guarded contributor SQL are all well-reasoned, properly reviewed pieces. Three patterns drift across the day's PRs: destructive ops aren't unified, filter builders are entrenching as per-collection, and the very predicate the day's biggest PR introduced was applied to fewer than half the destructive entry points.

If I had time for **one** fix tomorrow morning: close the `did.IsValid` rollout gap on the four admin destructive paths and the lint rule that bans `strings.HasPrefix(s, "did:")` outside `internal/atproto/did`. It's a 3-hour commit that retires the pattern the day's biggest PR was meant to retire.

If I had time for **one more**: hardening `ResetAll` to match `purgeActor`'s contract. It's the only mutation in the admin surface that can credibly take the system offline and the only one with no audit trail.

---

## 11. Reviewer attribution

This report is the synthesis of six independent reviewer passes:

| Pass | Lens | Findings |
|---|---|---|
| Security-R1 | Round 1 deep | 16 (S-001 → S-016) |
| Quality-R1 | Round 1 deep | 23 (Q-001 → Q-023) |
| Perf-R1 | Round 1 deep | 20 (P-001 → P-020) |
| Verify-R2 | Round 2 stress-test of 8 top claims | 8 verifications |
| Tests-R2 | Round 2 coverage + observability | 18 (T-001 → T-108) |
| Arch-R2 | Round 2 cross-cutting | 12 (A-001 → A-012) |

Round-2 verification reshaped this report — three claims (S-001, S-002/Q-002, Q-001) were downgraded based on mitigations the round-1 reviewers missed. The action plan reflects post-verification severity, not raw round-1 ranking.
