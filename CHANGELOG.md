# Changelog

## Unreleased — feat(graphql): case-insensitive string operators (`eqi`, `ini`)

Adds two opt-in operators to `StringFilterInput` so consumers can
match free-form string discriminators (notably
`org.hypercerts.collection.type` = `"project"` / `"Project"` /
`"PROJECT"`) without depending on producer-side normalization. The
existing `eq` / `in` operators remain unchanged and case-sensitive;
this change is purely additive.

The `-i` suffix is the going-forward convention for
case-insensitive operator variants on `StringFilterInput`. Pinned
in the type-level description and AGENTS.md so future operators
follow the shape.

### Server

- **feat(graphql)**: `StringFilterInput.eqi: String` — case-insensitive
  equality. Emits `lower((json->>'field') COLLATE "C") = $n` with
  the bound parameter pre-lowered in Go via `asciiToLower`. ASCII
  fold only; non-ASCII characters pass through unchanged on both
  sides (no Unicode confusable folding). On its own the operator is
  not GIN-indexable — pair with a column-level filter like
  `did { eq: ... }` for selectivity. The `COLLATE "C"` choice keeps
  the expression IMMUTABLE so a follow-up functional index
  (`lower((json->>'<field>') COLLATE "C")`) remains an option if a
  single field becomes a hot case-insensitive target.
- **feat(graphql)**: `StringFilterInput.ini: [String!]` — case-insensitive
  IN. Same fold semantics as `eqi`. Bounded `1 <= len <= 50`; the
  empty list is rejected (matched `OpIn` tightening — see below).
- **refactor(filter)**: `Validate()` rejects empty `in` lists
  (`OpIn` and `OpIni` share the same validator). Previously
  `in: []` emitted `= ANY('{}')` and silently matched nothing —
  a programmer error rather than a useful query. Listed as a
  behaviour tightening for consumers running schema-diff gates;
  the GraphQL surface is otherwise additive and existing
  non-empty `in:` queries continue to work unchanged.
- **fix(types)**: `DIDFilterInput.Description` pins the
  spec-case-sensitivity contract so introspection makes the
  no-eqi/ini stance self-documenting. DIDs are spec-case-sensitive;
  case folding would be a spec violation.

### Internal

- `repositories.OpEqi`, `repositories.OpIni`, and `asciiToLower`
  added to `internal/database/repositories/filter.go`. `asciiToLower`
  is deliberately not `strings.ToLower`: it folds only ASCII A-Z
  so the bound parameter stays byte-identical to Postgres
  `lower(... COLLATE "C")` for any input (including Turkish `İ`,
  Cyrillic look-alikes, etc.).
- Coverage: unit tests pin the SQL shape, ASCII-only fold, adversarial
  input handling, field-name injection rejection, type / length /
  IsJSON validation, and the `MaxInListSize` boundary (N=50 succeeds,
  N+1 rejected). Builder-level tests pin that `eqi`/`ini` appear on
  `StringFilterInput`-typed properties of a real generated WhereInput
  (e.g. `collection.type`) and do NOT appear on `DIDFilterInput`-typed
  fields (`did`, `contributor`, `subject`).
- Plan and review trail under
  `docs/case-insensitive-string-eq/{plan,review-round-1,review-round-2}.md`.

### Notes for consumers

- Additive on the GraphQL surface; no existing query shapes change.
  Schema-diff CI gates (Apollo Rover, GraphQL Inspector) will flag
  the new fields as a non-breaking addition.
- Behaviour change: `in: []` (and `ini: []`) now error out rather
  than match nothing. Consumers should treat this as a fix — an
  empty IN list is a programmer error, not a useful query shape.
  Applies to every `FilterKind` (the scalar path *and* the
  contributor / subject DID-only paths); consumers building
  filters directly in Go should audit call-sites accordingly.
- Schema-diff tools do NOT surface the empty-IN tightening — it
  is a runtime-only change. Schema-diff CI is a poor signal for
  this class of behaviour shift; consumers must audit manually.

---

## Unreleased — chore: review follow-ups for 2026-05-13 audit (P0 + selected P1)

Lands the fixes from the six-reviewer audit recorded in
`docs/review-2026-05-13/report.md`. All twelve tracks complete (5 P0 + 6 P1 + 1 coverage).

### Server

