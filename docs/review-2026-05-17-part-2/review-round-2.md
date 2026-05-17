# Implementation-review round 2 — review-2026-05-17 part 2

Per AGENTS.md §"deep flow" step 6. Two parallel reviewers ran
after the 21-commit Track-7 series landed on staging:

- **IR1 — Functional correctness** (dynamic-offset arithmetic,
  duplicate `$8`, zero-count edge cases, password redaction,
  migrations simplification, postgres executor surface, log-line
  changes).
- **IR2 — Smoke test** (quality gates, leftover references,
  migration files, test deletions, per-commit bisectability).

Both returned **green — ship it**. No bugs, no follow-up commit
required. Round 3 not warranted.

---

## Decisions

### IR1 — Functional correctness

| # | Finding | Decision | Rationale |
|---|---------|----------|-----------|
| IR1.1 | Dynamic-offset arithmetic across `actors.go:96-97`, `records.go:222-224`, `records.go:GetByCollectionFiltered ph()`, `records.go:GetByURIs/CIDsByURIs/ExistingCIDs/CollectionStatsFiltered`, `labels.go:GetByURIs/HasTakedown/GetTakedownURIs/GetPaginated`, `reports.go:GetPaginated` — all numeric arithmetic preserves the original Placeholder() output | **NO ACTION** | Hand-verified per site; no off-by-one possible. |
| IR1.2 | Intentional duplicate `$8` in `jetstream_activity.go:112,121` survived the inline. Explanatory comment at `:105-108` still warns future eyes | **NO ACTION** | Code comment is doing its job. |
| IR1.3 | All 5 `Placeholders(count, startIndex)` inlining sites are guarded by `if len(...) == 0 { return }` at the entry point — the previous `Placeholders` returned `""` for `count <= 0`, but that path is dead code at every caller | **NO ACTION** | Zero-count behaviour preserved. |
| IR1.4 | `internal/server/database.go` rejection error uses `config.RedactPassword(databaseURL)`; success-path slog also uses redacted; `isPostgresURL` correctly accepts both `postgres://` and `postgresql://` case-insensitively | **NO ACTION** | R1.2 fix landed correctly. |
| IR1.5 | `migrations.go` simplification — `loadMigrations()` is parameter-less, `Run()`/`Rollback()` no longer call `exec.Dialect()`, schema_migrations queries use literal `$1`, intermediate `fs` variable removed | **NO ACTION** | All 5 sub-criteria pass. |
| IR1.6 | `postgres/executor.go` surface — three removed methods, `fmt` import dropped because no other `fmt.` calls remain (error constructors go through `database.*Error` helpers) | **NO ACTION** | Clean. |
| IR1.7 | `cmd/hypergoat/main.go:266` dropped `"dialect": "postgresql"` field. Information not lost — `server/database.go:34` already emits "Connecting to Postgres" + redacted URL + statement_timeout_ms at the same startup phase | **NO ACTION** | The dropped field carried constant-valued (and now obvious) info. |
| IR1.8 | Reviewer flagged a "nit" — wanted explicit grep confirmation that `TestDialectString`/`TestParseDialect`/`TestExecutor_Dialect`/`TestExecutor_Placeholder`/`TestExecutor_Placeholders` were dropped | **CONFIRMED BY IR2** | IR2 ran the grep and confirmed only the five named tests are missing; everything else stays. |

### IR2 — Smoke test

| # | Finding | Decision |
|---|---------|----------|
| IR2.1 | All quality gates clean: build, vet, lint (0 issues), repository tests, migration tests | **NO ACTION** |
| IR2.2 | Leftover-reference grep returns 5 hits — all comments / variable names with the *word* `Placeholders` / `Dialect`, no actual `r.db.Placeholder(...)` calls or type references | **NO ACTION** | Plan acceptance criterion #2 met. |
| IR2.3 | `internal/database/migrations/sqlite/` deleted; full-tree `find` confirms no lingering sqlite paths | **NO ACTION** |
| IR2.4 | 56 postgres migration files intact (28 up/down pairs incl. 027 and 028 from part 1) | **NO ACTION** |
| IR2.5 | `executor.go` shrunk to 178 lines (plan estimated ~180), down from 208; `postgres/executor.go` at 243 lines (the plan didn't explicitly target a number for it — most of its body is the `injectStatementTimeout` regex machinery which is unchanged) | **NO ACTION** | Both files build standalone. |
| IR2.6 | Test deletions correct — only the 5 named dialect/placeholder tests are missing; all 7 other tests in `executor_test.go` and `TestConvertParams` + 11 `TestInjectStatementTimeout_*` cases in `postgres/executor_test.go` stay | **NO ACTION** |
| IR2.7 | Migration tests still pass after the `loadMigrations()` signature change — verbose run reaches migrations 027/028 apply+rollback cleanly | **NO ACTION** |
| IR2.8 | Per-commit bisect spot-check: commits `d6c08a1` (7.1), `28f2594` (7.19), `66cc239` (7.0') all built clean in isolation | **NO ACTION** | Bisectability claim holds. |

## No follow-up commit needed

Unlike part 1 (where IR1.A revealed a missing test), part 2's
implementation matched the plan exactly. Both reviewers concur:
ship it.

## Recommendation

Update PR #83's title and body to reflect the combined scope
(per the operator's coordination decision to land both parts in
the same PR), then wait for CI green.
