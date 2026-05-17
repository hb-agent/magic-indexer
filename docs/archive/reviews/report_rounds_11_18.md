# Review Report — Additional Rounds 11–18 (Fixed-Roster Cycle)

**Repo**: `magic-index` (fork of hypercerts-org/hyperindex)
**Branch**: `per-labeler-definitions` (PR #3)
**Base for this cycle**: commit `6d5e149` (end of Round 10 in the first report)
**Head after this cycle**: commit `d8edda8`
**Baseline**: already through 10 prior review rounds; build / vet / test / lint green.

Per the user's follow-up instruction, rounds 11–18 ran with a **fixed 20-perspective
roster that stays consistent across all 8 rounds** (fresh agent context each round,
same lenses). The roster is in `/tmp/fixed_roster.md` — it covers the entire codebase:
labeler ingest, Jetstream ingest, records repo, labels repo, label definitions,
migrations, DB layer, public GraphQL, admin GraphQL, OAuth flow, OAuth middleware
+ DPoP, DID resolution, HTTP middleware, WebSocket subscriptions, cmd/hypergoat
lifecycle, config, lexicon registry, workers + backfill, observability, and
build / deploy / deps.

Every round was driven by the same loop: dispatch the 20 reviewers, verify each
finding by reading the code (false positives filed but not fixed), apply the
legitimate fixes, re-run `go build / vet / test / lint`, commit and push.

## Round summary table

| Round | Critical | Major | Minor | Nice | Fixed | Commit |
|-------|---------|-------|-------|------|-------|--------|
| 11 | 0 | 0 | 0 | 0 | 0 | clean — all flagged items were false positives |
| 12 | 0 | 0 | 0 | 0 | 0 | clean |
| 13 | 0 | 0 | 0 | 0 | 0 | clean ("harder" pass, still nothing) |
| 14 | **2** | **1** | 0 | 0 | **3** | `d8edda8` — jetstream state races |
| 15 | 0 | 0 | 0 | 0 | 0 | clean |
| 16 | 0 | 0 | 0 | 0 | 0 | clean (all flags were false positives) |
| 17 | 0 | 0 | 0 | 0 | 0 | clean (regression sweep across all recent commits) |
| 18 | 0 | 0 | 0 | 0 | 0 | clean (final pass) |
| **total** | **2** | **1** | 0 | 0 | **3** | — |

## Round 14 — the one round that found real bugs

After rounds 11, 12, and 13 all came back clean, Round 14 was instructed to read
the code with extra skepticism. It caught three genuine concurrency holes that
had slipped through every prior round (including the earlier adversarial rounds):

**Jetstream consumer state races** (`internal/jetstream/consumer.go`)

1. **cursorDone pointer swap without the lock.** `Start()`'s reconnect loop
   reset `c.cursorDone` (line 141) without holding `clientMu`. `Stop()` reads
   and closes the same channel *under* `clientMu`. A concurrent `Stop` during a
   reconnect could race the pointer swap against an old `cursorFlusher`'s
   select, hitting a data race that the Go race detector would fire on.

2. **config.Collections written without the lock.** `UpdateCollections()` wrote
   `c.config.Collections = collections` outside `clientMu`, while
   `startInternal` reads the same field *inside* `clientMu` to construct the
   next client. That is a straight data race on a struct field.

3. **cursorDone swap + config write in `UpdateCollections`** was also outside
   the lock. Same race shape.

**Fix** (commit `d8edda8`): take `clientMu` around every write to `c.cursorDone`
and `c.config` in both `Start()` and `UpdateCollections()`. The lock is released
before any blocking operation (`Connect`, `client.Stop`) so no new deadlock risk.
`go test -race ./internal/jetstream/... ./internal/labeler/...` passes green.

These are the kind of bugs that only hit once in production on a reconnect that
races a shutdown, which is exactly why eight rounds of searching were worth the
effort.

## Rounds 11, 12, 13, 15, 16, 17, 18 — clean

Every other round returned either "clean" across all 20 perspectives, or
findings that were false positives on verification. Examples of flagged items
that did *not* warrant a code change:

- **Round 11**: jetstream cursor reinit at reconnect (cursor *is* loaded fresh
  in `startInternal` via `loadCursor`), `backgroundServices.Stop()` double-stop
  race (slice is snapshotted and nil'd under the lock), WebSocket GET-only
  enforcement (gorilla/websocket already rejects non-GET per spec), ADMIN_DIDS
  log leak (already logged as a `_set` bool, not the value).
- **Round 15**: claimed `c.ctx` could be nil on `backfillIfNeeded()` — the
  reviewer themselves noted it is "likely safe in practice" because `Start()`
  initializes `c.ctx` before calling `backfillIfNeeded()` and is guarded by
  `startOnce`. Stylistic fragility, not a bug.
- **Round 16**: claimed public GraphQL has no query-size clamp — but
  `internal/graphql/query/connection.go:ClampPageSize` enforces `MaxPageSize =
  100` on every connection arg. Also claimed a DPoP JTI cleanup is missing —
  already wired in `OAuthHandlers.StartCleanupWorker` at oauth_handlers.go:1100.
- **Round 17**: did a targeted regression sweep across every recent commit
  (`b5d08ef` through `d8edda8`) and confirmed each fix maintains the
  invariants the neighbouring code depends on.
- **Round 18**: final pass, hard look, nothing new.

## Final verification

```
go build ./...       — green
go vet ./...         — green
go test ./...        — green
go test -race ./...  — green (specifically caught the jetstream races had they
                       not been fixed by d8edda8)
golangci-lint run    — 0 issues
```

## Combined totals across all 18 rounds

- **18 rounds** × **20 reviewers each** = 360 reviewer-passes.
- **35 critical** + **100 major** + **95 minor** + **19 nice-to-have** findings
  reported, many overlapping or false-positive on verification.
- **58 legitimate fixes landed across 9 commits** (`b5d08ef`, `80a9c4e`,
  `68c526e`, `322ad09`, `a7aae92`, `f9e8c11`, `6d5e149`, `d8edda8`; plus the
  three Round 7 regression tests bundled with their fixes).
- **3 regression tests added** that pin the recent security-critical fixes.
- **Rounds 9, 10, 11, 12, 13, 15, 16, 17, 18** all returned clean — i.e. 9 of
  the final 10 rounds found no actionable issues, with Round 14 being the only
  exception and its findings already addressed.

## Ship readiness

After 18 rounds (10 varied-lens + 8 fixed-roster), the branch is green on
build, vet, test, `-race` test, and lint. The user's stated bar — "tomorrow I
want to implement it without any problems on the code base" — has been met to
the extent a code review can. The single real bug caught in the fixed-roster
cycle (Round 14 jetstream state races) is the kind of defect that typically
only surfaces under production load; catching and fixing it before deploy is
exactly what the extra rounds were for.

## Files

- `/tmp/review_log.md` — per-round log for rounds 1–10
- `/tmp/review_log_extra.md` — per-round log for rounds 11–18
- `/tmp/fixed_roster.md` — the 20-perspective roster reused across rounds 11–18
- `/tmp/review_report_rounds_1_10.md` — first report (first 10 rounds)
- this file — second report (additional 8 rounds)
