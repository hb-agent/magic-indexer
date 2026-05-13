# Issue #71 — Plan review, Round 1

**Date**: 2026-05-13
**Plan reviewed**: `docs/issue-71/plan.md` before round-1 amendments.
**Reviewers**: four parallel agents — DB/pgx semantics, HTTP middleware
correctness, security & operator ergonomics, API consumer ergonomics.

---

## Verdicts

| Lens | Verdict | Critical | Major | Minor |
|------|---------|----------|-------|-------|
| DB/pgx semantics | approve with conditions | 2 | 5 | 5 |
| HTTP middleware | **rework needed** | 3 | 4 | 4 |
| Security / operator | approve with conditions | 2 | 5 | 4 |
| API consumer | approve with conditions | 2 | 5 | 3 |
| **Totals** | | **9** | **19** | **16** |

Deduplicated, ~6 unique Criticals and ~14 unique Majors — three
reviewers independently caught the same header-after-write bug, two
caught the URL-options-merge regex fragility, etc.

The architectural shape (Layer 1 URL-level `statement_timeout` + Layer 2
per-route deadline) is sound across all four lenses. The Criticals are
all at the *implementation contract* level — wrong claims in the plan
about pgx v5 pool behaviour, wrong middleware shape, wrong
acceptance criteria, and missing operator/consumer ergonomics edges.

---

## Cross-cutting Criticals (caught by ≥2 reviewers)

| ID | Sources | Finding | Decision |
|----|---------|---------|----------|
| X-C1 | DB-C1; HTTP-C1, C2, C3; API-C2; Sec-M5 | Three independent reasons the original middleware shape is broken: (a) `w.Header().Set` after `next.ServeHTTP` is a no-op (headers already flushed by `json.NewEncoder`); (b) post-handler `ctx.Err()` race can falsely fire the metric for a non-timeout error returned at T-just-under-budget; (c) chi `middleware.Timeout(60s)` already wraps `r.Context()` with a deadline + a deferred 504 writer — layering the new 5s deadline inside it would produce a 200 body + 504 status race. | **A** — collapse into one structural fix: **timeout detection and response shaping move into the GraphQL handler**. The new middleware's only job is installing the deadline on `r.Context()`. The handler, after `graphql.Do` returns, checks `r.Context().Err() == context.DeadlineExceeded` and, if so, sets the `X-Query-Timeout` header, replaces the response body with the pinned `QUERY_TIMEOUT` shape (preserving any partial `data`), and increments the metric — all *before* the encoder writes a byte. |
| X-C2 | DB-C2; Sec-C1 | URL `options=` merge using `url.Values.Get/Set` and naive substring search will (a) silently shadow operator-set `-c statement_timeout=Ns` if the existing value carries multiple `-c` flags; (b) false-match `idle_in_transaction_session_timeout` as containing `statement_timeout`. | **A** — change the merge to: parse `options`, split on whitespace, regex-anchor the match (`(^|\s)-c\s+statement_timeout=`), and skip the append (logging "operator statement_timeout retained: <value>") if present. Concatenate when appending, not `Set`. Test fixtures cover both edges. Plan also documents `PGOPTIONS` env-var precedence (pgx reads it; URL wins). |
| X-C3 | DB-C1 | The plan's "Layer 2 returns the connection to the pool cleanly" is **factually wrong for pgx v5**. `DeadlineContextWatcherHandler` calls `asyncClose` on context cancel — the connection is destroyed and a fresh one is established. The server-side cancel still lands, but the pool turns over. | **A** — rewrite acceptance criterion E to "a follow-up query succeeds; the timed-out connection is *destroyed* and the pool opens a fresh one inheriting the URL-level `statement_timeout`." Plan's performance section also gets a "pool churn cost under sustained timeout pressure" note. The alternative (registering a `CancelRequestContextWatcherHandler` to keep the connection alive) is recorded as a future enhancement; not in scope here. |
| X-C4 | API-C1 | `X-Query-Timeout` is **invisible to browser-based GraphQL clients** without an `Access-Control-Expose-Headers: X-Query-Timeout` entry in the CORS middleware. The plan's whole "client distinguishes timeout from real error via header" claim collapses for the primary consumers (`certs-social`, `certified-app`). | **A** — add `Access-Control-Expose-Headers: X-Query-Timeout` to `internal/server/cors.go:CORSMiddleware` AND echo the budget value inside `extensions.budgetMs` so a header-less client still gets it machine-readable. Header stays for operator/log visibility; `extensions.budgetMs` is the source of truth for consumers. CORS test extended. |
| X-C5 | API-C2 | Response body shape was left ambiguous ("or similar canonical shape") — consumers will branch on the exact string, so it must be pinned in the plan and locked by a golden test. | **A** — pin the shape verbatim: `{"data": preserved, "errors": [{"message": "query exceeded server time budget", "extensions": {"code": "QUERY_TIMEOUT", "budgetMs": N, "retryable": false}, "path": ...}]}`. Document `extensions.code` SCREAMING_SNAKE_CASE convention in `AGENTS.md`. Reserve `QUERY_TIMEOUT`, `QUERY_TOO_DEEP`, `QUERY_TOO_LARGE`, `UNAUTHENTICATED`, `INTERNAL_ERROR` as the planned set. |
| X-C6 | Sec-C2 | The `LogConfig()` echo at startup is the canonical operator signal — without it, operators can't tell which budget is active per deploy. The plan's file ownership omitted this. | **A** — add `internal/config/config.go:LogConfig()` to file ownership: two new lines logging `db_statement_timeout_ms` and `graphql_public_query_timeout_ms`. Acceptance criterion G updated. |

