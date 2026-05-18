# 04 — Mini-review 1 (after M1–M5)

Five commits landed since the diagnostic docs:

```
5a9bbc9 chore: delete three small dead helpers                 [M1]
9d70ae8 chore(oauth): delete errors.go — 311 LOC               [M2]
8676590 chore(oauth): collapse scopes.go to ParseScopes only   [M3]
235bfa1 chore(oauth): delete dead Generate*/AuthMiddleware     [M4]
4687e70 fix(workers): restore activity cleanup worker          [M5 + T-1]
```

Plus the prior diagnostic doc commit (5ecb4a3).

Net source delta (excluding docs): **~30 files, ~1100 LOC out, ~135 LOC in.**

## Did any of these commits introduce a new problem?

No. M1–M4 are pure deletions of code with zero callers in the
codebase; the build + vet + lint + tests are all clean after
each commit individually. M5 is a 2-line behaviour fix
(`defer ticker.Stop()` moved inside the goroutine) plus a
testability seam (private interface + better test).

The one nuance worth flagging: M5 introduced a new abstraction
(the private `activityCleaner` interface) which the directive
generally discourages. Justification (recorded in the commit
body): the previous test was a concretely-broken tautology and
the smallest possible fix to make production code testable
required a 2-method interface. The interface is package-private
and the change adds three lines to the source.

## Did any commit regress a test?

No. After each commit:
- `go build ./...` clean.
- `go vet ./...` clean.
- `golangci-lint run ./...` returns `0 issues.`
- `go test -race -short` passes for every package that was
  touched (oauth, workers, lexicon, database).

The known-flake notifications tests + the
TestPurgeTokenSigner/TestMigrations_Rollback shared-state
flakes are unrelated and not within scope tonight.

## Did any commit contradict an earlier fix?

No. The five commits are independent. The OAuth dead-code
deletions (M2–M4) do not interact with the worker fix (M5).

## Atomic per scope ceiling?

Each commit is well under the 400-line / 8-file ceiling. M4
is the biggest at ~330 LOC across 4 files; the rest are
under 250 LOC each.

## Updates to next steps

- M6 (S-1 SQL injection) next — small validator + tests.
- M7 (jetstream cursor-flusher leak) is the next medium-risk
  item.
- M8 (WebSocket Stop race) is the highest-risk MUST-FIX —
  schedule it after M6/M7 build confidence.
- M9 (totalCount filter-aware) needs care because it changes
  observable behaviour (filtered totalCount values become
  smaller) — flag in PR body.

No re-triage of WILL FIX or WILL NOT FIX needed.
