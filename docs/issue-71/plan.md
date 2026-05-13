# Issue #71 — Per-query `statement_timeout` on the public GraphQL endpoint

**Issue**: [#71](https://github.com/hb-agent/magic-indexer/issues/71)

**Status**: PLAN (round-1 amended) — ready for implementation.

This is the **post-review** plan. The original draft, the four reviewer
reports, and the per-finding accept/reject decisions are at
`docs/issue-71/review-round-1.md`.

## Branch note

Working on `feat/statement-timeout` (branched from `origin/main`)
because `staging` was carrying parallel work-in-progress at the time
this issue was started. One-time deviation from the project-default
"work on staging" rule; revisit at PR-open time depending on
`staging`'s state.

---

## Larger goal this serves

Bound the time any single query can hold a connection on the shared
50-connection Postgres pool
(`internal/database/postgres/executor.go:30`). Platform hardening:
applies to every existing and future JSON-scanning filter — the
`contributor` filter from PR #69, `excludePds`, label `EXISTS`
subqueries, full-text search.

Not the same job as reverse-proxy rate limiting (`SECURITY.md` already
calls that out). Not the same as the chi
`middleware.Timeout(60*time.Second)` already in the middleware stack
(`cmd/hypergoat/main.go:347`) — that's a request-level
context-deadline serving an HTTP-write-timeout role; we layer a tighter
inner deadline on `/graphql` specifically.

Acceptance: `SELECT pg_sleep(N)` against the public endpoint with N
exceeding the configured budget is server-side aborted, the connection
is destroyed and replaced by a fresh one, and the client sees a clear
pinned-shape `QUERY_TIMEOUT` GraphQL error.

---

## Chosen design (after round-1)

Two independent layers, both fail-safe.

### Layer 1 — pool-level `statement_timeout` (server-side hard kill)

Inject `options=-c statement_timeout=<ms>` into the `DATABASE_URL`
before `sql.Open("pgx", url)` in
`internal/database/postgres/executor.go`. Every connection in the pool
inherits the setting at session start. Catches truly stuck queries
even if the client-side cancel never lands (network blip, pgx
misbehaviour). Applies to **all paths** — public, admin, subscription
handlers, Jetstream consumer.

**Default**: 30000 ms (30 s). Env override: `DB_STATEMENT_TIMEOUT_MS`.

### Layer 2 — per-route deadline on `/graphql` (client-side budget)

Thin middleware that wraps `r.Context()` with
`context.WithTimeout(r.Context(), budget)` before the handler runs.
**Detection and response shaping happen inside the GraphQL handler**
— not in the middleware (round-1 X-C1: header mutation after
`next.ServeHTTP` is a no-op; post-handler `ctx.Err()` check races with
the timer).

After `graphql.Do` returns, the handler checks
`r.Context().Err() == context.DeadlineExceeded`. If so:

1. Sets `X-Query-Timeout: <budget-ms>` on the response header.
2. Writes `WriteHeader(http.StatusOK)` explicitly so any later
   `WriteHeader(504)` from chi.Timeout's defer is a no-op (one Go log
   line — acceptable).
3. Replaces `result.Errors` with the pinned `QUERY_TIMEOUT` shape
   (preserving any partial `result.Data`).
4. Increments `hypergoat_graphql_query_timeout_total{route="public"}`.
5. Logs at `slog.Warn` with the operation name (clamped to 128 chars,
   with `\n`/`\r`/control chars rejected — round-1 M-2).
6. `json.NewEncoder(w).Encode(result)` — atomic from the client's
   perspective.

The middleware's *only* job is installing the deadline. No header
writes, no metric increments, no `ctx.Err()` reads after the handler
returns.

**Default**: 5000 ms (5 s). Env override:
`GRAPHQL_PUBLIC_QUERY_TIMEOUT_MS`.

### Why this is the right shape

1. Server-side hard kill (Layer 1) is **client-state independent** —
   Postgres self-cancels even if pgx misbehaves.
2. Per-route override (Layer 2) tightens the public budget without
   tightening admin / subscriptions / Jetstream.
3. Operator-tunable independently via env vars; both echo in
   `LogConfig()` at startup (round-1 X-C6).
4. No resolver refactor — context already propagates cleanly through
   to pgx.
5. Reversible: removing the middleware reverts Layer 2; removing the
   URL injection reverts Layer 1.

---

## Pinned response shape (round-1 X-C5)

Locked verbatim. Consumers branch on `extensions.code` and
`extensions.budgetMs`. Locked by a golden snapshot test in the
implementation step.

```json
{
  "data": <partial data from graphql-go, or null>,
  "errors": [
    {
      "message": "query exceeded server time budget",
      "extensions": {
        "code": "QUERY_TIMEOUT",
        "budgetMs": 5000,
        "retryable": false
      },
      "path": <preserved from original error if available>
    }
  ]
}
```

