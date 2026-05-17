# Implementation-review round 2 — issue-87

Two parallel reviewers after the 3-commit Track-87 series landed:

- **IR1 — Functional correctness** (alias plumbing
  completeness, EXISTS parameter ordering, locked-kind sentinel
  reachability, two-phase build determinism, AddFieldConfig
  with InputObject target, extractor precedence, Inner.Joined
  guard, adjacent-commit scope)
- **IR2 — Smoke test** (quality gates, per-commit
  bisectability, bare-json grep, files-audit, test coverage
  count, migration count, schema phase wiring)

Both returned **green — ship it**. No bugs, no follow-up commit
required.

---

## Decisions

### IR1 — Functional correctness

| # | Finding | Decision |
|---|---------|----------|
| IR1.1 | Alias plumbing complete: every SQL-emission site in `buildSingleFilter` is alias-qualified. Unqualified `r.subject_did` / `r.json->>'subject'` references in locked-kind helpers are protected by the sentinel. | NO ACTION |
| IR1.2 | EXISTS parameter ordering correct: outer leaves → inner → collection. Empty-inner case correctly skips `AND (...)` while still consuming the collection slot. Pinned by `TestBuildFilterGroupClause_JoinedFilter_InsideOr` and `_EmptyInner`. | NO ACTION |
| IR1.3 | Locked-kind sentinel is unreachable today (badge.definition has none of `subject`, `contributor`, union shapes), but the sentinel is correct defense-in-depth as the comment claims. | NO ACTION |
| IR1.4 | Two-phase WhereInput build is deterministic: Pass 1 is pure-write, Pass 2 looks up every target via map key — iteration order in either pass doesn't matter. | NO ACTION |
| IR1.5 | `AddFieldConfig` with `*graphql.InputObject` target works correctly: graphql-go traverses reachable types from query roots, and the parent input is attached to the query args, so the joined target is picked up by the schema. | NO ACTION |
| IR1.6 | Extractor precedence: joined-where check runs first, then filterRegistry. For badge.award, `subject` ∈ filterRegistry but ∉ joinedWhereRegistry, and `badge` is the inverse — no conflict possible. | NO ACTION |
| IR1.7 (follow-up note) | `TestExtractFieldFilters_NestedJoined_Rejected` is a tautology today — no joined target has its own joined-where entry, so the `len(inner.Joined) > 0` guard cannot fire from any payload. The test's own body acknowledges this. | **NOTE FOR LATER** — when a second joined-where registry entry lands, extend this test with a real two-level payload so the guard is actually exercised. Add to plan's known follow-ups; do not bake a synthetic two-level case in now since the registry has no shape that would produce one. |
| IR1.8 | Adjacent commits clean: 6 files touched, all in planned surface area. No collateral. | NO ACTION |

### IR2 — Smoke test

All 7 checks pass. Notable findings:

- 14 bare-`json` matches in filter.go, all accounted for: 5 are
  inside `jsonExtract` (using `%s.json->...` with alias
  parameter), 1 is in `BuildSortExpr` (single-table outer
  scope, correctly unaliased), 1 is the documented r-locked
  `KindStringSubject indexedExpr` const, the rest are in
  comments. No bare-json leak.
- Test coverage: 10 new tests (7 filter-side + 3 schema-side
  plus 1 documented-tautology placeholder).
- Migration count unchanged at 58 — issue #87 needs no new
  migrations, the existing record(uri) primary key serves the
  inner join probe.

---

## No follow-up commit needed

The one note (IR1.7) is documentation-only and points at a
future extension when the registry grows. Implementation is
correct.

## Known follow-ups

Recorded here so the next registry edit picks them up:

1. **Extend `TestExtractFieldFilters_NestedJoined_Rejected`**
   when a second `joinedWhereRegistry` entry lands whose target
   itself has its own joined-where entry. Today it's a
   tautology — re-extending it then is the right time.

## Recommendation

Update PR #85's body to reflect the combined scope (Batch C —
issue #87) and wait for CI green.
