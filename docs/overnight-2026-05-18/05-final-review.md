# 05 — Final review (Phase 6)

Two fresh reviewers walked the entire overnight diff. Each had
no access to the prior diagnostic findings — the goal was to
catch anything earlier passes missed by approaching cold.

Per-lens documents:

- [`05-final-review-correctness.md`](05-final-review-correctness.md) — correctness, commit-by-commit walk, cross-commit interactions
- [`05-final-review-smoke.md`](05-final-review-smoke.md) — quality gates, per-commit bisectability, CI-blocker sweep

## Top-line verdict: shippable

Both reviewers green. The smoke-test reviewer's verdict
verbatim: "shippable as a single Draft PR into main." The
correctness reviewer's verdict: "the diff is in good shape …
no critical or high-severity issues found."

## Final-pass findings

Three low-severity items surfaced by the correctness reviewer.
F-1 and F-3 fixed in this PR; F-2 deferred with rationale.

### F-1 (low) — comment in `CountByCollectionFiltered` had wrong rationale
Fixed in commit `ad8c8d6` (chore: correct
CountByCollectionFiltered comments). The "no validation needed"
comment claimed safety came from "collection sourced from
lexicon registry," but the generic `records(collection:
String!)` resolver passes user-supplied values through. The
code IS still safe (SQL binds collection as `$1`, never
interpolated), but the rationale was misleading. Replaced with
the accurate parameter-binding rationale + an explicit callout
of the user-supplied path so the contract is obvious.

### F-3 (low) — `metrics.RecordSearchApplied` double-fired on filtered + totalCount
Fixed in the same commit as F-1. The COUNT path's helper
copied the SELECT path's metric emission, so every
filtered-search + totalCount request bumped the counter
twice. Dropped the emission from the COUNT helper; the SELECT
path retains it (it's the user-visible filter event).

### F-2 (low) — `buildFilteredWhereForCount` is a duplicate, not an extraction
**Deferred to operator** — documented in
[`02b-late-findings.md`](02b-late-findings.md) as L-1. The M9
commit added the helper but didn't refactor
`GetByCollectionFiltered` to call it. The two
WHERE-builders are shape-identical today; they will drift on
the next filter-axis addition. The proper fix is a careful
refactor of the hottest query path and wasn't worth a 3am
patch.

## Five-dimension scoring (before vs. after)

| Dimension | Before | After | Evidence |
|---|---|---|---|
| **Problem framing** | 7/10 | 7/10 | The diagnostic surfaced three cross-cutting themes (activity-table broken end-to-end, ~400 LOC of dead OAuth, totalCount stealth defect) that prior reviews hadn't named; the codebase itself is unchanged in framing-quality terms. |
| **Approach** | 8/10 | 8/10 | The diff applies registry/extractor patterns already established in the codebase (#87, #88, #89); no new abstractions invented except the small `activityCleaner` interface (test-driven, justified). |
| **Code quality** | 7/10 | 8/10 | ~1100 LOC of dead code out (3 small helpers + 4 OAuth files). Files that were dense-but-load-bearing stay that way; the deletions target genuine dead exports, not "code I think is ugly." |
| **Robustness** | 6/10 | 8/10 | C-1 (activity worker), C-2 (cursor flusher leak), C-3 (Stop race panic), S-1 (SQL injection surface) were all real bugs the prior reviews hadn't caught. P-1 (totalCount wrong on filtered queries) is the most consequential — it had been shipping broken values to clients since #71. |
| **Evolvability** | 7/10 | 7/10 | One regression: F-2 left a duplicate WHERE-builder that will drift on the next filter-axis addition. Documented as L-1 for follow-up. Otherwise, the W-tier additions (tests T-3, T-4, RUNBOOK section O-1, ADMIN_DIDS validation S-5) tighten future-change confidence at low cost. |

## Cross-commit interaction audit (clean)

The correctness reviewer specifically walked these
interactions and found them clean:

- **M5 + M7 + M8** (consumer lifecycle): M5's per-goroutine
  ticker fix, M7's per-generation context, and M8's
  Run-owns-events-close all compose correctly. The pattern is
  "Run defers cleanup; Stop signals via context/done; no
  external close-on-channel."
- **M6 + M9** (validation policy): M6 added strict
  `validateCollectionName` for the DDL paths; M9's
  `CountByCollectionFiltered` explicitly skips it because the
  COUNT SQL uses parameter binding. Per F-1's fix, the
  rationale is now stated correctly in the comment.
- **M2/M3/M4 + M5/M7/M8** (OAuth deletions vs consumer
  goroutines): no incidental coupling. The OAuth dead code
  was unwired; the consumer-goroutine surgery didn't touch
  OAuth.

## Test count + CI signal

- Tests: 82 → 81 (net −1; deleted 4 dead-OAuth tests, added 3
  new behavioural tests).
- Quality gates: build, vet, lint, race tests all clean.
- Known shared-DB flakes (TestPurge*, TestMigrations_Rollback,
  TestApply_* in notifications) reproduce in the parallel
  suite, pass in isolation, are unrelated to this diff.

## Recommendation

Open the Draft PR `staging → main` per Phase 7. The diff is
green, the cross-commit composition is clean, and the one
deferred follow-up (F-2 / L-1) is sized for a daytime PR not
a 3am patch.