`extensions.retryable: false` is load-bearing — without it, Apollo
Client's `RetryLink` and equivalents will retry the timeout and pile
on the connection pool.

`extensions.code = "QUERY_TIMEOUT"` is SCREAMING_SNAKE_CASE — the new
project convention. Reserved set (documented in `AGENTS.md`):
`QUERY_TIMEOUT`, `QUERY_TOO_DEEP`, `QUERY_TOO_LARGE`,
`UNAUTHENTICATED`, `INTERNAL_ERROR`. Future error emissions follow the
same pattern.

`X-Query-Timeout: 5000` header (value in milliseconds) is for
operator/log visibility and **must** be exposed via CORS
(`Access-Control-Expose-Headers: X-Query-Timeout`) or browser-based
GraphQL clients won't see it (round-1 X-C4). The `extensions.budgetMs`
value is the source of truth for consumers; the header is a
convenience for `curl` / server logs.

---

## URL `options=` merge (round-1 X-C2)

Naive substring match for `statement_timeout` would false-match
`idle_in_transaction_session_timeout`. The merge:

1. `url.Parse(DATABASE_URL)` → `parsed`.
2. `q := parsed.Query()`.
3. `options := q.Get("options")` — may be empty.
4. Tokenise on whitespace; look for any token matching the regex
   `^statement_timeout=` immediately following a `-c` token (or a
   single `-c\s+statement_timeout=` form).
5. If found: log `slog.Info("statement_timeout preserved from
   DATABASE_URL", "value", v)` and skip the append.
6. Otherwise: `options = options + " -c statement_timeout=" + ms`
   (with a separating space if non-empty), `q.Set("options",
   options)`, `parsed.RawQuery = q.Encode()`.
7. Return the serialised URL to `sql.Open`.

`PGOPTIONS` env-var precedence: pgx's `parseURLSettings` merges
`defaultSettings + envSettings + connStringSettings`, with the URL
winning. The plan's modification is at the URL level, so it takes
precedence over an operator-set
`PGOPTIONS=… statement_timeout=…`. Documented in SECURITY.md so
operators relying on `PGOPTIONS` don't get surprised.

`statement_timeout` value format: **bare integer** (`30000`), not
`30000ms`. Postgres interprets bare integer as milliseconds for this
GUC. Removes "did the parser get the unit?" doubt.

---

## pgx v5 pool behaviour (round-1 X-C3)

pgx v5's default `BuildContextWatcherHandler` is
`DeadlineContextWatcherHandler`, which calls `Conn.SetDeadline` on
cancel. The next read fails with an I/O timeout; pgx's error path
calls `asyncClose`:

- closes the underlying TCP connection
- asynchronously sends a CancelRequest on a fresh side connection — so
  the server-side query **is** cancelled
- discards the connection from the pool

A subsequent request acquires a freshly-opened connection that
inherits the URL-level `statement_timeout` at session start.

**Consequence**: under sustained timeout pressure, the pool churns
connections. Each new connection costs a TCP handshake + TLS + auth
(tens of ms). Acceptable for v1 — the alternative
(`CancelRequestContextWatcherHandler` to keep the connection alive)
requires switching from `sql.Open` to
`sql.OpenDB(stdlib.GetConnector(...))`, which is a larger surgery.
Future work if measurement shows churn is a real bottleneck.

---

## CORS expose-headers (round-1 X-C4)

Without `Access-Control-Expose-Headers: X-Query-Timeout` on the CORS
response, browser clients see `undefined` when reading the header from
`fetch` / Apollo `response.headers`. Fix: single-line addition in
`internal/server/cors.go:CORSMiddleware`; CORS test extended.

---

## chi `middleware.Timeout(60s)` interaction (round-1 X-C1)

The existing chi `middleware.Timeout(60*time.Second)` at
`cmd/hypergoat/main.go:347` is a context-deadline middleware. It
wraps the request context with a 60 s deadline and, in its deferred
handler, writes `504 Gateway Timeout` if
`ctx.Err() == context.DeadlineExceeded`.

The new public middleware sits *inside* chi's Timeout. When the inner
5 s deadline fires, the handler:

1. Writes `WriteHeader(http.StatusOK)` to lock in the 200 status.
2. Encodes the timeout-shaped body.

When chi's outer defer then tries `w.WriteHeader(504)`, Go's
`net/http` logs one `superfluous response.WriteHeader call` line and
discards the call. The 200-with-`QUERY_TIMEOUT`-error body is what the
client sees. `metrics.Middleware` records 200 (because that's the
first WriteHeader to land). One log line in the server is the cost;
acceptance criterion B is met cleanly.

---

## Scope

### In scope

1. **Pool-level `statement_timeout` URL injection** in
   `internal/database/postgres/executor.go:NewExecutor`.