- **feat(admin)**: `resetAll` matches the actor-purge contract.
  `previewResetAll` materializes per-table row counts and mints an
  HMAC-signed `confirmToken` bound to (admin DID, total rows, exp,
  scope=`reset_all`). `resetAll(confirmToken)` verifies the token,
  re-counts under fresh state (drift rejects with the existing
  `ErrPurgeTokenCountDrift` sentinel), and runs every DELETE in one
  transaction over a hard-listed table set covering records, actors,
  activity, labels, reports, notifications, OAuth tokens / sessions /
  replay caches, and admin sessions. `config`, `lexicon`,
  `oauth_client`, `label_definition`, and `jetstream_cursor` are
  preserved by design so the reset doesn't lock the operator out.
  Emits a structured audit log line (`event=reset_all
  requested_by_did=… rows_deleted=… tables_affected=… ts=…`) matching
  the `actor_purge` shape — SECURITY.md operator contract documents
  the ≥90d retention requirement. The signer now carries an explicit
  scope claim so an `actor_purge` token cannot cross-redeem into
  `resetAll` (and vice versa); claim shape bumped to v2 with the
  documented one-TTL window of invalidation on deploy. Client
  `mutations.ts` ships the new shape; the settings page UI follows in
  a separate track. Closes SEC-2 + A-1.

- **fix(db)**: migration 021 drops the legacy `idx_record_json_gin` and
  recreates it as `idx_record_json_gin_path_ops` with `jsonb_path_ops`.
  Migration 013 had been a silent no-op against 001 (same index name +
  `IF NOT EXISTS`) and its down dropped the 001 index — rollback degraded
  the index permanently. 013 is now neutralised (`SELECT 1`); the new
  `TestMigrations_UniqueIndexNames` guard parses every `.up.sql` and
  blocks the foot-gun recurring.

- **fix(admin)**: close `did.IsValid` rollout gap across five resolver
  entry points (`PreviewPurgeActor`, `PurgeActor`, `AddAdmin`,
  `RemoveAdmin`, `BackfillActor`), the OAuth login-hint discriminator,
  and the lexicon validator's `FormatDID` case. `strings.HasPrefix(s,
  "did:")` outside `internal/atproto/did/` is now blocked by
  `scripts/lint-no-did-prefix.sh` (wired into `make lint`). Per-line
  opt-out via the `// allow-did-prefix:<reason>` marker for genuine
  format-discriminator (not validation) cases.

- **fix(admin)**: authenticate `/admin/graphql` requests before body
  decode and depth check. Unauthenticated probes used to measure the
  body-size cap, burn lexer CPU on depth-checking probe queries, and
  fingerprint the admin schema via timing.

- **fix(db)**: pgx `CancelRequestContextWatcherHandler` (5.3+) replaces
  the default `DeadlineContextWatcherHandler`. Under the Layer-2 5s
  request budget, every public timeout was churning one TCP+TLS
  handshake (default handler asyncCloses the connection). New handler
  sends CancelRequest on a sideband connection (100ms delay) and lets
  the original connection return to the pool. DeadlineDelay (10s)
  remains a hard fallback if CancelRequest itself stalls.

- **fix(admin)**: purge resolver hardening — claim version field for
  forward-compatibility, distinct `ErrPurgeTokenCountDrift` sentinel
  (so a benign ingest race is distinguishable from active token
  tampering), `sql.ErrNoRows` distinguished from other DB errors in
  `PreviewPurgeActor`, redundant `actor_did` audit field dropped,
  periodic sweeper goroutine bounds the used-sig set to 4096 entries
  with periodic prune. Flaky tamper-the-signature test fixed.

