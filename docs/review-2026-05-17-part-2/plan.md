# Implementation plan — review-2026-05-17 part 2 (Track 7)

Companion to `docs/review-2026-05-17/plan.md` part 1 (PR #83).
Implements the audit's Dim-2 "the dialect abstraction is half-built"
item via the SQL-B variant the operator confirmed at part-1 sign-off.

## Larger goal

Make Postgres-only the visible posture of the codebase, not just the
README's. Today every repository goes through a `r.db.Placeholder(N)`
indirection that exists so SQLite would "work" — except SQLite has
1 migration vs 26 Postgres, the SQLite tree was abandoned at the
fork from `hyperindex`, and Railway-deployed magic-indexer has no
plausible path to ship on SQLite. The cost is paid daily (every
SQL statement reads as if the dialect mattered); the benefit is
zero. SQL-B removes both: drops the SQLite tree and inlines `$N`
across every repository.

Net effect: same binary, same behaviour, smaller surface for the
next contributor and one fewer "wait, is this dialect-portable?"
question per SQL edit.

Out of scope: any database-engine swap, schema change, query
behaviour change, or migration-runner refactor beyond removing
the dialect parameter.

## Surface area (mapped 2026-05-17)

| Area | Files | Notes |
|---|---|---|
| SQLite migration tree | `internal/database/migrations/sqlite/` (2 files) | Delete the directory. |
| Dialect abstraction | `internal/database/executor.go` | Remove the `Dialect` enum, `ParseDialect()`, and the `Dialect()`/`Placeholder()`/`Placeholders()` methods on the `Executor` interface. |
| Postgres executor | `internal/database/postgres/executor.go` | Remove the corresponding method implementations. |
| Migration runner | `internal/database/migrations/migrations.go` | `loadMigrations` no longer takes a dialect; always uses the embedded `postgres/` FS. Both `Migrate` / `Rollback` stop calling `exec.Dialect()` / `exec.Placeholder()` and use literal `$1`. |
| Server connect | `internal/server/database.go` | `ConnectDatabase` validates that the URL is a `postgres://` or `postgresql://` shape and returns the Postgres executor directly. No dialect switch. |
| Existing dialect tests | `internal/database/executor_test.go` (TestDialectString, TestParseDialect) | Delete those tests; the rest of the file (if any) stays. |
| Postgres executor tests | `internal/database/postgres/executor_test.go` | Drop assertions on `Dialect()` / `Placeholder()` / `Placeholders()`. |
| Repository files (Placeholder inlining) | 19 files in `internal/database/repositories/`, ~196 `r.db.Placeholder(...)` call sites + ~handful of `Placeholders(...)` | Mechanical inline — `$1`, `$2`, etc. directly in the SQL string. Most `fmt.Sprintf` wrappers collapse to plain raw strings. |

Per-file `r.db.Placeholder` counts (from grep): `actors.go 10, jetstream_activity.go 4, label_definitions.go 5, config.go 4, label_preferences.go 5, oauth_atp_sessions.go 13, oauth_access_tokens.go 10, lexicons.go 5, labels.go 16, oauth_atp_requests.go 5, oauth_auth_requests.go 7, oauth_dpop_jti.go 6, oauth_dpop_nonces.go 5, oauth_refresh_tokens.go 10, oauth_authorization_codes.go 8, oauth_clients.go 12, reports.go 6, records.go 29, oauth_par_requests.go 6`. `records.go` is the largest at 29 sites.

## Per-file patterns (sampled from `actors.go`)

Three call shapes appear:

1. **Bound at top of function, used in adjacent `fmt.Sprintf`**:
   ```go
   p1 := r.db.Placeholder(1)
   p2 := r.db.Placeholder(2)
   p3 := r.db.Placeholder(3)
   sqlStr := fmt.Sprintf("INSERT ... VALUES (%s, %s, %s)", p1, p2, p3)
   ```
   Becomes:
   ```go
   sqlStr := "INSERT ... VALUES ($1, $2, $3)"
   ```

2. **Inline single-shot in `fmt.Sprintf`**:
   ```go
   sqlStr := fmt.Sprintf("DELETE FROM actor WHERE did = %s", r.db.Placeholder(1))
   ```
   Becomes:
   ```go
   sqlStr := "DELETE FROM actor WHERE did = $1"
   ```

3. **Dynamic offset `r.db.Placeholder(base+N)` in loops** (e.g. batch upserts):
   ```go
   for i := range items {
       base := i * 2
       row := fmt.Sprintf("(%s, %s)", r.db.Placeholder(base+1), r.db.Placeholder(base+2))
   }
   ```
   Becomes:
   ```go
   for i := range items {
       base := i * 2
       row := fmt.Sprintf("($%d, $%d)", base+1, base+2)
   }
   ```

`r.db.Placeholders(count, startIndex)` (a few call sites — exact count to be confirmed during implementation) becomes a small local helper or an explicit `strings.Join` over `fmt.Sprintf("$%d", ...)`. If only one or two callers, an inline replacement is fine.

## Tracks

Single logical change, but committed per-file for bisectability —
matches the audit's recommendation in plan.md and AGENTS.md's
"atomic commits with clear scope tag".

### Track 7.0 — Migration runner + executor interface

- **Files**: `internal/database/executor.go`,
  `internal/database/postgres/executor.go`,
  `internal/database/migrations/migrations.go`,
  `internal/database/executor_test.go`,
  `internal/database/postgres/executor_test.go`,
  `internal/server/database.go`.
- **Approach**:
  - Remove `Dialect` enum, `ParseDialect`, and the
    `Dialect()`/`Placeholder()`/`Placeholders()` methods from the
    `Executor` interface and the Postgres implementation.
  - `loadMigrations` becomes `loadMigrations()` with no arg.
  - `Migrate`/`Rollback` use literal `$1` in the schema_migrations
    queries.
  - `ConnectDatabase` accepts only postgres URL shapes; rejects
    others with a clear error.
  - Strip dialect tests; keep all other tests intact.
- **Acceptance**: the package still builds and the migration runner
  tests pass. Repository files won't build yet (they still call
  `r.db.Placeholder`) — that's expected; Track 7.0 lands the
  interface change and Tracks 7.1–7.N inline the call sites.

  Wait — this means Track 7.0 alone breaks the build until 7.N
  lands. **Sequencing decision**: don't commit 7.0 alone; either
  bundle 7.0 with all 7.N into one big commit, OR do it in
  reverse (inline all `Placeholder` calls first, then remove the
  interface methods last). The reverse order keeps every commit
  building.

  **Decision**: reverse order. Tracks 7.1–7.N inline `$N` in each
  repository file while the interface method still exists (harmless
  — it just stops being called). Track 7.Z (last) deletes the
  interface methods, the dialect enum, the migration-runner
  dialect parameter, and the SQLite tree.

