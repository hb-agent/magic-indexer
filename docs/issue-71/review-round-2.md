# Issue #71 — Implementation review, Round 2

**Date**: 2026-05-13
**Implementation reviewed**: `feat/statement-timeout` at `76d96df`.
**Reviewers**: three parallel agents — plan-fidelity, security & SQL,
code quality & tests.

---

## Verdicts

| Lens | Verdict | Critical | Major | Minor |
|------|---------|----------|-------|-------|
| Plan-fidelity | approve with conditions | 0 | 3 | 3 |
| Security & SQL | approve with conditions | 0 | 2 | 4 |
| Code quality & tests | approve with conditions | 0 | 3 | 10 |

No Criticals. All three reviewers agree the architectural shape is
right and round-1's structural fixes landed at the cited file:line
locations. The Majors cluster on two themes: **the integration test
surface promised in the plan was not delivered**, and the URL-merge
regex is narrower than Postgres's accepted option syntax.

---

## Cross-cutting Majors (caught by ≥2 reviewers)

| ID | Sources | Finding | Decision |
|----|---------|---------|----------|
| X-M-1 | PlanFid-M-INTEG-1; CQ-M1; Sec-m2 | Integration tests against Postgres are completely absent: no `pg_sleep`, no `generate_series`, no admin-path verification, no follow-up-fast-query test. Acceptance criteria A, D, E, F, J are unverified by any test. | **A** — add `internal/integration/timeout_integration_test.go` (gated on `TEST_DATABASE_URL`) covering Layer 1 firing on `pg_sleep`, the post-timeout pool recovery (criterion E), and the admin pass-through (criterion F). Cap each `pg_sleep(N)` at `min(N, budget + 1s)` per round-1 M-4. |
| X-M-2 | PlanFid-M-METRIC-1; CQ-M2; CQ-M3 | The `metrics.GraphQLQueryTimeout` counter is never asserted. Acceptance criterion D ("increments by exactly 1 per timed-out request") is unverified. | **A** — add a unit test in `internal/metrics/metrics_test.go` using `counterDelta` (same shape as PR #69's notifications counter test) **and** wrap the timeout-path assertion in `handler_test.go` with a `counterDelta` check. |
| X-M-3 | PlanFid-M-INTEG-2; CQ-M1 | No end-to-end test mounts the full chi stack (`middleware.Timeout(60s)` + new `QueryTimeoutMiddleware` + handler) and fires a slow request. Acceptance criterion L is unverified. This is the single most fragile assumption in the design — the chi.Timeout collision narrative deserves a regression guard. | **A** — add `internal/graphql/handler_chi_e2e_test.go` (or extend `handler_test.go`) using `httptest.Server` with a chi router carrying both middlewares. Resolver blocks on a small `<-ctx.Done()` so the test doesn't need Postgres. Assert status=200, body shape, header. |

## Lens-specific Majors

### Plan-fidelity (PlanFid)

| ID | Finding | Decision |
|----|---------|----------|
| PF-MIN-1 | `Path` field in the timeout error is hardcoded `nil`; plan said "preserved from original error if available" | **A** | Plan's "if available" makes this fine for graphql.Do timeouts (which don't surface a useful path), but the implementation comment in `handler.go` should explain why we don't read from the original errors. One-line comment update. |

### Security & SQL (Sec)

