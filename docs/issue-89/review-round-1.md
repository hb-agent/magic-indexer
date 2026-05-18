# Plan-review round 1 — issue-89 decisions

Two parallel reviewers — **R1 correctness/SQL** (migration shape,
SQL signature, security surface, precedent citations) and **R2
DX/ops** (LOC estimates, test coverage, naming, runbook, operator
questions, acceptance-criterion feasibility).

Reviewers surfaced **2 CRITICAL bugs** in plan code snippets and
**3 HIGH-severity** consistency / completeness issues. All
addressable without changing the design.

## Findings × decisions

| # | Reviewer | Severity | Finding | Decision |
|---|---|---|---|---|
| R1.1 | R1 | CRITICAL | Plan §5 repository snippet uses `[]interface{}` for query args — wrong. Executor contract is `QueryRow(ctx, sql, []database.Value, dest...)`. Sibling CountByDID at records.go:741 is the literal template. | **INTEGRATE** — plan §5 updated to `[]database.Value{database.Text("..."), database.Text(badgeURI)}, &count`. |
| R1.2 | R1 | CRITICAL | Migration 030 missing `-- no-transaction` directive (line 1). Without it, pgx rejects `CREATE INDEX CONCURRENTLY` with SQLSTATE 25001 — lesson logged on migration 021 / 029. | **INTEGRATE** — plan §4 prepends `-- no-transaction` to the migration file. |
| R1.3 | R1 | IMPORTANT | `awardCountDescription` ends with `"follow-up at docs/issue-89/plan.md §9.3"` — docs-path reference leaks repo paths at schema introspection. Existing pinned descriptions (badgeAwardBadgeDescription, etc.) stay external-facing. | **INTEGRATE** — drop the docs-path sentence; end at "...sub-millisecond." Optionally add "Filtered counts (e.g. by issuer) are not yet exposed." for forward-compat hint. |
| R1.4 | R1 | IMPORTANT | Plan §2.1 cites builder.go:243 `labels` resolver as precedent for Resolve on per-collection record types. Wrong: that's on `genericRecord`, NOT per-collection records. Per-collection `labels` at object.go:153 has NO Resolve — value is pre-baked into the node map at builder.go:719. This PR introduces a NEW pattern (Resolve on per-collection records). | **INTEGRATE** — §2.1 acknowledges this is a new pattern; impl reviewer should audit the resolver-invocation contract (`p.Source` is the per-collection node map populated at builder.go:952, which includes `uri`). |
| R1.5 | R1 | IMPORTANT | `NewObjectBuilder` signature change has **6 call sites** (1 in builder.go:44 + 5 in `types_test.go:184,247,366,383,401`). Plan undercounts. Recommends backward-compat: `NewObjectBuilderWithDerivedFields(...)` alongside the existing `NewObjectBuilder`. | **INTEGRATE** — adopt the backward-compat approach. New ctor takes the derived-fields map; existing tests stay green. §7 + §3 surface area updated. |
| R1.6 | R1 | NICE-TO-HAVE | Plan §6 import block doesn't list `fmt` (used in `MustNotReserveField`) or `internal/graphql/types`. | **INTEGRATE** — implementer adds both at code-time; no plan edit needed beyond a §6 note. |
| R1.7 | R1 | NICE-TO-HAVE | §9.3 filtered-awardCount is composable, not circular: parent URI is always ANDed against any inner `badge.uri` filter. | **DOCUMENTED** in §9.3 — no design change. |
| R1.8 | R1 | — | §11 commit 2 bisectability sound. | NO ACTION |
| R1.9 | R1 | — | §4 index shape avoids #86 R1.1 typeof trap. §6 SQL-injection surface clean. §5 empty-URI semantics consistent. §6 cross-package imports already present. | NO ACTION |
| R2.1 | R2 | MEDIUM | LOC estimate of 555 likely 30-40% under (~700-750 realistic). | NOTE in plan §3 — flag as a directional estimate, not a contract. |
| R2.2 | R2 | HIGH | Same as R1.5 (NewObjectBuilder call sites). | Resolved by R1.5 backward-compat decision — test sites untouched. |
| R2.3 | R2 | MEDIUM | Test coverage gaps: no description-drift pin (cf. `TestJoinedWhereRegistry_BadgeAwardBadge`'s byte-equal check at where_test.go:267), no N+1 regression test, no concurrency test. | **INTEGRATE** — §8 adds: (a) description-drift assertion to E12.S1, (b) E12.S7 "fires-once-per-row" via mock-repo counter for the dataloader-follow-up assertion baseline, (c) skip concurrency test for v1 (graphql-go's resolver dispatch contract is well-tested upstream; not worth the test complexity here). |
| R2.4 | R2 | MEDIUM | `MustNotReserveField` is a dangling helper — no caller spec. | **INTEGRATE** — §6 calls it in an `init()` inside `derived_fields.go` iterating `derivedFieldRegistry`. Startup-fail mode (panic at init), surfaces immediately on `go build` / first-run smoke. |
| R2.5 | R2 | LOW | File §9.1 dataloader as GH issue now. | **DEFERRED** — file post-merge alongside the §9.2/9.3 perf follow-up from #88. |
| R2.6 | R2 | LOW | PR body parity — add test count. | INTEGRATE in §13 (PR body template). |
| R2.7 | R2 | LOW | Migration 030 numbering is safe (029 is the last). | NO ACTION |
| R2.8 | R2 | HIGH | **§12 acceptance criterion #6 is currently impossible**: `appCertifiedBadgeAward(where: { badge: { uri: { eq: <def-uri> } } })` requires `uri` exposed on `AppCertifiedBadgeDefinitionWhereInput` — it isn't (where.go:203-248 iterates lexicon properties + adds `did`; `uri` is a top-level record column, not exposed). | **INTEGRATE** — §12 acceptance criterion #6 changed to: "Cross-check: sum `awardCount` across a page of definitions equals a known seed-set's total award count for that issuer (computed via `appCertifiedBadgeAward(where: { did: { eq: <issuer> } }) { totalCount }`)." No `uri` filter needed. |
| R2.9 | R2 | MEDIUM | E12 catalogue entry underspecified — needs sample SQL + sample wire-level query in plan. | **INTEGRATE** — plan §8.5 (new) embeds the E12 detail-section template so the implementer pastes verbatim. |
| R2.10 | R2 | HIGH | Missing §14 "open questions for the operator" section. Three load-bearing decisions: nullability (`Int!` vs `Int`), name (`awardCount` confirmed by the issue text — not actually open), CONCURRENTLY confirmation. | **DECIDED INLINE** — name is `awardCount` per the issue's own example. Nullability is `Int!` (zero awards returns 0, never null). CONCURRENTLY is required (production-size record table — same as #86's follow.subject index). No §14 needed; all three are forced choices. |
| R2.11 | R2 | MEDIUM | Plan §4 references "see RUNBOOK.md for INVALID-index recovery" but the runbook doesn't have that section. | **INTEGRATE** — drop the RUNBOOK reference from the migration comment OR add a one-paragraph section to RUNBOOK. Drop the reference for now (smaller surface); §9.5 adds "RUNBOOK section: Recovering from an INVALID CONCURRENTLY index" as a follow-up. |
| R2.12 | R2 | LOW | §1 non-goals could add "no schema-level result caching" and "no admin-API toggle to enable/disable per-DID." | INTEGRATE in §1 non-goals. |

