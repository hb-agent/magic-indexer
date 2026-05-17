# Plan-review round 1 — issue-87

Two focused reviewers on the nested-where plan:

- **R1 — SQL correctness** (alias collision, correlated EXISTS,
  parameter ordering, lexicon-specific kinds, condition-cap
  accounting)
- **R2 — GraphQL schema + extractor** (two-pass build, field
  collision, description composition, depth interaction,
  recursive InputObject, lifecycle, registry plumbing)

Both came back with actionable findings. One real plan error
caught (cap-bypass), one latent SQL bug worth folding in, plus
seven tighten-ups. Round 2 not warranted.

---

## Decisions

### R1 — SQL correctness

| # | Finding | Decision |
|---|---------|----------|
| R1.1 (real plan error) | `FilterGroup.CountConditions()` walks `Filters` and `Children` but not `Joined`. Plan §4.2 claimed the global `MaxFilterConditions=20` cap still applies via inner roll-up; without an explicit code edit it doesn't. A query with 19 outer leaves + 1 joined filter with 5 inner leaves would pass the check while emitting 24 SQL conditions. | **ACCEPT** — patch the plan: `CountConditions()` must sum `j.Inner.CountConditions()` for each `j` in `Joined`. Two-line edit, but it has to be called out explicitly so the implementation doesn't ship without it. |
| R1.2 (latent fix) | `jsonExtractTyped` (filter.go:615) returns bare `fieldName` for non-JSON columns. Today no column-level field routes through gt/lt, so the bug is latent — but the alias refactor in §4.1 is the right moment to qualify with the alias for symmetry. | **ACCEPT** — add to §4.1's bullet list: "`jsonExtractTyped`'s non-JSON early-return must qualify with the alias, not return the bare field name." |
| R1.3 (note) | Depth budget reset across the EXISTS boundary. Extractor counts joined as `depth+1`; SQL builder restarts inner at `depth=0`. The §4.2 one-level-joined bound caps total nesting, so this is intentional, not a regression. | **ACCEPT (doc-only)** — add a sentence to §4.3 noting the budget reset is intentional and bounded. |
| R1.4 | Alias collision (`r`, `a`, `d`): clean; inner EXISTS introduces `d` as a fresh range-table entry; outer refs correlate by scope. | NOTED — no action |
| R1.5 | Correlated EXISTS shape (`d.uri = r.json->'badge'->>'uri'`): planner-friendly; either parameterised index probe or hash semi-join. The mandatory LOCAL EXPLAIN ANALYZE (acceptance #7) is the belt-and-suspenders. | NOTED — no action |
| R1.6 | Parameter ordering: traced — outer leaves $1,$2 + joined inner $3 + collection $4. Math correct. | NOTED — no action |
| R1.7 | Lexicon-specific kinds (Contributor / UnionSubject / StringSubject): definition.json has none of `subject`, `contributor`, union shapes. "Locked to r." assumption holds today. | NOTED — see R2-guard below for a runtime sentinel |
| R1.8 | Empty Inner branch correctly produces the no-tail-AND EXISTS clause. | NOTED — no action |

### R2 — GraphQL schema + extractor

| # | Finding | Decision |
|---|---------|----------|
| R2.1 | Two-pass build feasibility confirmed via `*graphql.InputObject.AddFieldConfig` (already used for `_and`/`_or` injection at where.go:146-153). Plan's two-pass approach (build all WhereInputs first, then inject joined fields) is correct, but the current orchestration builds WhereInputs lazily inside `buildQueryType` per-lexicon — needs an explicit `buildWhereInputTypes()` phase added between phases 3 and 4 of `Build()`. | **ACCEPT** — patch §4.5 with the explicit phase and a constraint note that `InputObjectConfigFieldMap` must stay non-Thunk so `AddFieldConfig` keeps working. |
| R2.2 | Field collision (`badge` record field + `badge` input field): non-issue — Object types and Input Object types are separate namespaces in GraphQL. Precedent: `subject` already lives on both for badge.award. | NOTED — no action |
| R2.3 (real fix to plan text) | Draft `_or` composition example "subject is X OR there exists a definition with badgeType=endorsement (for any award)" is semantically odd. Saner shape: intersection on subject + disjunction across badge types inside the join: `where: { subject: { eq: <did> }, badge: { _or: [ { badgeType: { eq: "endorsement" } }, { badgeType: { eq: "verification" } } ] } }`. | **ACCEPT** — rewrite the example for the pinned `badgeAwardBadgeDescription`. |
| R2.4 (test gap) | Depth-budget interaction is documented above (R1.3) but the test plan §4.7 has no test asserting the worst-case nesting actually trips the cap. | **ACCEPT** — add `TestExtractFieldFilters_NestedBadgeWhere_DepthCap` to §4.7. |
| R2.5 | Recursive InputObject field-type (referencing another WhereInput) confirmed safe — `_and`/`_or: NewList(whereInput)` is the precedent. | NOTED — no action |
| R2.6 | `whereInputByLexiconID` lifecycle needs an explicit construction phase, not just a struct field. Current `Build()` runs WhereInput construction inline inside `buildQueryType`, which is incompatible with the two-pass approach because target lexicons may not be built yet when the parent is constructed. | **ACCEPT** — same patch as R2.1: move WhereInput construction into its own phase, populate the map there, then both `buildQueryType` and the joined-field injection read from it. |
| R2.7 (extractor plumbing) | `extractFieldFiltersRecursive` is a free function (where.go:199) taking only `lex *lexicon.Lexicon`. The new joined-where branch needs to look up the target lexicon by ID, which requires registry access. Either thread the registry through the signature or move the extractor onto `*Builder`. | **ACCEPT** — pass the registry through the extractor signature. Move-onto-Builder is a bigger refactor; signature plumbing is local. |
| R2.8 (new runtime guard) | The lexicon-specific kinds (Contributor / UnionSubject / StringSubject) emit hardcoded `r.` SQL. If a future descriptor accidentally registers one inside a joined Inner, the SQL is silently wrong. | **ACCEPT** — add a sentinel in `buildSingleFilter`: if `alias != "r"` and `kind != KindScalar`, return an error. Cheap, future-proof. |
| R2.9 (code comment) | `JoinExpr` in the registry is emitted verbatim into SQL. Today values are code-defined; this is safe — but worth pinning by comment so a future contributor doesn't accidentally let request data flow into the field. | **ACCEPT** — code comment at the registry definition site. |

---

## Net effect on the plan

Eleven plan edits applied in place to `docs/issue-87/plan.md`:

1. **§4.1** — add the `jsonExtractTyped` alias-qualification fix to the plumbing scope (R1.2).
2. **§4.2** — explicit `CountConditions` update to sum `Joined` (R1.1).
3. **§4.3** — note the intentional depth-budget reset across EXISTS (R1.3).
4. **§4.5** — explicit `buildWhereInputTypes()` phase between phases 3 and 4 of `Build()`; constraint note about not switching to `InputObjectConfigFieldMapThunk` (R2.1 + R2.6).
5. **§4.5** — rewrite the pinned description's composition example to the saner intersection+disjunction form (R2.3).
6. **§4.6** — extractor receives the lexicon registry by parameter (R2.7).
7. **§4.7** — add `TestExtractFieldFilters_NestedBadgeWhere_DepthCap` (R2.4).
8. **§4.7** — add `TestBuildSingleFilter_LockedKindsRejectNonRSeqAlias` (R2.8).
9. **§4.1** — runtime sentinel in `buildSingleFilter` rejecting lexicon-specific kinds when `alias != "r"` (R2.8).
10. **§4.4** — code comment at registry definition documenting `JoinExpr` is emitted verbatim (R2.9).
11. **§10** — adjust commit sequencing: the WhereInput-phase move (R2.6) lands with the registry/builder commit (item 4 in the existing sequencing), not in a separate refactor commit — the phase split is necessary precisely because the joined-where injection needs it.

## No follow-up round needed

R1 caught one real plan error (the cap-bypass) and one latent
SQL bug worth folding in. R2 caught the two-pass-phase ordering
explicitly. Everything else is doc-tightening. Round 2 would
nit-pick.

## Recommendation

Proceed to implementation.