- **perf(db)**: index the new filter shapes from PRs #64 + #75 so
  they can survive the 5s `/graphql` budget on busy collections
  (P-2 + P-3). Four migrations: 023 adds an IMMUTABLE wrapper
  function `record_contributor_identities(jsonb)` (Postgres rejects
  inline subqueries in index expressions, SQLSTATE 0A000, so the
  ARRAY-subquery has to live inside a function); 024 adds the
  partial GIN expression index `idx_record_contributor_identities`
  scoped to `org.hypercerts.claim.activity`; 025 adds the STORED
  generated column `record.subject_did` over the three BadgeAward
  subject shapes (bare-string `at://...`, strongRef object,
  `app.certified.defs#did` object); 026 adds the partial btree
  `idx_record_subject_did` scoped to `app.certified.badge.award`.
  `internal/database/repositories/filter.go` rewrites the
  KindArrayContributor and KindUnionSubject SQL to target the new
  indexes (`@>` / `&&` on the wrapper function; `=` / `= ANY` on
  the generated column). Operator note: migration 025's `ALTER
  TABLE … ADD COLUMN … STORED` rewrites the table on Postgres
  < 18 — schedule a maintenance window for >10M-row deployments.

- **feat(metrics)**: destructive-op + admin-mutation observability
  (T-OBS-1 + T-OBS-2). Five new Prometheus series:
  `hypergoat_purge_token_rejected_total{reason}` (bounded
  seven-value reason set — never `err.Error()`),
  `hypergoat_purge_actor_total{tap_status}`,
  `hypergoat_purge_records_deleted` histogram
  (1/10/100/1k/10k/100k/1M buckets),
  `hypergoat_admin_settings_changed_total{field}`, and
  `hypergoat_reset_all_total`. `PurgeTokenSigner` exposes
  `VerifyReason` alongside `Verify` so the resolver can label the
  metric without leaking the more granular reason into the error
  contract (wrong_admin and wrong_target still collapse to
  `ErrPurgeTokenInvalid`; the metric label distinguishes them).

- **feat(admin)**: audit logs for every state-changing admin
  mutation (T-OBS-2). `updateSettings` emits one
  `event=admin_settings_changed` line per applied field with
  `before` / `after` operator-controlled strings scrubbed through
  `logsafe.String`. `addAdmin` and `removeAdmin` emit
  `event=admin_added` / `event=admin_removed` with `actor_did`,
  `target_did`, `total_admins`. Shape mirrors the `actor_purge` and
  `reset_all` lines so a single log-aggregator rule routes the
  whole admin surface. SECURITY.md grows an audit-log + metrics
  table under "Admin surface".

- **feat(logsafe)**: new `internal/logsafe` package with `DID(s)`
  and `String(s)` helpers (Q-6). `DID` returns the input if it
  passes `did.IsValid`, otherwise a sentinel `<invalid-did>` marker.
  `String` replaces ASCII controls, DEL, U+2028, U+2029, and
  invalid UTF-8 with U+FFFD and truncates at 256 bytes after
  replacement so a hostile payload cannot pad past the cap.
  Applied at every audit-log slog site (purge, resetAll, the new
  admin-mutation events, the X-User-DID + API-key auth log) as
  belt-and-braces against a future bug bypassing upstream DID
  validation.

- **test(coverage)**: resolver-level + Postgres-backed shape tests
  for the purge subsystem and new filter SQL (T-COV-1 + T-COV-3).
  `purge_resolver_test.go` covers admin gating, DID validation,
  transaction boundary, tap-status classification, audit-log
  emission, and metric increments — the gaps the existing
  signer-only tests left open. `records_filter_test.go` grows
  Postgres-backed table-driven tests pinning the contributor and
  BadgeAward-subject SQL against every production shape (bare
  string, strongRef, defs#did for subject; bare string + object
  form for contributor) plus the subject_did generated column
  and the array-size + non-array guards. Locally these need a
  live Postgres (`TEST_DATABASE_URL`); CI provides one.

### Client

- **fix(client)**: settings form hydration was using `useState(()=>…)`
  as if it were `useEffect`, so the form never populated from server
  state on first load. Replaced with `useEffect(…, [settings])`. Data
  loss was prevented by the resolver's `|| undefined` guard; the
  visible bug was "I can't see my current settings."

- **fix(client)**: OAuth `returnTo` validated at the write site
  (`/api/login`) via `new URL(returnTo, env.PUBLIC_URL).origin ===
  base.origin`. The previous `startsWith("/") && !startsWith("//")`
  guard at the callback was insufficient against `/\evil.com`,
  `/%2f%2fevil.com`, and leading-whitespace tricks. Defense in depth;
  browser mitigations made the live exploit narrow.

See `docs/review-2026-05-13/implementation-plan.md` for full
tracking; `report.md` and the round-1/round-2 review docs for
provenance.

## Unreleased — feat: layered query budgets on /graphql (issue #71)

Caps the time any single query can hold a connection on the shared
Postgres pool. Two layers, both fail-safe:

- **Layer 1 — `DB_STATEMENT_TIMEOUT_MS`** (default 30 s). Injected
  into every connection via `options=-c statement_timeout=<ms>`.
  Server-side hard kill, runs regardless of client liveness.
  Applies to every path — public, admin, subscriptions, Jetstream.
- **Layer 2 — `GRAPHQL_PUBLIC_QUERY_TIMEOUT_MS`** (default 5 s).
  HTTP middleware on `/graphql` wraps the request context with a
  tighter deadline. The handler shapes the response when the
  deadline fires.

### Response shape (pinned)

Timed-out queries return HTTP 200 with a `Cache-Control: no-store`
response, an `X-Query-Timeout: <budget-ms>` header (exposed via
CORS so browser clients can read it), and the canonical GraphQL
error body:

```json
{
  "data": <partial data preserved>,
  "errors": [{
    "message": "query exceeded server time budget",
    "extensions": {
      "code": "QUERY_TIMEOUT",
      "budgetMs": 5000,
      "retryable": false
    }
  }]
}
```

`extensions.retryable: false` is load-bearing — without it,
Apollo/urql `RetryLink` middlewares retry timeouts and pile on the
pool. `extensions.code = "QUERY_TIMEOUT"` is SCREAMING_SNAKE_CASE
— the new convention for `extensions.code` strings across the
codebase (initial reserved set: `QUERY_TIMEOUT`, `QUERY_TOO_DEEP`,
`QUERY_TOO_LARGE`, `UNAUTHENTICATED`, `INTERNAL_ERROR`).

### Operator contract

- `DB_STATEMENT_TIMEOUT_MS` must be strictly greater than
  `GRAPHQL_PUBLIC_QUERY_TIMEOUT_MS`. Enforced at startup.
- Reverse-proxy `proxy_read_timeout` must be strictly greater than
  `GRAPHQL_PUBLIC_QUERY_TIMEOUT_MS` or the proxy cuts the response
  before the in-process budget fires, losing the metric signal.
- Both budgets log via `LogConfig()` at startup so the deploy
  record shows the configured values.

### URL-merge semantics

If `DATABASE_URL` already carries an explicit
`-c statement_timeout=` directive, the operator's value is
preserved (logged at INFO at startup). Multi-flag `options` values
(`-c statement_timeout=… -c search_path=…`) survive intact. The
adjacent `idle_in_transaction_session_timeout` GUC name is
explicitly disambiguated via a regex anchor so the injector does
not false-match it. `PGOPTIONS` env-var is overridden by the URL —
documented in SECURITY.md.

### pgx v5 pool behaviour

When the per-request context deadline fires, pgx v5's
`DeadlineContextWatcherHandler` closes the underlying TCP
connection and asynchronously sends a CancelRequest on a fresh
side connection. Server-side cancel lands; the pool re-opens a
fresh connection on the next acquire. Under sustained timeout
pressure the pool churns — acceptable v1 cost; future work would
register `CancelRequestContextWatcherHandler` to keep the
connection alive across cancels.

### Metrics

New `hypergoat_graphql_query_timeout_total{route="public"}`
counter. Recommended Prometheus alert:
`rate(hypergoat_graphql_query_timeout_total[5m]) > N`.

### Out of scope (deferred to follow-up issues)

- Per-admin / per-subscription middleware (Layer 1 covers them).
- `X-Query-Budget-Ms` request-header override — name reserved.
- `CancelRequestContextWatcherHandler` to keep connections.
- `idle_in_transaction_session_timeout` and `lock_timeout`
  companion budgets via the same URL mechanism.

## Unreleased — feat: contributor filter on activity records + notifications fix

Adds a server-side GraphQL filter `contributor: DIDFilterInput` on
`OrgHypercertsClaimActivityWhereInput` so consumers (primarily
`certified.app`'s profile page) can list every activity a user has
been named on as a contributor in a paginated query — issue #64.
The filter is **DID-only**: handle values are rejected at the GraphQL
layer with an actionable error. Records whose contributor identity is
a handle do not match — handle storage is a producer-side concern,
not indexed as a queryable identity. Compose with the existing `did`
filter via `_or` for "authored OR contributed" in a single query.

### Schema changes

- New field `contributor: DIDFilterInput` on the activity WhereInput
  only (`internal/graphql/schema/where.go:wantsContributorFilter`
  gates the inclusion; absence asserted on `app.certified.badge.award`).
- Field exposes `eq` and `in` (via the existing `DIDFilterInput`). No
  other operators are introduced; non-DID values are rejected at the
  resolver before SQL is built.

### Data model

- No migrations, no new tables. The filter rides the existing
  `(collection, indexed_at DESC, uri DESC)` keyset index. The
  `idx_record_json_gin` GIN index does not assist this filter (its
  `jsonb_path_ops` opclass supports `@>` etc., not the EXISTS-over-
  `jsonb_array_elements` shape the filter uses).

### SQL

- New `FieldFilter.IsArrayContributor` marker; when set,
  `buildSingleFilter` emits a guarded EXISTS subquery over
  `json->'contributors'` that COALESCEs the two contributor-identity
  shapes (bare string and `{$type,identity}` object) so both match.
- The whole shape is wrapped in `CASE WHEN <guards> THEN EXISTS(...)
  ELSE FALSE END` because Postgres does not guarantee left-to-right
  AND evaluation in WHERE; CASE is the documented escape hatch for
  forcing the guards to fire before the set-returning function.
  Two cheap guards: `jsonb_typeof = 'array'` (so a pathological
  record with a non-array `contributors` does not raise and brick
  all queries) and `jsonb_array_length <= 200` (caps per-row scan
  cost; oversized records are fail-safe invisible to the filter).
  `MaxArrayContributorScan` mirrors the existing
  `notifications.MaxContributorsBeforeReject`.

### Input validation

- Introduces `internal/atproto/did/IsValid` as the canonical input-
  validation DID predicate. Strict: lowercase method prefix
  required (`did:[a-z]+:`), no leading/trailing whitespace, length
  [8, 256], charset `[A-Za-z0-9:._-]`. Filter values are validated
  before reaching the SQL layer; an invalid value yields a clear
  GraphQL error including the rejected entry. Empty `in: []`
  lists are also rejected with a clear error rather than silently
  matching zero rows.
- The pre-existing `oauth.IsValidDID` prefix-only check is
  **removed**. The round-1 plan renamed it to
  `oauth.HasDIDMethodPrefix`; round-2 review discovered that all
  five remaining callers (labeler reset/pause endpoints,
  `UpdateSettings` admin DIDs, service-auth JWT `iss` claim,
  audit `X-User-DID` header) were vulnerable to specific attacks
  (config-key namespace escape via `/../`, log injection via
  newlines, URL injection via `?` flowing into the PLC resolver).
  All five call sites now use the strict `did.IsValid`; the weak
  helper and its test are deleted.

### Notifications extractor fix (folded into this release)

- `extractContributorDID` now reads the production object shape
  (`{$type, identity}`) in addition to the lexicon-compliant bare
  string. Before this fix, `ReasonActivityContributor` notifications
  were silently dropped for every record `certified.app` wrote.
  Released as a separate commit so a partial revert is possible.

### Metrics

- New `hypergoat_contributor_identity_total{outcome}` counter,
  incremented per contributor at ingest. Outcomes: `did` (resolved
  to a valid DID), `non_did` (string read but failed validation —
  typically a handle), `unrecognized_shape` (no string read — covers
  strong-refs and other drift). Mirrors the `pds_resolve_total`
  shape. A rising `non_did` trend nudges producers; a rising
  `unrecognized_shape` trend signals strong-refs entering production.

### Follow-up issues filed alongside this PR

- Ingest-time hard cap on `contributors` array length (the SQL
  bound is the query-side defence; record persistence is unbounded
  today).
- Per-query `statement_timeout` on the public GraphQL endpoint.
- Strong-ref variant support, gated on the
  `unrecognized_shape` metric trending up.

### Metrics — outcome classification refinement (round-2)

`contributor_identity_total{outcome}` classification was tightened
to match the plan's intent. Empty bare-string contributor
identities are now bucketed as `non_did` (a string, just not a
valid DID) instead of `unrecognized_shape`. `unrecognized_shape`
is reserved for genuinely non-string-shaped values (objects
without `.identity`, arrays, numbers, malformed JSON) so a rising
trend cleanly signals strong-refs entering production.

### Process

- Implemented under the deep-flow process (see `AGENTS.md`).
- Plan + 5-reviewer round-1 (schema, SQL, security, performance,
  ergonomics) at `docs/issue-64/`. One Critical (unbounded
  contributors-array DoS surface) plus 18 Major findings accepted
  and folded into the plan + implementation.
- Implementation + 3-reviewer round-2 (plan-fidelity, security+SQL,
  code-quality). Two more material Majors surfaced and fixed:
  Postgres WHERE-clause AND ordering is not guaranteed (now using
  CASE WHEN wrapper), and the rename of the weak oauth helper
  still left five attack-surface callers — now all migrated to the
  strict predicate and the weak helper deleted.

## 2026-05-09 — feat: server-side excludePds filter and pds field on every record

Adds a server-side filter that lets GraphQL clients exclude records authored from specific PDSes — primarily intended for hiding test-PDS content from public feeds (see consumer issue: hb-agent/maearth-social#10). Also exposes the resolved PDS endpoint as a new `pds: String` field on every record's GraphQL node so clients can render badges or branch logic on it.

### Schema changes
- New `excludePds: [String!]` arg on every record connection (`PDSFilterArgs` in `internal/graphql/query/connection.go`, composed into `ConnectionArgs`). Capped at `MaxPDSExcludeSize=32` endpoints per query.
- New `pds: String` field added to `ReservedRecordFields` and `buildRecordFields` (`internal/graphql/types/object.go`). Nullable: null means "PDS not yet resolved for this author."
- Filter semantic is best-effort: records whose author has no resolved PDS (NULL on `actor.pds`) pass through. Documented on the field description; consumers that need guaranteed exclusion should use the existing labeler-based `excludeLabels`.
- The generic `recordEvents` subscription stream does NOT populate `pds` — at insertion time the actor row may not yet exist. Field description calls this out.

### Data model
- Migration 019: `ALTER TABLE actor ADD COLUMN pds TEXT` (transactional, metadata-only on Postgres).
- Migration 020: `CREATE INDEX CONCURRENTLY idx_actor_pds ON actor(pds) WHERE pds IS NOT NULL` (non-transactional, via the existing `-- no-transaction` sentinel).
- Records are unchanged. The records query gets a `LEFT JOIN actor a ON r.did = a.did` and projects `COALESCE(a.pds, '')` into `Record.PDS` for every read path. Cardinality justifies this: PDS is a per-actor attribute (~thousands of rows), not a per-record one (~millions).

### Ingestion
- `RecordProcessor.DIDCache` field reuses the existing `oauth.DIDCache` (singleflight + TTL + cleanup goroutine in `internal/oauth/did_cache.go`). 24h TTL, 1h cleanup ticker, wired in `cmd/hypergoat/main.go` startJetstream.
- `Actors.UpsertWithPDS(ctx, did, handle, pds)` replaces `Upsert` on the ingestion path. `Upsert` is kept as a thin wrapper (no behaviour change for callers that don't pass a pds). On conflict, pds uses `COALESCE(EXCLUDED.pds, actor.pds)` so transient resolver failures don't blank a previously-resolved value.
- Resolution is best-effort: cache miss + transient failure logs at warn and persists the actor with empty pds. Singleflight collapses bursts of records from the same DID into one upstream call.

### Backfill
- New `cmd/backfill_pds` CLI: scans actors with NULL/empty pds, resolves each via the same `oauth.DIDCache`, writes via the new pure-UPDATE `Actors.SetPDS` (does not touch `handle` or `indexed_at`). Defaults: 8 workers, 10 req/s rate-limited, `--force` to re-resolve regardless. Idempotent and safe to interrupt.

### Metrics
- New counter `hypergoat_pds_resolve_total{outcome}` with labels `ok`, `failed`, `no_endpoint`.

### Tests
- Repo unit: `actors_test.go` covers `UpsertWithPDS` (set, COALESCE preserve, non-empty overwrite), `SetPDS` (UPDATE-only, indexed_at preserved, missing-actor noop). `records_filter_test.go` covers the JOIN behaviour, NULL-pds pass-through, multi-endpoint exclusion, and that the `pds` is populated on the returned `Record`.
- Schema: `builder_test.go` pins that the `pds` field exists on every record type and `excludePds` arg appears on per-collection and generic-records connections.
- GraphQL parse: `connection_test.go` covers `ParsePDSExcludeFilter` (nil/null/empty handling, dedupe with first-occurrence preservation).
- Ingestion: `processor_test.go` covers `resolvePDS` (nil cache → empty, cache hit → endpoint, labeler-only DID → empty).
- Integration: `TestPublicGraphQL_ExcludePdsFilter` exercises the full ingest-to-query path through the real GraphQL handler.

### Backwards compatibility
- Purely additive: existing GraphQL clients see no schema break. Pre-existing `Actors.Upsert(ctx, did, handle)` calls continue to work unchanged.

## 2026-04-14 — Fix: notifications aggregated upsert fails with SQLSTATE 42P10 (#61)

The aggregated-envelope upsert in `internal/notifications/repo.go` omitted the partial-index predicate on `ON CONFLICT`, so every call failed with Postgres error 42P10 (`invalid_column_reference`). No notifications were ever persisted, and `/notifications/graphql` always returned an empty feed.

Fix: add `WHERE group_key IS NOT NULL` to the `ON CONFLICT` clause so it matches the partial unique index `notification_group_idx` defined in migration 015. Mirrors the working pattern already used in `internal/database/repositories/labels.go:90`.

Also adds `internal/notifications/repo_test.go` with 9 Postgres integration tests covering the fix and surrounding aggregation behaviour (new envelope, cross-record aggregation, in-batch replay, cross-call replay, older-SortAt preservation, non-aggregated path, non-aggregated replay cleanup, cross-DID isolation, null `reason_subject`). Extends `testutil.resetBetweenTests` to include the `notification`, `notification_participant`, and `actor_state` tables from migration 015.

## 2026-04-14 — Fix: `collectionOverview` rejected by strict Postgres (#59)

`GetCollectionOverview` joined the raw `record` table to a pre-aggregated invalid-count subquery and grouped only by `r.collection`. Postgres strict mode rejects this because `inv.invalid_count` isn't in `GROUP BY` and `r.collection` isn't a primary key, so the functional-dependency rule doesn't cover the joined column. Surfaced as **"No collections found"** in the admin UI after the #57 deploy rebuilt the container.

Fix: aggregate both sides to `collection` first, then `LEFT JOIN`. Same result shape, no ambiguous columns. No schema or API change.

## 2026-04-14 — AT Protocol service-auth for notifications (#57)

Replaces the shared-secret `INDEXER_ADMIN_API_KEY` + `X-User-DID` path for the notifications API with AT Protocol service-auth JWTs (spec: https://atproto.com/specs/xrpc). Any ATProto client can now query its own user's notifications directly — no shared admin secret in the request path. Landed after 4 rounds of 8-reviewer plan review and 1 round of 5-reviewer implementation review.

### New endpoint: `/notifications/graphql`
- Accepts a service-auth JWT in `Authorization: Bearer …`. Verifier validates signature, audience (`DOMAIN_DID`), expiry, `lxm` (must be `com.hypergoat.notification.query`), and replay. Successful verification injects the issuer DID onto the request context; resolvers read it instead of a `did` argument.
- GraphQL fields exposed: `notifications`, `unreadNotificationCount`, `updateNotificationsSeen` — identical to the admin schema minus the `did` arg.
- Gated by `DOMAIN_DID`: endpoint mounts only when set. Admin-key path at `/admin/graphql` is unchanged so certs-social continues to work during transition.

### Verifier implementation (`internal/oauth/`)
- `ServiceAuthVerifier.Verify(ctx, token, expectedLxm)` returns the issuer DID on success, one of 20 `ErrServiceAuth*` sentinels on failure.
- Supports **ES256** (P-256) and **ES256K** (secp256k1). ES256K is a custom `jwt.SigningMethod` wrapping `decred/dcrd/dcrec/secp256k1/v4`. IEEE-P1363 `r||s` 64-byte format enforced; DER rejected; panic-recovered.
- `#atproto` verification method with `type: "Multikey"` required. `publicKeyMultibase` decoded via proper multibase + multicodec varint, compressed-point-only (33 bytes), point-on-curve validated.
- `alg` allowlist enforced before key resolution (`jwt.WithValidMethods`) so `alg=none` / `HS256` reject pre-signature.
- Replay cache: bounded LRU by expiry, atomic `LoadOrStore`, capacity 100k.
- `jti` optional (matches older PDSes that don't emit it). When absent, the token must have `iat` and replay key is synthesised from signature bytes — ECDSA is non-deterministic so re-signings produce fresh keys.
- Low-s malleability deliberately **not** enforced — ATProto ecosystem is lenient.
- `lxm` pinned per middleware, exact-match. A token minted for one endpoint can't be replayed on another.

### `/.well-known/atproto-did`
Publishes `cfg.DomainDID` as `text/plain` when `DOMAIN_DID` is `did:web:<ourHost>`. Required by the did:web spec for third-party verification of our `aud`. For `did:plc:` the DID document lives on the PLC directory; we emit nothing there.

### Metrics
- `hypergoat_service_auth_verified_total{lxm}` — successful verifies.
- `hypergoat_service_auth_rejected_total{reason, lxm}` — 18 bounded reason labels (`bad_audience`, `expired`, `did_resolve_not_found`, `bad_signature`, `replay`, …) so a 401 spike is diagnosable from the dashboard.
- `hypergoat_did_resolve_served_stale_total` — reserved for the follow-up serve-stale path.
- `hypergoat_notifications_request_total{endpoint, field}` — counts admin vs xrpc hits per field. This is the gate for deleting the admin-path notification fields.

### Config (`internal/config/`)
- `DOMAIN_DID` — fatal at startup if malformed, `did:plc:` or `did:web:` only. Unset = endpoint disabled (soft), which is the default on prod today.

### Tests
40+ cases across the oauth + server packages: ES256/ES256K happy paths, tampered payload, bad aud/lxm/exp/iat, alg=none, alg swap, HS256/RS256 rejection, forged iss (attacker signs claiming victim's DID), replay, missing-jti-and-iat, jti-only / iat-only accepted, pubkey varint / codec / compression edge cases, concurrent JTI check-and-set with `-race`, well-known handler branches, XRPC handler missing-DID / non-POST / bad-JSON / introspection. A `TestAllServiceAuthSentinelsCovered` test asserts the sentinel-error registry stays complete.

### Deferred to follow-ups (acknowledged plan items)
- Per-`iss` + client-IP token-bucket throttle on DID resolution (sentinel `ErrServiceAuthThrottled` exists, not yet fired).
- Negative cache + serve-stale on PLC outage (metric helper exists, not yet called).
- Key-rotation retry on `bad_signature`.
- `caller_hash` label on `notifications_request_total` for per-caller migration visibility.
- Persistent `jti` store (reuse `oauth_dpop_jti` table pattern) once we scale past one replica.
- certs-social client port: switch from admin proxy to direct service-auth calls against `/notifications/graphql`.

## 2026-04-13 — Issues #22, #24, #26, #33, #53 bundled PR

Single PR closing five open indexer issues. Three rounds of 5-reviewer plan review and two rounds of 3-reviewer implementation review informed the design.

### #24 — OAuth refresh token DPoP key binding (`internal/oauth`, `internal/server`, `internal/database`)
- `oauth.RefreshToken` gains `DPoPJKT` (SHA-256 JWK thumbprint) and `OriginalIssuedAt` (rotation-stable issuance time). New refresh tokens are bound to the DPoP key used at issuance; refresh requests must present a matching JKT (constant-time compare).
- Legacy (pre-binding) tokens are accepted only if `OriginalIssuedAt` predates the `OAUTH_LEGACY_DPOP_JKT_CUTOFF` env var — this is the sunset window. Tokens issued after the cutoff without a JKT are rejected.
- `OAUTH_LEGACY_DPOP_JKT_CUTOFF` is **required**; startup fails closed if unset or non-positive.
- Migration 016 adds `dpop_jkt` and `original_issued_at` columns with a backfill (legacy rows inherit `created_at`).
- Metrics: `hypergoat_oauth_refresh_jkt_mismatch_total`, `hypergoat_oauth_refresh_legacy_null_jkt_total`, `hypergoat_oauth_refresh_legacy_expired_total`. Watch the legacy counter fall to zero before shortening the window.

### #22 — Schema restart on lexicon upload (`internal/graphql/admin`, `cmd/hypergoat`)
- `UploadLexicons` now stages every entry, runs a pre-commit schema build over a cloned registry (`SchemaValidateCallback`), and only writes to the DB if the resulting GraphQL schema compiles. Malformed uploads are rejected wholesale.
- On successful upload, `ProcessRestartCallback` signals `serve()` to gracefully shut down; `main` exits with code 42 so the orchestrator (Railway) restarts the process. The new schema is picked up on boot.
- Replaced an earlier hot-swap design with this exit-on-success flow after review — the hot-swap required atomic pointer juggling and introspection invalidation, while Railway already handles restarts cheaply.

### #26 — Bluesky-style `sortAt` feed ordering (Deploy 1 of 2) (`internal/ingestion`, `internal/database`)
- Migration 017 adds a nullable `sort_at timestamptz` column and a `CONCURRENTLY`-built `(collection, sort_at DESC NULLS LAST, uri DESC)` keyset index.
- `ingestion.ComputeSortAt(createdAt, now)` returns `min(createdAt, now + 5m)`, falling back to `now` when `createdAt` is absent or unparseable. Matches Bluesky's clock-skew clamp so a misconfigured client can't pin itself to the top of the feed.
- `RecordsRepository.InsertWithParams` takes an `InsertParams` struct (forward-compatible) with an optional `SortAt *time.Time`. `RecordProcessor` extracts `createdAt` from the record JSON envelope and writes the clamped value on every create/update.
- Deploy 2 (follow-up) will backfill existing rows, flip the column to NOT NULL, and expose a `sortAt` GraphQL field plus `ORDER BY COALESCE(sort_at, indexed_at)` queries.

### #33 — ActivityChart dark-mode colors (`client/`)
- Six `--chart-creates|updates|deletes-{stroke,fill}` CSS variables in `globals.css`, overridden under `[data-theme="dark"]`. `ActivityChart.tsx` reads them via `var(--…)` — the theme swap no longer triggers React re-renders.

### #53 — Filter tests + `DateTimeScalar` in datetime filters (`internal/graphql/types`)
- `DateTimeFilterInput.{eq,neq,gt,lt,gte,lte}` now use `DateTimeScalar` instead of `graphql.String`, so clients get ISO-8601 validation at the GraphQL boundary.
- New `filters_test.go` covers field presence, scalar wiring, and lexicon-type dispatch.

### Config
- `OAUTH_LEGACY_DPOP_JKT_CUTOFF` (new, required) — Unix timestamp of the DPoP-binding deploy. Set to roughly the time of this release.

## 2026-04-14 — Notifications subsystem (v1)

Bluesky-pattern notification system for certs.social, built after 3 rounds of
10-reviewer plan feedback. Two notification types in v1: endorsement received,
activity contributor mention. Server-side aggregation for endorsements.

### Schema (migration 015)
- `notification` — notification envelopes, aggregated on `(did, group_key)` for types that opt in
- `notification_participant` — per-source-record participation rows, unique on `(record_uri, recipient_did)` for idempotency
- `actor_state` — per-user seen watermark

### Package: `internal/notifications/`
- `Notifier` interface, registry, repository
- Post-insert `RecordHook` attached to shared `RecordProcessor` (runs with `HookLogContinue` — a failing extractor cannot stall firehose ingestion)
- Extractors for `app.certified.temp.graph.endorsement` (aggregating) and `org.hypercerts.claim.activity` (non-aggregating, fan-out per contributor)
- `clampSortAt` clamps record timestamps to `[now-7d, now]` to prevent out-of-range sort_at values
- `isValidDID` syntactic validation + `MaxReasonSubjectBytes` cap defend against untrusted input
- Fan-out capped at `MaxFanOutPerRecord = 100`; oversized contributor lists rejected early via shallow pre-check

### GraphQL API (admin endpoint)
- `Query.notifications(did, reasons, first, after)` with cursor pagination
- `Query.unreadNotificationCount(did)` returning `{count, more}`, capped at 50+
- `Mutation.updateNotificationsSeen(did, seenAt)` with monotonic GREATEST watermark + clamp to `now()`
- Cursor V3 for notifications: base64-URL JSON array `["v1:notif", sort_at_iso, id]`
- Resolvers registered via `admin.WithExtraQueries` / `admin.WithExtraMutations` options

### Hook infrastructure (`internal/ingestion/`)
- New `RecordHook` type with `HookErrorPolicy` (`HookLogContinue` or `HookAbortTx`)
- `RecordProcessor.RecordHooks []RecordHook`, called sequentially after record insert with panic recovery

### Configuration
- `NOTIFICATIONS_ENABLED` env var (default false) — per-service Railway flag for staged rollout
- Documented in `.env.example`

### Deferred (follow-up work)
- Same-transaction hook (requires refactor across all repos)
- Per-reason circuit breaker / kill switch
- Top-N authors (`latest_authors text[]`) for "Alice, Bob, and 3 others" rendering
- Count-drift reconciler
- Public-endpoint migration when OAuth ships on `/graphql`
- Push notifications, preferences, activity subscriptions

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
