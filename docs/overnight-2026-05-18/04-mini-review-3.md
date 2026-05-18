# 04 — Mini-review 3 (after W2, W6, W11, W12, W5, W1, W7)

Seven WILL FIX items shipped since mini-review 2:

```
ba29fc0 chore(filter): delete unused BuildFieldFilterClause                  [W2 / R-1]
eafea00 docs(schema): correct awardCount description's claim                  [W6 / P-5]
0e41071 test(repo): BatchInsert atomicity                                     [W11 / T-3]
87e30a0 test(filter): depth-cap defense-in-depth + boundary                   [W12 / T-4]
a3246b3 docs(runbook): recovering from an INVALID CONCURRENTLY index         [W5 / O-1]
7ea0e1b fix(server): scrub loginHint + DID in oauth audit logs               [W1 / S-4]
3ace52e fix(config): validate ADMIN_DIDS env-var entries via did.IsValid     [W7 / S-5]
```

## Did any of these commits introduce a new problem?

No. Each commit was preceded by either a grep (for deletion
safety) or a targeted unit test (for behavior changes), and
followed by:
- `go build ./...` clean.
- `go vet ./...` clean.
- `golangci-lint run ./...` returns `0 issues.`
- Affected-package `go test -race -short -count=1` passes.

## Did any commit regress a test?

No. New tests added by W11, W12, W7 each fail if their fix is
reverted. Existing tests untouched by all seven commits.

## Did any commit contradict an earlier fix?

No. The W-tier work is independent of itself and of the
M-tier. One coupling worth noting:

- W7 (ADMIN_DIDS validation) leans on the same SplitCSV +
  did.IsValid pair that M5 (activity worker fix) and M6 (S-1
  validator) implicitly depend on via the existing security
  posture. No surprise interaction.

## Atomic per scope ceiling?

All seven commits are well under the 400-line / 8-file
ceiling. Largest is W7 at ~70 LOC across 2 files.

## Decision: stopping WILL FIX iteration here

Remaining WILL FIX items per `03-implementation-plan.md`:
W4, W3, W9, W8, W10. Each carries one of:

- W3 (delete GetByCollection + migrate 5 test callers) —
  largest scope; reasonable but bigger than tonight's risk
  budget warrants.
- W9 (collapse buildBadgeAwardSubjectFilter /
  buildStringSubjectFilter) — non-trivial refactor across two
  KindXSubject paths with separate drift-detection tests; needs
  careful test reshape.
- W8 (ALLOWED_ORIGINS unset → startup error in production) —
  has deployment config implications; could break a deploy
  that relies on the current permissive-default behavior.
  Worth doing but with operator confirmation.
- W4 (collapse GetCIDsByURIs / GetExistingCIDs) and W10
  (extract TextINClause helper) — cosmetic deduplication;
  small value relative to the cumulative diff already shipped.

Per directive: "Do not start a new MUST FIX item if you cannot
complete and verify it before the budget closes." Applying the
same rule to WILL FIX. Cumulative diff is already substantial;
moving to Phase 6 (final re-review) to verify nothing in the
~22 commits introduced a regression before opening the PR.

## Phase 6 plan

Spawn two fresh reviewer agents on the full diff:
1. **Correctness re-review** — walk every commit, flag any
   regression introduced by the cumulative changes.
2. **Smoke test** — full `go test -race -count=1 ./...` plus a
   per-commit bisectability check on the highest-risk commits
   (M5, M7, M8, M9 — the behavioral fixes that touch shared
   surface).

Produce `05-final-review.md` integrating findings.