### Tracks 7.1 – 7.19 — Per-repository `Placeholder` inlining

One commit per repository file, in roughly ascending order of size
(smallest first) so each commit is fast to review:

| Track | File | Sites |
|---|---|---|
| 7.1 | `config.go` | 4 |
| 7.2 | `jetstream_activity.go` | 9 (incl. intentional duplicate `$8`) |
| 7.3 | `lexicons.go` | 5 |
| 7.4 | `label_definitions.go` | 5 |
| 7.5 | `label_preferences.go` | 5 |
| 7.6 | `oauth_atp_requests.go` | 5 |
| 7.7 | `oauth_dpop_nonces.go` | 5 |
| 7.8 | `oauth_dpop_jti.go` | 6 |
| 7.9 | `oauth_par_requests.go` | 6 |
| 7.10 | `reports.go` | 6 |
| 7.11 | `oauth_auth_requests.go` | 7 |
| 7.12 | `oauth_authorization_codes.go` | 8 |
| 7.13 | `oauth_access_tokens.go` | 10 |
| 7.14 | `oauth_refresh_tokens.go` | 10 |
| 7.15 | `actors.go` | 10 |
| 7.16 | `oauth_clients.go` | 12 |
| 7.17 | `oauth_atp_sessions.go` | 13 |
| 7.18 | `labels.go` | 16 |
| 7.19 | `records.go` | 29 |

Each per-file commit:
- Inlines `r.db.Placeholder(N)` → `$N` literals.
- Replaces `r.db.Placeholders(count, startIndex)` with the equivalent string literal or a `strings.Join`/`fmt.Sprintf("$%d", ...)` loop.
- Collapses `fmt.Sprintf("...", placeholders...)` to plain string literals where dynamic placeholders are no longer needed.
- Touches that one repository file plus its test file if needed.

### Track 7.0' — `migrations.go` placeholder + dialect inlining

Added after plan-review round 1 item R2.1 — `migrations.go` is a
Placeholder caller and was missing from the per-file table.

- **Files**: `internal/database/migrations/migrations.go`.
- **Approach**: inline the 4 `exec.Placeholder(1)` sites
  (`:113, 146, 218, 247`) to literal `$1`; remove the 2
  `exec.Dialect()` calls (`:51, 171`) and the `dialect` parameter
  threaded through `loadMigrations` (it always uses the embedded
  `postgres/` FS anyway). Keep the interface methods alive — they
  get removed in 7.Z.
