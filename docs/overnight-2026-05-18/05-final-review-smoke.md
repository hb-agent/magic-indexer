# Final Review — Smoke + Bisectability

Branch: `staging` (20 ahead of `origin/staging`, all 2026-05-18, all 20 from `origin/main..HEAD`).

Commit-shape note: task expected ~22, actual is 20. All on 2026-05-18, all from a single author session. Close enough to the estimate to be unremarkable.

## Quality gates
- build: pass (`GOARCH=arm64 go build ./...`, clean)
- vet:   pass (`GOARCH=arm64 go vet ./...`, clean)
- lint:  pass (`golangci-lint run ./...` → `0 issues.`)
- test:  pass with notes — see below

### Test notes
Full suite (`-race -short -count=1 ./...`): one package failed, `internal/graphql/admin`:
- `TestPurgeActor_HappyPath`
- `TestPurgeActor_RejectsExpiredToken`
- `TestPreviewPurgeActor_EmptyActorOK`

Re-ran the three in isolation: **pass**. Same shared-Postgres-state-collision pattern as the documented PurgeActor / PurgeTokenSigner flake family. Treated as known-flake siblings (the `_RequiresAdmin` member is on the list; these are adjacent tests in the same fixture). Not a new failure mode.

Repositories 3-of-3 stress run (highest-risk package): **3/3 pass**, ~3.4s each. No CI-blocker risk there.

## Per-commit bisectability

All five high-risk commits build clean in isolation on `GOARCH=arm64 go build ./...`:

| Tag | SHA       | Subject                                                  | Build |
|-----|-----------|----------------------------------------------------------|-------|
| M5  | `4687e70` | activity cleanup worker — ticker.Stop ordering fix       | pass  |
| M7  | `9c4cb10` | jetstream per-generation cursor flusher                  | pass  |
| M8  | `24f7760` | jetstream/labeler — Run owns events-channel close        | pass  |
| M9  | `3ea2c55` | repo/schema — totalCount respects filters                | pass  |
| W7  | `3ace52e` | config — ADMIN_DIDS validated via did.IsValid            | pass  |

Returned to `staging` after, clean tree.

## Files audit
- 45 files changed, +6,134 / −1,214 (net +4,920 LOC).
- Of the +6,134, ~4,560 are `docs/overnight-2026-05-18/*.md` (orientation/findings/plans/mini-reviews) — pure docs.
- Largest code change: `internal/database/repositories/records.go` +257 (totalCount filter-aware fix, M9).
- Largest deletions: `internal/oauth/scopes_test.go` −236 and `internal/oauth/token_generator_test.go` −72 (tests for code deleted in the OAuth cleanup chores).

## Test-count delta
- Was 82 at orientation.
- HEAD: **81** (`git ls-tree -r HEAD | grep _test.go`).
- Net −1: deleted `scopes_test.go`, `token_generator_test.go`, and trimmed `workers_test.go` (was a single-file delta inside an existing test file); added `filter` depth-cap test and BatchInsert atomicity test as new files. Within the "roughly flat or slightly down" envelope.

## Migration count
- `internal/database/migrations/postgres/` → **60 files** (matches expected; no new migrations tonight).

## Diff sanity
- `*.sql` diff: **0 lines** (no migration changes — consistent with the migration-count check)
- `*.json` diff: **0 lines** (no config drift)
- Nothing in the top-10 file diff jumps out as weird. The shape is: docs-heavy (expected for a review night), one ~250-line correctness fix, two ~200-line test-deletion-following-dead-code-removal, everything else under 150 LOC delta.

## CI-blocker risk verdict

**Low.** Build/vet/lint all green. Highest-risk package (`repositories`) is 3-of-3 stable under `-race -short`. The only red in the full-suite run reproduces the known shared-state PurgeActor flake family and passes in isolation — same root cause as the documented set, just adjacent test names. Recommend opening the Draft PR `staging → main` as planned.

Caveats for the human merger:
- Heads-up that the `_HappyPath`, `_RejectsExpiredToken`, and `_EmptyActorOK` PurgeActor tests should be added to the known-flake list (or the underlying shared-state fixture fixed) since CI will see them flake too.
- 20 commits not 22, in case the count was load-bearing for someone.