| ID | Finding | Decision |
|----|---------|----------|
| **Sec-M1** | The regex `(^|\s)-c\s+statement_timeout=` misses two Postgres-accepted syntactic variants: `-cstatement_timeout=` (no space — getopt short-option-with-value syntax) and `--statement_timeout=` (long-option form, explicitly documented as equivalent to `-c name=value`). An operator using either form would have the indexer's default silently appended on top — silently breaking the "operator override preserved" contract. | **A** — broaden to `(^|\s)(-c\s+\|-c\|--)statement_timeout=`. Add three test cases covering each accepted shape. Also add a negative test for `--statement_timeout_other=` to confirm the regex doesn't false-match a longer-named GUC. |
| **Sec-M2** | `config.Validate()` does not enforce `GraphQLPublicQueryTimeoutMs < 60000` (chi's outer `middleware.Timeout(60s)` ceiling). An operator setting Layer 2 = 120s would silently get a 60s effective budget; the `X-Query-Timeout` header and `extensions.budgetMs` advertise a value that never actually fires. The layering invariant in SECURITY.md is breached. | **A** — extend `Validate()` to reject `GraphQLPublicQueryTimeoutMs >= 60000` with an error message naming the chi constant. Hoist the chi 60s into a named constant so the relationship is discoverable. Add a test case. |

### Code quality & tests (CQ)

| ID | Finding | Decision |
|----|---------|----------|
| CQ-m1 | Two stacked `## Unreleased` headers in CHANGELOG.md (this PR + the still-Unreleased issue #64 entry on main) | **A** (light touch) — leave as two sections; each represents distinct in-flight work. Note in the round-2 commit message that they consolidate at release time. The reviewer flagged this as unusual, but the existing convention is single-Unreleased; the two-stack here is a genuinely temporary state. |
| CQ-m2 | The CHANGELOG "Process" subsection is meta-commentary that belongs in `docs/issue-71/review-round-1.md`, not the public changelog | **A** — drop the "Process" section. The plan and review docs are linked from the PR body. |
| CQ-m3 | `injectStatementTimeout` doc comment is 17 lines, longer than the 25-line function. The "Do not issue `SET statement_timeout` without `LOCAL`" guidance is global, not function-local. | **A** — trim to the function-local rationale (regex anchor, operator override, return-original-on-parse-error). Move the global SET-LOCAL warning to a package-level comment. |
| CQ-m4 | `clampOperationName` comment says "rejects control characters" but accepts non-ASCII printables and Unicode line separators (U+2028, U+2029) — Sec-m1 mirrored this. | **A** — also reject U+2028 and U+2029 (some log aggregators split lines on these); update comment to be precise about the charset. |
| CQ-m8 | AGENTS.md placement: the new bullets are under "Items deliberately deferred" but describe **active live policy**, not deferred work. | **A** — move both bullets to a more appropriate location (a new "Conventions" subsection within the existing structure, or alongside other live posture items). |

## Minor / nice-to-have — folded or deferred

Accepted as small belt-and-braces fixes:
- Log a `slog.Warn` when `injectStatementTimeout` swallows a parse error (CQ-m7) — useful operator breadcrumb.
- Truncate the `options` value in the operator-override log line (Sec-m3) — defensive against operators who shove unexpected content into `options`.
- Update comment on `Path: nil` to explain the choice (PF-MIN-1) — one-line clarification.

Deferred (not in this PR):
- Functional / property tests on `injectStatementTimeout` (CQ-N1) — current 8 cases are enough.
- Splitting `injectStatementTimeout` into smaller functions (CQ-N1) — current shape is readable.
- Code dedup between timeout/non-timeout response paths in handler (CQ-N2) — current explicit shape is clearer than a shared helper.
- Validate test boundary case `statementTimeoutMs=0 with malformed URL` (CQ-N4) — covered by the existing parse-error branch.
- Multiple-value-same-key query-param test (CQ-m6) — Go's `url.Values` semantics are standard; not worth a dedicated test.

Deferred (rejected for now):
- Stricter regex for `-c statement_timeout = 10` (whitespace around `=`) — Postgres rejects this form anyway, so it's defence-in-depth without measurable value.
- Always return a fresh `*graphql.Result` from `timeoutResponse` (CQ-m5) — current aliasing is well-tested and faster; the cosmetic concern doesn't justify the allocation.

---

## Apply order

Single follow-up commit on `feat/statement-timeout`:

1. **`fix(graphql): round-2 review fixes for query budgets`** — bundles:
   - Security M1: broaden regex + three test cases
   - Security M2: Validate ceiling + test
   - Cross-cutting M1: integration test file
   - Cross-cutting M2: metric test + counterDelta in handler test
   - Cross-cutting M3: chi-stack e2e test
   - CQ-m2: drop CHANGELOG Process section
   - CQ-m3: trim executor comment, move SET-LOCAL warning to package doc
   - CQ-m4: reject U+2028/U+2029 in clampOperationName
   - CQ-m8: move AGENTS.md bullets to a non-deferred section
   - PF-MIN-1, Sec-m3, CQ-m7: small belt-and-braces

## Round 3?

**No.** Round 2 is mostly about test coverage delivery and one regex
correctness fix. None of the Majors changes the design. Once these
land, the PR is ready for Draft → CI → operator merge.

## Follow-up issues to file before PR-ready

(Already filed during #71's plan phase; nothing new to file here.)
