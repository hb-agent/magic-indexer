# Implementation-review round 2 — issue-89

Two parallel reviewers after the 3-commit Track-89 series landed:

- **IR1 — Functional correctness** (migration + repository
  byte-equality, predicate_implied_by, helper signature, resolver
  guards, init-time panic reachability, ObjectBuilder
  integration, backward-compat ctor preservation, N+1
  boundedness, test robustness, security surface)
- **IR2 — Smoke test** (quality gates, per-commit bisectability,
  migration sanity, files audit, test coverage count, no doc-
  path leaks, schema wiring, behavioural catalogue placement,
  round-1 integration spot-checks, CI risk sweep)

Both returned **green — ship it**. No CRITICAL or IMPORTANT
findings. IR1 flagged two NICE-TO-HAVE items, both observability
improvements that collapse to one-line edits.

---

## Findings × decisions

### IR1 — Functional correctness

| # | Finding | Decision |
|---|---------|----------|
| IR1.1 | Drift test cross-checks `json->'badge'->>'uri'` byte-equality between migration and records.go, but does NOT pin the collection literal `app.certified.badge.award`. If the partial-predicate literal drifted on either side (e.g. accidental rename to "awards"), predicate_implied_by would silently fail and the planner would seq-scan — without the test tripping. | **INTEGRATED** — drift test extended with two extra assertions: both migration and records.go must contain the collection literal verbatim. |
| IR1.2 | `resolveAwardCount` returns `(0, nil)` silently when `p.Source` is not a map. The connection resolver always sets a map source (builder.go:952), so this is "shouldn't happen," but a future refactor routing a typed struct through would silently return 0 with no observable signal. | **INTEGRATED** — added `slog.WarnContext` with the actual source type for the dead-letter path. Mirrors the existing nil-repositories warn at the same site. |
| IR1.3 | Migration 030 + repository SQL byte-equality confirmed. `-- no-transaction` directive on line 1. | NO ACTION |
| IR1.4 | predicate_implied_by works for the index shape (same pattern as migrations 026 + 029, both production-proven). | NO ACTION |
| IR1.5 | Repository helper signature matches CountByDID's pattern (line 741): `[]database.Value{database.Text(...)}` + variadic dest. Error wrapped via `fmt.Errorf`. | NO ACTION |
| IR1.6 | Resolver guards: 3 early-return paths (non-map source, empty URI, nil repos) all return `(0, nil)`. No nil-leak that would crash the `Int!` marshal. | NO ACTION |
| IR1.7 | `derivedFieldRegistry` shape: NonNull(Int) + pinned description + Resolve. `awardCountDescription` has no docs-path leak (R1.3 satisfied). | NO ACTION |
| IR1.8 | `init()` panic for collisions is reachable — Go's package-init order guarantees `types.ReservedRecordFields` is populated before this package's init() runs. | NO ACTION |
| IR1.9 | `ObjectBuilder` integration: derived-fields loop runs AFTER lexicon-property loop; nil-map case is a Go no-op (range over nil map). Lexicon wins on collision (warn-and-continue). | NO ACTION |
| IR1.10 | Backward-compat ctor preserves 5 test sites in types_test.go (lines 184, 247, 366, 383, 401). | NO ACTION |
| IR1.11 | Schema-builder wiring at line 44 passes `derivedFieldsForObjectBuilder()`. Flattening helper reuses registry's `*graphql.Field` pointers (verified by `TestDerivedFieldsForObjectBuilder_ShapeMatches`). | NO ACTION |
| IR1.12 | N+1 bounded: `MaxPageSize = 100` (query/connection.go:22). Resolver is a leaf scalar Resolve, no recursion, no fan-out beyond 1 call per row. | NO ACTION |
| IR1.13 | Test robustness: `TestRecordsRepository_CountAwardsByBadgeURI` seeds the critical collection-filter-excludes-non-award case. `TestBuildRecordFields_BadgeDefinitionHasAwardCount` confirms badge.definition has the field AND badge.award control case does not. | NO ACTION |
| IR1.14 | No security regressions: URI bound via `database.Text(badgeURI)`, never string-concat. Registry SQL is constant. | NO ACTION |

### IR2 — Smoke test

All 10 checks pass. Notable findings:

- **Quality gates** clean: build, vet, golangci-lint = 0 issues.
- **Per-commit bisectability** holds: both 778eb1c (migration +
  repository) and 4d893f7 (schema) build standalone.
- **Migration sanity**: 60 files now in postgres/ (58 baseline +
  2 new for 030); both up.sql and down.sql have `-- no-transaction`
  on line 1.
- **Files audit**: 12 files changed, all in planned surface area.
  No drift.
- **Test coverage**: 11 new tests (4 repo + 7 schema; one
  bonus above the planned 10).
- **No doc-path leaks**: `grep "docs/issue\|plan.md"` in
  derived_fields.go returns 0 hits in any string literal.
- **Schema wiring**: builder.go:44 uses
  NewObjectBuilderWithDerivedFields with the flattened registry;
  5 legacy test sites at types_test.go still call the old
  NewObjectBuilder unchanged.
- **Catalogue placement**: E12 at line 108 between E11 (107) and
  F1 (109).
- **CI risk sweep**: clean for all #89-adjacent packages. One
  unrelated failure in `internal/notifications` (9
  TestApply_Aggregated_* tests — same Postgres-state-collision
  flake family as the known TestPurgeTokenSigner /
  TestMigrations_Rollback cases from prior reviews; tests pass
  in isolation). The #89 diff does not touch `internal/notifications/`.

---

## Verdict

Ship-ready. Two NICE-TO-HAVE observability improvements
integrated as a follow-up commit. No correctness concerns. The
change cleanly extends #87/#88's registry-based pattern to derived
record fields (a new shape — no previous Resolve-bearing fields on
per-collection record types; the implementation reuses the
labels-resolver guard idiom from genericRecord).

## Known follow-ups (unchanged from plan §9)

1. Dataloader batching to collapse N+1 → 1 query per page
   (§9.1); trigger: first page-size complaint or slow-tab
   telemetry.
2. Connection-level `totalAwardCount` (§9.2).
3. Filtered `awardCount(where: ...)` accepting the
   AppCertifiedBadgeAwardWhereInput from #87 (§9.3).
4. Generic "joinedCountRegistry" abstraction (§9.4); trigger:
   2nd registry entry lands.
5. RUNBOOK section for INVALID-CONCURRENTLY-index recovery
   (§9.5).

## Recommendation

Open Draft PR `staging → main` per plan §13 template. Wait for
CI green, then stop and notify operator.