## Major findings

| ID | Source | Decision | Rationale |
|----|--------|----------|-----------|
| M-1 | Sec-M1 | **A** | "5s × 50 = 250 connection-seconds per burst" oversold the defence — bounds per-query time but not aggregate concurrency. Plan rewritten to scope the security claim to per-query connection-hold, with explicit "reverse-proxy rate limit is still the load-shedding mechanism." |
| M-2 | Sec-M2 | **A** | Clamp operation name to 128 chars; reject `\n`/`\r`/control chars before logging. Apply same fix to admin handler in a follow-up issue. |
| M-3 | Sec-M3, API-m11 | **A** | Pin `extensions.code` as SCREAMING_SNAKE_CASE; document in AGENTS.md as the project convention. Reserve the initial set. |
| M-4 | Sec-M4 | **A** | Integration `pg_sleep` tests cap the sleep at `min(N, budget + 1s)` so a misconfigured-test-env doesn't accidentally hold connections; tests explicitly set both layers to small values via test-only env override. |
| M-5 | Sec-M5 | **A** | SECURITY.md "Query budgets" section: reverse-proxy read timeout must be > `GRAPHQL_PUBLIC_QUERY_TIMEOUT_MS`; otherwise proxy cuts first, in-process budget never fires, Layer 1 still holds the connection until the safety net. |
| M-6 | HTTP-M1 | **A** | The original "wire to `r.Post('/graphql', …)`" would drop GET, OPTIONS preflight, and `/graphql/`. Plan now uses `r.With(timeoutMW).Handle("/graphql", h)` plus `r.With(timeoutMW).Handle("/graphql/", h)` to preserve current routing. Subscription path `/graphql/ws` confirmed unaffected (sibling route, separate registration). |
| M-7 | HTTP-M2, HTTP-M3 | **A** | Detection signal is `r.Context().Err() == context.DeadlineExceeded`, not `result.Errors` string-matching. `graphql-go`'s `FormattedError` doesn't implement `Unwrap()`; relying on `errors.Is` against the formatted error is fragile. The context's `Err()` is the authoritative source. |
| M-8 | HTTP-M4 | **A** | The handler does the timeout check + body rewrite *before* `json.NewEncoder(w).Encode(...)`. Handler explicitly calls `w.WriteHeader(http.StatusOK)` before any body write, making chi's potential subsequent `WriteHeader(504)` a no-op + a single Go log line (acceptable). |
| M-9 | API-M3 | **A** (already X-C5) | Covered by the pinned shape: `extensions.retryable: false` ships in v1. |
| M-10 | API-M4 | **A** (with documented trade-off) | HTTP 200 + GraphQL errors[] stays. Plan now documents the trade-off: HTTP-level monitoring is blind to timeouts; the Prometheus counter is the compensating signal. PR body lists the recommended Prometheus alert. |
| M-11 | API-M5 | **A** | `X-Query-Timeout` semantics: "the budget that was exceeded" (header value = configured budget in ms). Spelled out in the response-shape pin. `extensions.budgetMs` carries the same machine-readable value. |
| M-12 | API-M6 | **A** | Partial `data` is preserved — `result.Data` flows through verbatim; `result.Errors` is replaced/annotated with the canonical `QUERY_TIMEOUT` error. Plan acceptance criterion adds: "a query selecting two fields where one is fast and one is slow returns the fast field's data and the slow field's error has `code: QUERY_TIMEOUT`." |
| M-13 | DB-M1 | **A** | Plan adds a code comment in `executor.go`: "Do not issue `SET statement_timeout` without LOCAL; the URL-level value is the contract." A grep-based CI lint isn't worth the complexity today — comment + review is enough. |
| M-14 | DB-M2 | **A** | URL re-serialisation: unit test asserts a fixture with `sslmode=require&sslrootcert=/path/with%20space/ca.pem&application_name=hypergoat` survives the rewrite. The percent-encoding form change is benign because pgx parses either form. |
| M-15 | DB-M3 | **A** | Use bare-integer form: `-c statement_timeout=30000` (no `ms` suffix). Eliminates "did the parser get the unit?" doubt. |
| M-16 | DB-M4 | **A** | Add a CPU-bound probe alongside `pg_sleep`: `SELECT count(*) FROM generate_series(1, 1e9)` to verify `statement_timeout` actually fires on the workload shape. |
| M-17 | API-M7 | **D** (reserved) | Per-request budget override via `X-Query-Budget-Ms` header: defer; plan notes the name is reserved so we don't paint ourselves into a corner. Out-of-scope item. |

