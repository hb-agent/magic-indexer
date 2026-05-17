# Implementation-review round 2 — issue-86

Two parallel reviewers after the 5-commit Track-86 series landed:

- **IR1 — Functional correctness** (index/runtime byte-coupling,
  `extractBtreeExpression` extraction, `subject` field-collision
  resolution, schema-side pin test, `_or` composition trace)
- **IR2 — Smoke test** (quality gates, drift-detector mutation
  test, per-commit bisectability, files-audit, registry presence,
  migration numbering)

Both returned **green — ship it**. No bugs, no follow-up commit
required. Round 3 not warranted.

---

## Decisions

### IR1 — Functional correctness

| # | Finding | Decision |
|---|---------|----------|
| IR1.1 | Index/runtime coupling correct. Migration 029 indexes `((json->>'subject'))` and `buildStringSubjectFilter` emits `r.json->>'subject'` — only the `r.` alias divergence, exactly as the regression test expects. The `jsonb_typeof` predicate that the plan-review caught (R1.1) is NOT re-introduced; the only `WHERE` predicate is the `collection =` partial. | NO ACTION |
| IR1.2 | `extractBtreeExpression` extracts `(json->>'subject')` from the migration's `ON record ((json->>'subject'))`; test then strips outer parens to `json->>'subject'`. Normalize logic only handles `r.json` → `json` + whitespace, so any operator/path change fails loudly. No over-permissiveness. | NO ACTION |
| IR1.3 | Schema-side `subject` collision resolved correctly: property loop writes `DIDFilterInput` first, registry loop overwrites with the same type + pinned description. Extractor's registry-first ordering routes to `KindStringSubject`. Both halves match the plan. Pin comment at `where.go:128-142` makes the invariant explicit. | NO ACTION |
| IR1.4 | `TestFilterRegistry_GraphFollowSubject` covers all three regressions named in R2.3 (removal, `Kind` change, description drift) plus negative entries for other lexicons. | NO ACTION |
| IR1.5 | `_or` composition example in the pinned description traces correctly: `where: { _or: [ { did: { eq: ... } }, { subject: { eq: ... } } ] }` produces `r.did = $1 OR r.json->>'subject' = $2`, both index-served. The pinned description is honest. | NO ACTION |
| IR1.6 | Adjacent commits clean. 8 files total, all in scope. No leaked typos, no behaviour shifts in unrelated code. | NO ACTION |

### IR2 — Smoke test

| # | Finding | Decision |
|---|---------|----------|
| IR2.1 | All quality gates clean: build/vet/golangci-lint/test. | NO ACTION |
| IR2.2 | Drift-detector test catches the mutated migration with a clear, actionable error message naming both halves to update. Restored cleanly. | NO ACTION |
| IR2.3 | All 5 commits build standalone. Per-commit bisect would terminate immediately on any introduced bad commit. | NO ACTION |
| IR2.4 | Diff scope is exactly 8 files, +508 lines, all expected. No collateral. | NO ACTION |
| IR2.5 | Registry entry present at `where.go:80`; `KindStringSubject` declaration + enum entry + dispatch arm all present in `filter.go`. | NO ACTION |
| IR2.6 | Migration count clean: 58 files (29 up + 29 down), `029_*` is the latest, no collision with `028_*`. | NO ACTION |

## No follow-up commit needed

Plan-review round 1 caught the one real correctness issue (R1.1,
the `jsonb_typeof` predicate) before any code was written.
Implementation matched the plan exactly. Round 2 finds the same
result both audits expected.

## Recommendation

Open Draft PR `staging → main` referencing issue #86.