2. **Thin deadline middleware** in
   `internal/server/middleware/timeout.go` (new dir).
3. **Handler-side detection + response shaping** in
   `internal/graphql/handler.go`. Pinned shape per the section above.
4. **`Access-Control-Expose-Headers: X-Query-Timeout`** in
   `internal/server/cors.go`.
5. **Two new env vars** in `internal/config/config.go`:
   `DBStatementTimeoutMs` (default 30000),
   `GraphQLPublicQueryTimeoutMs` (default 5000). Both echoed in
   `LogConfig()` at startup. Config `Validate()`:
   - reject `DBStatementTimeoutMs < 1000`
   - reject `GraphQLPublicQueryTimeoutMs < 100`
   - reject `DBStatementTimeoutMs <= GraphQLPublicQueryTimeoutMs`
     (Layer 1 must be strictly greater than Layer 2)
6. **New metric**:
   `hypergoat_graphql_query_timeout_total{route}` — only label value
   emitted today is `route="public"`; accessor function in
   `internal/metrics/metrics.go` mirrors `pds_resolve_total`
   precedent.
7. **`extensions.code` SCREAMING_SNAKE_CASE convention** recorded in
   `AGENTS.md`. Initial reserved set: `QUERY_TIMEOUT`,
   `QUERY_TOO_DEEP`, `QUERY_TOO_LARGE`, `UNAUTHENTICATED`,
   `INTERNAL_ERROR`.
8. **SECURITY.md "Query budgets" section** as an operator contract:
   both env vars, the layering, the reverse-proxy ordering requirement
   (`proxy_read_timeout > GRAPHQL_PUBLIC_QUERY_TIMEOUT_MS`).
9. **Tests** — unit + integration + httptest end-to-end:
   - URL-merge fixtures (pristine, with non-matching
     `idle_in_transaction_session_timeout`, with operator-set
     `statement_timeout`, with multi-flag preservation, with all
     common ssl* / application_name params surviving).
   - Middleware installs the deadline; downstream sees it.
   - Handler shapes the timeout response correctly (golden snapshot).
   - Metric accessor.
   - CORS exposes the header.
   - Integration (Postgres required):
     - `SELECT pg_sleep(N)` with N > Layer 2 budget — public path
       returns the pinned body within `budget + 200ms`, metric
       increments, header set.
     - CPU-bound probe (`generate_series(1, 1e9)`).
     - Sleep capped at `budget + 1s` in test.
     - Partial-data preservation: a query selecting two fields, one
       fast, one slow.
     - Admin path with `pg_sleep(N)` > Layer 2 but < Layer 1 completes
       normally.
     - Follow-up fast query after timeout succeeds.
   - End-to-end `httptest.Server` mounting full chi stack including
     `Timeout(60s)`.

### Out of scope (deferred)

1. **Per-admin / per-subscription middleware** — Layer 1 catches
   abuse.
2. **`X-Query-Budget-Ms` request-header override** — reserve the name
   in `AGENTS.md`; implement if demand surfaces.
3. **`CancelRequestContextWatcherHandler`** — defer until pool churn
   shows up in production.
4. **`idle_in_transaction_session_timeout` and `lock_timeout`
   companions** — follow-up issue.
5. **Plaintext 400 → unified GraphQL-shaped errors** for pre-execution
   rejects — separate refactor.
6. **`docs/api/errors.md` consumer-facing error table** — create when
   more `extensions.code` values land.

---

## File ownership