## Plan edits

The plan is updated in place to integrate every CRITICAL and
IMPORTANT decision. Specifically:

- §1 non-goals expanded (R2.12)
- §2.1 acknowledges the new resolver-on-per-collection pattern
  with audit pointer for impl review (R1.4)
- §3 surface-area table updated to reflect the backward-compat
  ctor approach (no test-file churn) (R1.5)
- §4 migration prepends `-- no-transaction`, drops the
  RUNBOOK reference (R1.2, R2.11)
- §5 repository snippet uses `[]database.Value` + `database.Text`
  (R1.1)
- §6 pinned description rewritten without docs-path leak
  (R1.3); `MustNotReserveField` documented as `init()`-time
  caller (R2.4)
- §7 adds the `NewObjectBuilderWithDerivedFields` backward-compat
  ctor (R1.5)
- §8 adds description-drift pin to E12.S1 + adds E12.S7
  N+1-regression test (R2.3)
- §8.5 (new) embeds the E12 behavioral-catalogue entry verbatim
  (R2.9)
- §9 (follow-ups) adds RUNBOOK-INVALID-index-recovery as §9.5
  (R2.11)
- §12 acceptance criterion #6 changed to per-DID totalCount
  cross-check (R2.8)
- §13 PR body template adds test count (R2.6)

No round 2 needed — proceed to implementation.
