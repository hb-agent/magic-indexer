# Plan-review round 1 — issue-86

Two focused reviewers (smaller mix than the multi-track audit
work — this is one feature, well-scoped):

- **R1 — Index/SQL correctness** (will the planner pick the
  index? IMMUTABLE check, partial-index predicate matching,
  regression-test extractor)
- **R2 — GraphQL schema + composition** (registry change
  produces the right shape; `subject` collision between
  auto-generated and registry; `_or` composition)

Both returned actionable findings. One is a real bug; the rest
are smaller. Round 2 not warranted.

---

## Decisions

### R1 — Index/SQL correctness

| # | Finding | Decision | Rationale |
|---|---------|----------|-----------|
| R1.1 | **CRITICAL — partial-index predicate `AND jsonb_typeof(json->'subject') = 'string'` will not be discoverable by Postgres's structural `predicate_implied_by`.** The runtime emits `r.json->>'subject' = $N` which doesn't reference `jsonb_typeof(json->'subject')` at all. The planner can't prove implication structurally (it's a different operator on a different sub-expression of the JSON), so the partial index would be created but never matched — silent degradation to sequential scan. | **ACCEPT** | Drop the `jsonb_typeof` predicate from migration 029. Lexicon-required string typing makes the guard belt-and-suspenders, not correctness; the collection-only partial predicate is sufficient. Index footprint marginally wider (legacy/malformed rows index as NULL, which btree stores compactly), but correctness wins. |
| R1.2 | The regression test plan reuses `extractGinExpression` (`filter_unit_test.go:803`), but that helper hard-codes `USING\s+gin\s*\(`. A btree expression index uses `ON record ((expr))` (double parens, no `USING` clause). Plan section 4.5 was wrong. | **ACCEPT** | Add a sibling `extractBtreeExpression` helper that anchors on `ON\s+record\s*\(\(` and paren-balance extracts the inner expression. |
| R1.3 | `CREATE INDEX CONCURRENTLY IF NOT EXISTS` has a known foot-gun: if a CONCURRENTLY build fails partway, the index lands in an `INVALID` state, and `IF NOT EXISTS` will then *skip* re-creating it. Pre-existing pattern across migrations 024/026/028, so this isn't new — but worth surfacing for operators. | **ACCEPT (doc-only)** | Add an operator note to plan §9: if migration completes with a warning, run `SELECT indexrelid::regclass FROM pg_index WHERE NOT indisvalid;` and manually `REINDEX INDEX CONCURRENTLY` or `DROP` + re-run. No code change. |
| R1.4 | `extractInValues` returns `(values, hasNull)`; existing call sites in `buildContributorFilter` and `buildBadgeAwardSubjectFilter` discard `hasNull`. Plan inherits that pattern. Safe because `DIDFilterInput`'s schema validation rejects null list entries before the SQL layer sees them. | **ACCEPT (comment-only)** | Add a one-line comment in `buildStringSubjectFilter` noting the `hasNull` is intentionally discarded — mirrors the pattern but makes it discoverable. (Also worth back-applying to the existing two builders, but that's drive-by; skip for this PR's scope.) |
| R1.5 | `->>` operator on `jsonb` is IMMUTABLE in Postgres 12+. Plan's assumption verified. | **NOTED** | No action. |
| R1.6 | Parameter-bound `collection = $coll` matches a partial index predicate `collection = 'app.certified.graph.follow'` because Postgres folds `Var op Param` like `Var op Const` for partial-index implication. The badge.award index (#026) works the same way; no subtle difference for follow. | **NOTED** | No action. |

### R2 — GraphQL schema + composition

| # | Finding | Decision | Rationale |
|---|---------|----------|-----------|
| R2.1 | **No silent degradation** despite the `subject` field appearing on both the auto-generated path (lexicon property `{type: string, format: did}`) and the registry path. On the schema side, the registry write happens *after* the property loop, so it wins. On the extractor side, the registry lookup runs *before* the property-derived branch and `continue`s. Both paths route to `KindStringSubject`. | **NOTED** | No bug. Worth keeping the design invariant explicit. |
| R2.2 | Defensive: add a one-line comment near `where.go:121` noting "registry intentionally overrides duplicate property-derived field" so a future refactor doesn't reorder the loops. | **ACCEPT** | One-line comment, cheap insurance against drift. |
| R2.3 | Plan §4.5 lists DB-layer regression + unit tests but no schema-layer registry pin. `where_test.go:152-164` has `TestFilterRegistry_BadgeAwardSubject` as the precedent. | **ACCEPT** | Add `TestFilterRegistry_GraphFollowSubject` mirroring the badge.award test. |
| R2.4 | `did` filter does in fact target the record author column (`r.did`) via `qualifyColumn` in filter.go. Plan claim verified. | **NOTED** | No action. |
| R2.5 | `_or` composition flows through `FilterGroup.Children` / `GroupOR` and is wire-supported. The pinned description's composition example is accurate. | **NOTED** | No action. |
| R2.6 | `DIDFilterInput`'s schema-side validation rejects `isNull` for registry-routed fields — the input type doesn't expose `isNull` to consumers, so users can't even send it. Routes through `buildDIDOnlyEqInFilter` which only accepts `eq`/`in`. | **NOTED** | No action; aligns with the plan's "unsupported operators error" expectation. |

---

## Net effect on the plan

Three plan edits, applied in place to `docs/issue-86/plan.md`:

1. **§4.2** — drop `AND jsonb_typeof(json->'subject') = 'string'` from migration 029's WHERE clause. Update the rationale comment.
2. **§4.5** — name the new helper `extractBtreeExpression`, not "reuse `extractGinExpression`."
3. **§4.5** — add a schema-layer pin test
   `TestFilterRegistry_GraphFollowSubject` in
   `internal/graphql/schema/where_test.go`.
4. **§9** — operator note: invalid-index recovery procedure (pre-existing across 024/026/028, surfaced for clarity).

Two small code-comment additions to track during implementation:

- `buildStringSubjectFilter`: one-line comment on the discarded
  `hasNull` second return from `extractInValues`.
- `where.go` around L121: one-line comment on the registry-wins
  invariant.

## Round 2?

Not warranted. R1.1 is the only material correctness item and the
fix is one SQL clause. The remaining items are doc / test /
comment improvements.

## Recommendation

Proceed to implementation.