| Path | Change |
|------|--------|
| `internal/database/postgres/executor.go` | `NewExecutor(databaseURL string, statementTimeoutMs int)` — adds the URL merge described above. Idempotent on `statement_timeout`; preserves other `-c` flags. |
| `internal/database/postgres/executor_test.go` | Unit tests for the merge. |
| `internal/server/middleware/timeout.go` (new dir + file) | `QueryTimeoutMiddleware(timeout time.Duration)` — thin deadline wrapper. No header writes, no metric reads. |
| `internal/server/middleware/timeout_test.go` (new) | Unit tests. |
| `internal/server/cors.go` | Add `X-Query-Timeout` to `Access-Control-Expose-Headers`. |
| `internal/server/cors_test.go` (extend) | Assert the header is exposed. |
| `internal/graphql/handler.go` | After `graphql.Do`, detect `ctx.Err() == context.DeadlineExceeded`; replace `result.Errors` with the pinned `QUERY_TIMEOUT` shape (preserving partial `Data`); set `X-Query-Timeout` header; explicit `WriteHeader(200)`; clamp operation name before logging at Warn; increment metric. |
| `internal/graphql/handler_test.go` (new or extend) | Golden snapshot of the timeout response JSON. Tests for: deadline-exceeded path; partial-data preservation; non-timeout error path unaffected; metric increments only on timeout. |
| `internal/config/config.go` | New fields `DBStatementTimeoutMs`, `GraphQLPublicQueryTimeoutMs` with defaults and env reads. `Validate()` gates on the three rules. `LogConfig()` echoes both at startup. |
| `internal/config/config_test.go` (extend) | Test defaults, env overrides, validation gates. |
| `cmd/hypergoat/main.go` | Wire `QueryTimeoutMiddleware` via `r.With(...).Handle("/graphql", h)` and `r.With(...).Handle("/graphql/", h)`. NOT applied to `/admin/graphql` or `/graphql/ws`. Pass `cfg.DBStatementTimeoutMs` into `NewExecutor`. |
| `internal/metrics/metrics.go` | New `hypergoat_graphql_query_timeout_total{route}` counter; `GraphQLQueryTimeout(route string)` accessor. |
| `internal/metrics/metrics_test.go` (extend) | Counter increments. |
| `SECURITY.md` | "Query budgets" section. |
| `AGENTS.md` | `extensions.code` convention recorded; reserved initial set named; reserve `X-Query-Budget-Ms` request-header name as future work. |
| `CHANGELOG.md` | Unreleased entry covering both layers. |
| `docs/issue-71/plan.md` | This file. |
| `docs/issue-71/review-round-1.md` | Round-1 review decisions. |

**No migrations. No schema changes. No new tables.**

---

## Acceptance criteria

A. `SELECT pg_sleep(N)` against `/graphql` with N >
   `GRAPHQL_PUBLIC_QUERY_TIMEOUT_MS/1000` returns within
   `budget + 200ms`.

B. The response is HTTP 200 with the pinned body shape (see "Pinned
   response shape"). `extensions.code = "QUERY_TIMEOUT"`,
   `extensions.budgetMs` equals the configured budget,
   `extensions.retryable = false`. `data` carries any partial field
   results.

C. The response carries header `X-Query-Timeout: <budget-ms>`. The
   header is exposed via CORS.

D. `hypergoat_graphql_query_timeout_total{route="public"}` increments
   by exactly 1 per timed-out request.

E. A subsequent fast query succeeds — the pool re-opens a fresh
   connection that inherits the URL-level `statement_timeout`. The
   timed-out connection is destroyed (per pgx v5 behaviour); a
   follow-up `pg_stat_activity` probe shows no leaked rows.

F. `SELECT pg_sleep(N)` against `/admin/graphql` with N <
   `DB_STATEMENT_TIMEOUT_MS/1000` completes normally. With N >
   `DB_STATEMENT_TIMEOUT_MS/1000`, the pool-level safety net aborts.

G. `LogConfig()` at startup logs `db_statement_timeout_ms=...` and
   `graphql_public_query_timeout_ms=...`.

H. Config `Validate()` rejects: Layer 1 < 1000, Layer 2 < 100, Layer
   1 <= Layer 2. Tests for each.

I. Quality gates green: `go build`, `go vet`, `go test -race`,
   `golangci-lint`.

J. Integration tests pass with `TEST_DATABASE_URL` set.

K. Smoke against the staging-deployed indexer: `curl -i -X POST
   /graphql` with a deliberately slow query returns the pinned shape
   with `X-Query-Timeout` header visible.

L. `httptest.Server` end-to-end test mounts chi router with
   `Timeout(60s)` + new public middleware + GraphQL handler, fires a
   slow query, asserts status=200, body shape, header.

---

## Operator-facing notes (mirrored in SECURITY.md)

- Default budgets: 30 s pool safety net, 5 s public per-request.
- `Layer 1 (DB_STATEMENT_TIMEOUT_MS)` must be > `Layer 2
  (GRAPHQL_PUBLIC_QUERY_TIMEOUT_MS)`. Enforced at startup.
- Reverse-proxy `proxy_read_timeout` (or equivalent) **must be
  strictly greater** than `GRAPHQL_PUBLIC_QUERY_TIMEOUT_MS`. Otherwise
  the proxy times out first and the in-process budget never fires;
  Layer 1 still bounds query duration on the DB side but you lose the
  metric signal and the prompt client-facing error.
- Tune Layer 2 based on `httpRequestDuration{route="/graphql"}` p99
  from `/metrics`.
- Recommended Prometheus alert:
  `rate(hypergoat_graphql_query_timeout_total[5m]) > N`.
- `extensions.code` strings are part of the public API contract;
  renaming requires deprecation procedures.

---

## Rollback plan

Two independent reverts:

- Remove the middleware wire-up in `cmd/hypergoat/main.go` → Layer 2
  gone, Layer 1 remains.
- Remove the URL-injection in `executor.go` → Layer 1 gone, Layer 2
  remains.

`git revert` of the merge commit removes both. No data migration, no
schema change, no coordinated client work.
