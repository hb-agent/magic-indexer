# 04 — Mini-review 2 (after M6–M9)

Four more commits since mini-review 1:

```
78ef4f1 fix(repo): validate collection name before raw-interpolating into DDL   [M6 / S-1]
9c4cb10 fix(jetstream): per-generation cursor flusher                            [M7 / C-2]
24f7760 fix(jetstream,labeler): Run owns events-channel close                    [M8 / C-3]
3ea2c55 fix(repo,schema): totalCount respects filters                            [M9 / P-1 + T-8]
```

All 9 MUST FIX items from `03-implementation-plan.md` are now
shipped.

## Did any of these commits introduce a new problem?

No. Each commit was followed by:
- `go build ./...` clean.
- `go vet ./...` clean.
- `golangci-lint run ./...` returns `0 issues.`
- Affected-package `go test -race -short -count=1` passes.

The pre-existing known-flake set (admin's
`purge_resolver_test.go` migration deadlock when run inside the
full parallel suite, notifications shared-state collisions) was
encountered once during M9 verification but reproduced as
expected when isolated — unrelated to this PR's surface.

One observable behaviour change introduced:

**M9 — totalCount on filtered queries.** Clients that previously
read `totalCount` on a connection with `where` / `authors` /
`labels` / `search` / `excludePds` will see a SMALLER value
after this lands. That is the CORRECT value (now matches the
result set); the prior value was the unfiltered collection
size and made pagination UIs unusable on filtered queries.
This is flagged explicitly for the PR body.

## Did any commit regress a test?

No. All tests pass after each commit. The new tests added by
M5, M6, M9 (worker-ticker + collection-name validator +
filtered-count regressions) each fail if the corresponding fix
is reverted — verified mentally during write-up.

## Did any commit contradict an earlier fix?

No. The 9 commits are independent except for two intentional
coupling points:

- M7 + M8 both touch the Jetstream consumer's connection-
  generation lifecycle. M7 (cursor flusher per-gen ctx)
  composes with M8 (Run owns events close) because both
  treat the per-connection cleanup as "Run's deferred
  responsibility" rather than "Stop's eager responsibility."
  The combined shape is more coherent than either alone.

- M9 introduces `validateCollectionName` skip in
  `CountByCollectionFiltered`. M6 added that validator
  specifically for the DDL paths. The skip is intentional and
  documented in the code comment + commit body — the
  resolver-layer collection arg comes from the lexicon
  registry, not from request data.

## Atomic per scope ceiling?

- M6: 2 files, ~120 LOC. Well under.
- M7: 1 file, ~25 LOC. Well under.
- M8: 2 files, ~50 LOC. Well under.
- M9: 3 files, ~300 LOC (the bulk is the new
  `buildFilteredWhereForCount` helper, which mirrors the
  existing WHERE-builder). Approaches the 400-line ceiling
  but stays within it; the change is coherent (one
  correctness fix) so splitting wouldn't improve clarity.

## Next steps

Move into WILL FIX IF TIME PERMITS. The list per
`03-implementation-plan.md` §"WILL FIX IF TIME PERMITS":

```
W1  S-4   loginHint/did → logsafe.String                             S
W2  R-1   Delete BuildFieldFilterClause (dead)                       S
W3  R-3   Delete GetByCollection / GetByCollectionWithCursor         M
W4  R-6   Collapse GetCIDsByURIs / GetExistingCIDs                   S
W5  O-1   RUNBOOK INVALID-CONCURRENTLY recovery section              S
W6  P-5   awardCount description: drop "sub-millisecond" claim       S
W7  S-5   Validate ADMIN_DIDS via did.IsValid at startup             S
W8  S-3   ALLOWED_ORIGINS unset = startup error when https           S
W9  R-2   Collapse buildBadgeAwardSubjectFilter / buildStringSubject M
W10 R-5   Extract TextINClause helper for IN-clause loops            S
W11 T-3   BatchInsert partial-failure test                           S
W12 T-4   Direct depth-cap test on buildFilterGroupRecursive         S
```

Order by ascending wall-clock cost: W2 → W6 → W11 → W12 → W10
→ W5 → W7 → W4 → W3 → W9 → W8 → W1.

Will pull from this list until budget closes. Each commit
remains atomic; mini-review 3 after another 4-5.
