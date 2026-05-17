# Plan-review round 1 — issue-88 decisions

Two parallel reviewers — **R1 correctness/SQL** (alias plumbing, AST consumer scan, sentinel sweep) and **R2 DX/ops** (test parity, naming, lexicon-load risk, follow-up tracking).

## Findings × decisions

| # | Reviewer | Severity | Finding | Decision |
|---|---|---|---|---|
| R1.1 | R1 | — | `OpEq` JSON branch alias plumbing emits `e.json @>` correctly with alias param | confirmed correct |
| R1.2 | R1 | — | `CountConditions` walks `Joined` recursively today; new `Arrays` arm matches pattern; cap enforced at `BuildFilterGroupClause` entry | confirmed correct |
| R1.3 | R1 | IMPORTANT | Pseudo-lexicon shape requires explicit `RecordDef`-vs-`ObjectDef` struct conversion (different types, identical underlying `PropertyEntry`); `sync.Once` zero-values are valid; `#`-anchored synthetic ID is the collision-prevention contract since real lex IDs never contain `#` | INTEGRATE — extend plan §5.5 with the conversion note + `#`-anchored ID contract |
| R1.4 | R1 | IMPORTANT | `extractStrongRefFilter` is genuinely new — extractor today has no ref-property branch; passing `{itemIdentifier: {uri: {eq:...}}}` to existing scalar loop hits "Unknown filter operator" warn-and-continue (silent drop). Helper must run BEFORE the scalar-operator loop in the element pseudo-lexicon dispatch | INTEGRATE — extend plan §5.5 with a subsection on element-property dispatch: `ref → strongRef` goes through helper first, else falls through to scalar |
| R1.5 | R1 | — | `jsonb_array_elements` raises `SQLSTATE 22023` on non-array; `CASE WHEN jsonb_typeof = 'array'` guard is equivalent to existing contributor wrapper pattern | confirmed correct |
| R1.6 | R1 | — | `b.whereInputs` mutated single-threaded in Pass 1, read in Pass 2; new cache fits | confirmed correct |
| R1.7 | R1 | — | `StrongRefFilterInput` placement clean (no symbol collision in `internal/graphql/`) | confirmed correct |
| R1.8 | R1 | — | Surface area complete: only consumers of `FilterGroup` tree are `IsEmpty()` (covered) + extractor (covered by `Arrays` guard mirror of `Joined`) | confirmed correct |
| R1.9 | R1 | — | `lower((r.json->>'type') COLLATE "C") = $N` is the exact `OpEqi` emission from filter.go:660-664 | confirmed correct |
| R1.10 | R1 | — | Commit 2 leaves `ArrayFilter` unused (golangci-lint allows unused exported types); commit 3 depends on commit 2 — order respected | confirmed correct |
| R1.11 | R1 | NICE-TO-HAVE | Parameterise the locked-kind sentinel test over `["d", "e"]` rather than duplicate cases | INTEGRATE in §7 test note (parameterise E11.7) |
| R2.1 | R2 | BLOCKER | Test parity gap: E11.4 should pin alias prefix for both JSON `__`-paths AND a column-style reference (analogue of #87's `d.did`). Elements have no column-style references — document the gap in test comment | INTEGRATE — clarify E11.4 in §7: JSON-path only by design (element rows have no columns), one-line comment explains |
| R2.2 | R2 | — | E10/E11 catalogue placement OK (line 107, between E10 and F1) | confirmed correct |
| R2.3 | R2 | BLOCKER | Naming `ItemsItemFilter` is stuttery. Response-side element type is `OrgHypercertsCollectionItem` (verified by dev introspection) — apply the existing `<RecordTypeName>WhereInput` convention | **DECIDED**: use `OrgHypercertsCollectionItemWhereInput`. Matches the only naming precedent in the codebase. Drop the proposed `<Parent><Field><ElementDef>Filter` convention from §5.3 and §11. |
| R2.4 | R2 | NICE-TO-HAVE | `StrongRefFilterInput` placement is mechanical | decided in plan (§5.3) — no change |
| R2.5 | R2 | NICE-TO-HAVE | Pre-merge dev test default: rely on coverage + post-deploy verify | decided — mirrors #87 §9.2 |
| R2.6 | R2 | DOC | Depth math: first inner `_and` after `items` boundary already trips `MaxFilterDepth=3` (depth=4); plan E11.S5 said "two `_and`s push it over." Simplify example. Expected literal: `"exceeds maximum depth"` (where.go:263) | INTEGRATE in §7 — simplify E11.S5 payload to one inner `_and`, assert literal substring |
| R2.7 | R2 | DOC | RUNBOOK has no slow-query playbook; skip — not in scope | confirmed correct |
| R2.8 | R2 | NICE-TO-HAVE | File one GitHub issue covering §9.2 (containment emission) + §9.3 (GIN index), labelled `perf` + `blocked-on-measurement` | DEFERRED — file post-merge, not now (keeps issue tracker tied to actual work, not speculation) |
| R2.9 | R2 | NICE-TO-HAVE | Add §14 "PR body template" so impl-time the PR body matches PR #90 shape | INTEGRATE — add §14 with the four-section template (Summary / Breaking / Out of scope / Test plan) |
| R2.10 | R2 | NICE-TO-HAVE | Tests-with-code sequencing confirmed | no change |
| R2.11 | R2 | BLOCKER (was) | Lexicon-load risk — verify dev has `#item` def | **VERIFIED on dev**: `orgHypercertsCollection` returns `items` as `[OrgHypercertsCollectionItem!]`, meaning the parent lexicon's `#item` def is loaded. Risk cleared. |
| R2.12 | R2 | DOC | `makeElementPseudoLexicon` defined only by passing reference; add a 3-line sketch | INTEGRATE in §5.5 — explicit struct-literal shape |
| R2.13 | R2 | DOC | §9.4 follow-up scope is correct (strongRef is the only ref in `#item` today) | confirmed correct |

## Summary

**Critical (none).** Two blockers from R2 were addressable without operator input:

1. **Naming** (R2.3) decided as `OrgHypercertsCollectionItemWhereInput` based on the existing record-type-name precedent — the response type is already `OrgHypercertsCollectionItem`, so the convention `<RecordType>WhereInput` gives the cleanest schema.
2. **Lexicon-load risk** (R2.11) cleared by dev introspection — the parent lexicon's `#item` def is loaded.

**IMPORTANT plan clarifications**: R1.3 (pseudo-lex shape + `#`-anchor), R1.4 (extractStrongRefFilter wiring order), R2.1 (E11.4 scope), R2.6 (depth example), R2.9 (PR body template), R2.12 (`makeElementPseudoLexicon` sketch).

Plan §5.3, §5.5, §7 (E11.4, E11.7, E11.S5), and a new §14 updated in place.

Proceeding to implementation.
