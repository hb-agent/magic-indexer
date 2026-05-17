# Plan-review round 1 — review-2026-05-17 part 2

Two parallel reviewers on the mechanical refactor plan:

- **R1 — Lurking-bug**: dynamic-offset sites, edge cases,
  silent-bug hazards, mock implementers.
- **R2 — Test coverage**: per-file test gaps, bisectability,
  CI integration coverage.

Both returned substantive findings. Round 2 not warranted; the
items are concrete and actionable.

---

## Decisions

### R1 — Lurking-bug findings

| # | Finding | Decision | Rationale |
|---|---------|----------|-----------|
| R1.1 | `jetstream_activity.go:121` has an intentional **duplicate `$8`** — `source_event_id` is referenced twice in the CTE (once in INSERT, once in the SELECT-fallback). A reviewer skim might "fix" this to `$8, $9` and silently misbind. | **ACCEPT** | Call this out explicitly in the per-file commit and add a code comment at the inline site so a future eye doesn't unfix it. |
| R1.2 | `ConnectDatabase` (`internal/server/database.go:34`) currently writes the raw `databaseURL` (with password) into the rejection error message — `config.RedactPassword` is only used on the success-path slog. | **ACCEPT** | Small adjacent fix worth folding into Track 7.Z. Use `config.RedactPassword(databaseURL)` in the rejection error so a misconfigured operator doesn't paste a credential into a log. |
| R1.3 | Plan's per-file site counts are off (e.g. `jetstream_activity.go: 4` actually has 9 numbered sites; total 166, not 196). | **ACCEPT** | Re-run the grep before starting Track 7.1 and update the plan's site-count table to the real numbers. Doesn't change bisect granularity, but the plan should be accurate. |
| R1.4 | No `r.db.Placeholder` returns `$0` in any current call site — all offsets start `i+1` or guard with non-empty preconditions. | **NOTED** | No action; documented absence of risk. |
| R1.5 | No external implementers of `Executor` interface — no mocks/fakes to update. | **NOTED** | Removing the three methods is safe. |
| R1.6 | `migrations.go` uses `Placeholder(1)` only for fully operator-controlled inputs (migration version from embedded FS). No SQL-injection surface either way. | **NOTED** | Inline is trivial. |
| R1.7 | Sequencing (inline first, drop interface last) is correct; no module-init `var x = db.Placeholder(...)` caches that would break partially. | **NOTED** | Keep the plan's reverse-order approach. |
| R1.8 | `Placeholders(count, startIndex)` inlining — recommend a single package-local helper rather than 5 copies of `strings.Join + fmt.Sprintf`. | **REJECT** | 5 sites is below my abstraction threshold (3 is "is similar but not the same"; 5 is "could be a helper"; only past ~10 does the helper repay its own line count). Inline at each site instead — the pattern is one line of `strings.Join`. Decision rationale: keeping the call sites self-contained means a future SQL edit doesn't have to hunt for the helper. |

### R2 — Test coverage findings

| # | Finding | Decision | Rationale |
|---|---------|----------|-----------|
| R2.1 | **Bisectability claim doesn't fully hold for `migrations.go`** — it's a Placeholder caller (4 sites at `:113, 146, 218, 247` + 2 `exec.Dialect()` sites at `:51, 171`) but not in the per-file table. Either add a Track 7.0' for it, or 7.Z gets bigger than the plan implies. | **ACCEPT** | Add **Track 7.0'** (between 7.19 and 7.Z) that inlines the migrations-package sites. Keeps 7.Z to "delete interface methods + delete enum + delete sqlite dir + drop dialect tests" — cleaner final commit. |
| R2.2 | 11 of 19 repo files have **no dedicated `*_test.go`** (especially `oauth_clients.go` at 12 sites, `oauth_atp_sessions.go` at 13). An off-by-one inlining in those files would compile, pass `go vet`, and slip through CI. | **DEFER** | Adding happy-path tests for 11 files is its own initiative (~250 LOC of test infra per file). Folding it into a "mechanical inline" batch blows up scope. **Mitigation in this batch**: (a) per-file commit messages MUST cite the exact `$N` sequence used so review can sanity-check; (b) the implementer (me) will manually verify each inlined `$N` matches the original `Placeholder(N)` by side-by-side diff before committing; (c) `git grep "r\.db\.Placeholder" internal/database/repositories/<file>.go` must return zero after each commit. Filed as a follow-up for the operator: "consider adding round-trip tests for oauth_* repositories that lack them" — a separate ticket, not this batch's problem to solve. |
| R2.3 | Test infra is Postgres-backed (`internal/testutil/db.go:43,97-104`); tests do not skip on missing env. | **NOTED** | Exercises real SQL on every test run. |
| R2.4 | CI (`.github/workflows/ci.yml:25-30,67`) runs `postgres:16-alpine` and sets `TEST_DATABASE_URL` — covered files run real integration in CI. | **NOTED** | The covered subset has real CI safety net. |
| R2.5 | `executor_test.go` retains 7 substantive tests after dropping the 2 dialect tests (288 lines total, 9 `Test*` funcs). | **NOTED** | File stays; no extra cleanup needed. Plan was correct to scope the deletion to specific tests, not the file. |

## Net effect on the plan

Three plan edits, applied in place:

1. **Add Track 7.0'** for `internal/database/migrations/migrations.go` inlining (between 7.19 and 7.Z).
2. **Track 7.2 (jetstream_activity.go)** — note the intentional duplicate `$8` in the commit message; add code comment at the inline site.
3. **Track 7.Z** — explicit acceptance criterion: `ConnectDatabase` rejection error must use `config.RedactPassword(databaseURL)`.

Site-count table re-verification deferred until just before Track 7.1 starts (the implementer's first action).

## Items not adopted

- **R1.8 (shared `pgPlaceholders` helper)** — rejected per rationale above. 5 sites stays inline.
- **R2.2 (add tests for the 11 untested oauth_* files)** — out of scope for this batch; flagged as a follow-up for the operator.

## Recommendation

Proceed to implementation. Round 2 of plan review not warranted.