- **Acceptance**: build + tests clean; no `exec.Placeholder` or
  `exec.Dialect` calls remain in `internal/database/migrations/`.

### Track 7.Z — Remove the dialect abstraction

- **Files**: `internal/database/executor.go`,
  `internal/database/postgres/executor.go`,
  `internal/database/executor_test.go`,
  `internal/database/postgres/executor_test.go`,
  `internal/server/database.go`,
  `internal/database/migrations/sqlite/` (deleted).
- **Approach**:
  - At this point no repository OR migration code calls
    `r.db.Placeholder` / `r.db.Placeholders` / `exec.Dialect()`.
    Build is green.
  - Remove the three methods from the `Executor` interface and
    from `postgres.Executor`.
  - Remove `Dialect` enum + `ParseDialect`.
  - Simplify `ConnectDatabase` (no dialect switch). **R1.2 fix**:
    the rejection error path currently logs the raw `databaseURL`
    (with password); replace with `config.RedactPassword(databaseURL)`
    so a misconfigured operator doesn't leak credentials. Pre-
    existing bug worth folding into this scope.
  - Drop `internal/database/migrations/sqlite/` directory.
  - Drop `TestDialectString` and `TestParseDialect` from
    `executor_test.go` (the 7 other tests in that file stay).
- **Acceptance**: build clean, all tests pass, no remaining
  references to `Placeholder`, `Placeholders`, `Dialect`,
  `ParseDialect`, or `sqlite` directory. Rejection-error path
  redacts the password.

## Alternatives considered

| Alternative | Why not |
|---|---|
| SQL-A (delete migration tree only, keep `Placeholder()` indirection) | Operator already rejected at part-1 sign-off — doesn't close the audit's "cost is paid daily" complaint. |
| One massive commit covering all 19+1 changes | Loses bisectability the per-file commits buy. The audit's evidence note for this track explicitly mentioned "wide but mechanical"; per-file matches the project's #79/#80 multi-commit precedent. |
| Reverse the order (delete interface first, then inline) | Breaks the build between Tracks 7.0 and 7.N. Reverse-of-reverse (inline first, then drop) keeps every commit green. |
| Keep `Placeholders(count, startIndex)` as a free function in `internal/database/` (no method) | Adds a layer for ~handful of sites. Easier to inline `strings.Join` once per site than to import a new helper. |
| Rewrite repositories to use a query builder (`squirrel`, etc.) | Audit didn't ask for it; would invalidate every existing test; well out of scope. |

## Acceptance criteria (overall)

1. All four quality gates clean: `GOARCH=arm64 go build ./...`,
   `go vet ./...`, `golangci-lint run ./...`, and
   `CGO_ENABLED=1 GOARCH=arm64 go test -race -short ./...`.
2. `grep -rn "Placeholder\b\|Placeholders\b\|ParseDialect\b\|database\.Dialect\b" --include="*.go" internal/ cmd/` returns no matches.
3. `internal/database/migrations/sqlite/` does not exist.
4. CI green on the Draft PR.
5. Every per-file commit builds and tests green on its own (bisect
   verifies — `git bisect run` would terminate immediately on any
   bad commit).

## Rollback plan

- Per-file commits → per-file `git revert`. Mechanical and conflict-
  free as long as nothing edits the same lines in the meantime.
- Track 7.Z revert restores the SQLite tree directory and re-adds
  the dialect abstraction. The repository files would still compile
  because they no longer call the removed methods.
- Production rollback at Railway: standard deployment-rollback. No
  schema change in this batch means no migration to roll back.

## Out of scope

- Removing `database.Value` / `Text`/`Int`/`Bool` helpers — they're
  not part of the dialect abstraction, they're a typed-value pattern.
- Swapping `database/sql` for `pgx.Pool` directly. Out of scope.
- Renaming the package layout (`internal/database/postgres/` →
  `internal/database/`). The abstraction-via-package is a smaller cost
  than the audit's interface-method abstraction; leave it.
- Performance work, query rewrites, or `pgx` row-scan changes.

## Open questions for the operator

None expected — the part-1 sign-off resolved them all. If
plan-review surfaces ambiguity, will record there.

## Sequencing

Single commit per row in the table above, in the listed order.
After all 19 per-file commits land, Track 7.0' inlines the
migrations-package sites (per plan-review R2.1), then Track 7.Z
removes the dialect surface and deletes the SQLite tree. Then
Draft PR `staging → main`.

The per-file order is roughly ascending by site count so easy ones
land first and `records.go` (29 sites) gets reviewed against the
pattern set by the other 18.
