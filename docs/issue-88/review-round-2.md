# Implementation-review round 2 — issue-88

Two parallel reviewers after the 3-commit Track-88 series landed:

- **IR1 — Functional correctness** (EXISTS parenthesisation,
  CASE WHEN double-interpolation, locked-kind sentinel for
  alias `e`, parameter ordering inside _or, pseudo-lexicon
  shape, extractStrongRefFilter rejection paths, extractor
  branch ordering, AddFieldConfig with InputObject target,
  Pass 2 idempotency, test robustness)
- **IR2 — Smoke test** (quality gates, per-commit
  bisectability, bare-json grep, files audit, test coverage,
  migration count unchanged, schema phase 3b wiring,
  catalogue placement, StrongRefFilterInput cross-package
  reach, round-1 integration spot-checks, end-to-end SQL
  emission sanity, full CI sweep)

Both returned **green — ship it**. No CRITICAL/IMPORTANT
findings. IR1 flagged three NICE-TO-HAVE clarity items that
all collapse to one-line edits — landed in a single follow-up
commit (`f29a4d3 chore(filter,schema): IR1 clarity fixes`).

---

## Findings × decisions

### IR1 — Functional correctness

| # | Finding | Decision |
|---|---------|----------|
| IR1.1 | EXISTS inner-clause parenthesisation: `array-where` emits `EXISTS (... AS e(json) WHERE %s)` without wrapping the inner in extra parens. Safe — the WHERE has the inner clause as its sole top-level expression, so even a top-level `OR` parses as the EXISTS predicate. Joined-where wraps because it composes `d.collection AND d.uri AND (%s)` where parens guard precedence; array-where has no sibling, so no parens needed. The asymmetry with joined-where is intentional. | NO ACTION |
| IR1.2 | CASE WHEN double-interpolation: `arr.ArrayPath` is interpolated three times, all byte-identical reads from the same struct field. Registry value `"r.json->'items'"` confirmed. Postgres handles repeated subexpressions fine; no divergent-expression hazard. | NO ACTION |
| IR1.3 | Sentinel error message at `filter.go:667` says "joined-where subquery (alias %q)" but alias `"e"` is also rejected. An operator reading the error would misdiagnose the scope. | **INTEGRATED** — error message updated to "nested subquery (joined-where or array-element, alias %q)" so both rejected aliases are surfaced. |
| IR1.4 | Param ordering inside Arrays inside _or: `paramIdx += len(innerParams)` with no `+1` for a collection param (arrays have none). Pinned by `TestBuildFilterGroupClause_ArrayFilter_InsideOr` — outer leaf at $1, inner at $2, total=2 params. Matches the emitter. | NO ACTION |
| IR1.5 | Pseudo-lexicon construction: `makeElementPseudoLexicon` builds a new `*lexicon.Lexicon` with a fresh `RecordDef{Properties: elemDef.Object.Properties}`. Private `propIndex`/`propIndexOnce` zero-init correctly. The `#`-anchored ID never matches any of the three registries by construction, AND `extractElementFilters` never looks up by `lex.ID` anyway — only walks Properties. Safe. | NO ACTION |
| IR1.6 | extractStrongRefFilter rejection paths: unknown sub-key, unsupported operator (e.g. `contains`), and type mismatches all surface clear errors. `eq: 123` rejected at GraphQL parse layer before reaching the extractor (StringFilterInput.eq is typed `graphql.String`). | NO ACTION |
| IR1.7 | Branch-ordering doc gap: `TestExtractFieldFilters_ItemsExtractorPrecedence` shows non-collision (title→Filters, items→Arrays) but doesn't pin what wins on collision because no collision is reachable today. A future contributor reading just the test wouldn't know the explicit precedence (joined-where > array-where > filterRegistry > scalar). | **INTEGRATED** — comment added at where.go's array-where branch documenting the explicit branch precedence so a future colliding registry edit is a deliberate choice, not an accident. |
| IR1.8 | Pointer equality in `fd.Type != types.StrongRefFilterInput`: Go interface equality compares (type-descriptor, value). Both stored as `*InputObject` pointing at the same package global, so comparison succeeds and would catch a regression that swapped in a different InputObject. Meaningful assertion. | NO ACTION |
| IR1.9 | `buildWhereInputTypes` idempotency: called once from `Builder.Build()` today. A double-call would register two distinct `*graphql.InputObject` instances with the same `Name()` (because `buildArrayElementInputType` constructs a fresh InputObject each time) — graphql-go's schema-validation pass would raise. Today safe; future contributors adding a rebuild hook should know. | **INTEGRATED** — single-call invariant documented as a code comment at the top of `buildWhereInputTypes`, with the failure mode and IR1.9 anchor. |
| IR1.10 | E11.1–E11.6 + E11.S1–E11.S6 test robustness: each assertion would catch a different real regression. E11.4 specifically uses OpNeq to bypass the OpEq+IsJSON containment short-circuit, exercising the jsonExtract alias-plumbing path — not a tautology. E11.S6 acknowledged in the test body as a placeholder per R1.7 plan follow-up. | NO ACTION |