## Minor & nice-to-have — folded into plan

1. **Mechanical / belt-and-braces** — folded into implementation:
   - Validate `Layer 1 > Layer 2` at config startup (Sec-n1)
   - Reject `< 100ms` on Layer 2 and `< 1000ms` on Layer 1 (Sec-n2)
   - `Cache-Control: no-store` on the timeout response (Sec-n4)
   - `httptest.Server` integration test of full middleware-handler-router stack (HTTP-n2)
   - Defensive `WriteHeader(200)` before encoder (HTTP-n3)
2. **Documentation tone** — folded into SECURITY.md / AGENTS.md
3. **Future-work-only** — recorded for later:
   - `CancelRequestContextWatcherHandler` to keep connections alive across cancel
   - Per-resolver budget overrides
   - Plaintext 400 → unified GraphQL error responses for pre-execution rejects
   - `docs/api/errors.md` consumer-facing error table

---

## Round 2?

**No.** The 6 cross-cutting Criticals all collapse into one structural
fix (move timeout detection and response shaping into the handler) plus
a handful of localised changes (URL regex, CORS expose-headers, pinned
shape, LogConfig echo). The fixes are mechanical once the design call
is made; the implementation review will catch any miss.

The plan is amended in place. The implementation step proceeds.

---

## Plan changes summary

1. Middleware is a thin deadline wrapper; handler owns detection + shaping.
2. URL options merge — regex-anchored, concatenate not Set, PGOPTIONS precedence documented.
3. Response body shape pinned verbatim (extensions.code/budgetMs/retryable, message text).
4. CORS expose-headers — `X-Query-Timeout` added.
5. Acceptance criterion E corrected for pgx v5 pool churn.
6. `LogConfig()` echoes both budgets at startup.
7. chi.Timeout collision documented; handler writes 200 first to make chi's defer a no-op.
8. `statement_timeout` value format — bare integer (ms), not `Nms`.
9. Tests: `pg_sleep` capped, CPU-bound probe added, partial-data preservation test added, end-to-end httptest.
10. Validation: config-level Layer 1 > Layer 2; lower-bound rejections on both.
11. SECURITY.md "Query budgets" section as operator contract.
12. AGENTS.md records `extensions.code` SCREAMING_SNAKE_CASE convention + initial reserved set.

---

## Follow-up issues to consider after #71 lands

1. `idle_in_transaction_session_timeout` and `lock_timeout` on the pool via the same `options=-c` mechanism.
2. Per-resolver / per-route budget overrides via `X-Query-Budget-Ms` request header (M-17 deferral).
3. Clamp un-sanitised user-controlled fields in admin handler logging (M-2 follow-up).
4. Plaintext `400` pre-execution errors → unified GraphQL-shaped error responses.
5. Consumer-facing `docs/api/errors.md` table of `extensions.code` values.