### IR2 — Smoke test

All 12 checks pass. Notable findings:

- **Quality gates** clean: build, vet, golangci-lint = 0
  issues. Affected-package tests pass.
- **Per-commit bisectability** holds: both 93f0eda
  (filter-side) and 5a1aabb (schema-side) build standalone.
- **No bare-json leak**: every match in filter.go uses `%s`
  substitution fed by `arr.ArrayPath` (registry-defined) or
  alias parameters. New EXISTS emitter aliases the row to
  `e(json)` and inner clauses use `e.`-prefix.
- **Files audit**: 9 files, all in planned surface area —
  3 doc, filter.go + test, builder.go, where.go + test,
  types/filters.go.
- **13 new tests** (7 filter + 6 schema), meets bar.
- **Migration count** unchanged at 58.
- **Schema phase 3b** correctly wired in `Build()` —
  Pass 2 iterates both `joinedWhereRegistry` and
  `arrayWhereRegistry`, symmetric `slog.Warn` for missing
  targets / element defs.
- **E11 placement** at line 107 (between E10 at 106 and F1
  at 109).
- **StrongRefFilterInput** exported from types package,
  consumed cross-package as `types.StrongRefFilterInput`.
- **Round-1 integrations all landed**: name is
  `OrgHypercertsCollectionItemWhereInput` (no `ItemsItemFilter`
  anywhere), sentinel test parameterises over `["d", "e"]`,
  depth-cap test asserts literal `"exceeds maximum depth"`.
- **End-to-end SQL emission** pinned by E11.1: emits
  `EXISTS (SELECT 1 FROM jsonb_array_elements(CASE WHEN
  jsonb_typeof(r.json->'items') = 'array' THEN r.json->'items'
  ELSE '[]'::jsonb END) AS e(json) WHERE e.json @>
  $1::jsonb)`. Matches plan §6.1.
- **CI sweep** clean for all #88-adjacent packages. One
  unrelated failure: `TestMigrations_Rollback` (Postgres
  shared-state collision, same class as the known #86 flakes
  `TestPurgeTokenSigner_RejectsTamperedSignature` /
  `TestPurgeActor_RequiresAdmin`). Confirmed non-#88 by file
  scope — migration test does not touch the filter/schema
  paths.

---

## Verdict

Ship-ready. Three NICE-TO-HAVE clarity edits integrated as a
follow-up commit. No code-correctness concerns. The change
mirrors #87's joined-where pattern faithfully where it should
and diverges intentionally (no collection-param, no inner-paren
wrap, defensive `jsonb_typeof = 'array'` guard) where the
array-element shape demands.

## Known follow-ups (unchanged from plan §9 + R1.7)

1. Extend `TestExtractFieldFilters_NestedArrayWhere_Rejected`
   with a real two-level payload when a second array-where
   registry entry lands whose element type itself has another
   array-where entry. Today the guard is unreachable; tracked
   as plan §9.5.
2. File one GitHub issue covering plan §9.2 (`@>` containment
   emission) + §9.3 (GIN index) post-merge, labelled `perf` +
   `blocked-on-measurement`.

## Recommendation

Open Draft PR `staging → main` per plan §14 template. Wait for
CI green, then stop and notify operator.
