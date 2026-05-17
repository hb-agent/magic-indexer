# Implementation plan — issue #86

> Ingest `app.certified.graph.follow` + add a `subject` filter so
> the certified-app can fetch "who follows this DID."

## 1. Evaluate the request

Issue is well-defined and useful. The certified-app needs a
server-side `subject` filter on follow records because follows
live on the *follower's* PDS — without an index we'd have to fan
out across every user's PDS to assemble a followers list.
Identical scaling problem to #65/#78 (badge.award subject).

The proposed shape (`appCertifiedGraphFollow(where: { subject: {
eq: "did:plc:..." } }, first, after)`) drops cleanly into the
existing per-collection auto-generated resolver path. `did`
filtering for the author and `_or`/`_and` composition are
already available via the standard `WhereInput` machinery.

Pushback considered: none. The data is real, the read pattern is
correct (paginated, filtered server-side), the field names match
the lexicon. Proceed.

## 2. Approach decision: expression index, not generated column

The repo has two precedents for "filter on a non-scalar JSON
path":

- **Contributor (#024)**: partial GIN expression index +
  IMMUTABLE function wrapper. No new column. Used for
  `KindArrayContributor` on `org.hypercerts.claim.activity`.
- **BadgeAward subject (#025+#026)**: STORED generated column
  (`record.subject_did`) extracted by a multi-arm CASE expression
  + partial btree index. Used for `KindUnionSubject` on
  `app.certified.badge.award`.

The badge.award pattern was warranted because `subject` there is
a union (`app.certified.defs#did` object, `com.atproto.repo.strongRef`
object, defensive bare-string `at://` URI) — the multi-arm
extraction is non-trivial and STORED is the right tool.

For follow records, `subject` is the bare string format
`{type: string, format: did}`. Extraction is just
`json->>'subject'`. A new column would force a table rewrite on
the existing 7k+ row `record` table (`ALTER TABLE record ADD
COLUMN ... GENERATED ALWAYS AS (...) STORED` takes an ACCESS
EXCLUSIVE lock on Postgres < 18 — we're not on 18 yet, per
migration 025's operator note).

**Decision: partial expression index, no column.** Mirrors the
contributor pattern at the SQL level — but the expression is
simpler (`(json->>'subject')` vs the IMMUTABLE function call) so
no wrapper function is needed; Postgres handles `->>` as
IMMUTABLE on its own.

The price: a Track-1-style regression test guarding the
byte-for-byte coupling between the index expression and the
filter SQL. Cheap, well-understood from #024.

## 3. Surface area

| Layer | File | Change |
|---|---|---|
| Lexicon | `testdata/lexicons/app/certified/graph/follow.json` | NEW — fetched from `hypercerts-org/hypercerts-lexicon@feature/add-graph-follow-lexicon`. |
| Migration | `internal/database/migrations/postgres/029_add_follow_subject_index.up.sql` + `.down.sql` | NEW — partial expression index on `(json->>'subject')` scoped to follow collection. CONCURRENTLY, single statement, no-transaction. |
| Filter SQL | `internal/database/repositories/filter.go` | NEW `FilterKind` enum value `KindStringSubject` (more general than `KindGraphFollowSubject` — future bare-DID subject filters can reuse). NEW `buildStringSubjectFilter` function. NEW arm in `buildSingleFilter` dispatch switch. |
| Schema registry | `internal/graphql/schema/where.go` | NEW entry in `filterRegistry`: `app.certified.graph.follow` → `subject` → descriptor with the new Kind. NEW pinned description constant `graphFollowSubjectDescription`. |
| Regression test | `internal/database/repositories/filter_unit_test.go` | NEW `TestStringSubjectFilter_IndexExpressionMatchesMigration029` mirroring the existing contributor-index test. Same paren-balanced extraction pattern. |
| Filter unit tests | `internal/database/repositories/filter_unit_test.go` | NEW `TestBuildSingleFilter_StringSubject_Eq` and `…_In` (and `…_UnsupportedOperator`) mirroring the existing contributor tests. |
| Behavioral test catalogue | `docs/behavioral-tests.md` | NEW entry E9 — follow subject filter, mirroring E7/E8. Catalogue table row added. |

No code in `internal/graphql/schema/builder.go` or `where.go`'s
extractor changes — the registry hook (point 5 above) is the
only place that needs editing thanks to the design from #65.

## 4. Detail per layer

### 4.1 Lexicon

Fetched from `hypercerts-org/hypercerts-lexicon@feature/add-graph-follow-lexicon`:

```json
{
  "lexicon": 1,
  "id": "app.certified.graph.follow",
  "defs": {
    "main": {
      "type": "record",
      "key": "tid",
      "record": {
        "type": "object",
        "required": ["subject", "createdAt"],
        "properties": {
          "subject":   { "type": "string", "format": "did" },
          "createdAt": { "type": "string", "format": "datetime" },
          "via":       { "type": "ref", "ref": "com.atproto.repo.strongRef" }
        }
      }
    }
  }
}
```

Staged in `testdata/lexicons/app/certified/graph/follow.json`.
Once the upstream PR merges, the canonical version takes
precedence on production; the testdata copy is for local builds
and CI.

### 4.2 Migration 029

```sql
-- no-transaction
-- Partial expression index on `json->>'subject'` for the
-- KindStringSubject filter on app.certified.graph.follow. Pairs
-- with the runtime SQL `r.json->>'subject' = $N` /
-- `= ANY($N::text[])`. Without this index, a query for "who
-- follows did:plc:X" degrades to a sequential scan over the
-- follow collection that trips the 5s /graphql budget at scale.
--
-- Partial — scoped to follow records only. The collection
-- predicate is matched against the resolver's parameter-bound
-- `r.collection = $coll` via Postgres's partial-index implication
-- (same mechanism that powers idx_record_subject_did on
-- badge.award, migration 026).
--
-- NOTE: an earlier draft included `AND jsonb_typeof(json->'subject')
-- = 'string'` here as belt-and-suspenders. Removed in plan-review
-- round 1 (item R1.1) because Postgres's structural
-- `predicate_implied_by` can't prove `r.json->>'subject' = $N`
-- implies that typeof predicate — adding it would create an index
-- the planner refuses to use. The lexicon requires `subject` to be
-- a string, so non-string rows shouldn't exist; if they do, they
-- index as NULL (btree stores compactly).
--
-- IMMUTABLE: `->>` on jsonb is IMMUTABLE in Postgres, so the
-- expression is safe in an index.
--
-- CONCURRENTLY: no exclusive lock on the table, but must run
-- outside a transaction. `-- no-transaction` opts in.
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_record_follow_subject
    ON record ((json->>'subject'))
    WHERE collection = 'app.certified.graph.follow';
```

Down:

```sql
-- no-transaction
DROP INDEX CONCURRENTLY IF EXISTS idx_record_follow_subject;
```

### 4.3 Filter SQL — `KindStringSubject`

New enum value in `internal/database/repositories/filter.go`:

```go
// KindStringSubject signals the indexable bare-DID-subject
// filter on per-collection resolvers whose `subject` field is
// a plain string (lexicon format: did). Currently used by
// app.certified.graph.follow. Backed by the partial expression
// index `idx_record_follow_subject` in migration 029.
//
// The runtime SQL is `r.json->>'subject' = $N` /
// `r.json->>'subject' = ANY($N::text[])`. The expression must
// match the migration's `(json->>'subject')` byte-for-byte
// (modulo the `r.` alias) for the planner to pick the partial
// index. The regression test
// TestStringSubjectFilter_IndexExpressionMatchesMigration029 in
// filter_unit_test.go guards this coupling.
KindStringSubject
```

New builder, modeled on `buildBadgeAwardSubjectFilter`:

```go
func buildStringSubjectFilter(f FieldFilter, paramIdx int) (string, []interface{}, int, error) {
    // indexedExpr MUST match the index expression in migration
    // 029 byte-for-byte (modulo the `r.` alias). A drift here
    // silently degrades the filter to a sequential scan.
    const indexedExpr = `r.json->>'subject'`
    switch f.Operator {
    case OpEq:
        param := fmt.Sprintf("$%d", paramIdx)
        return fmt.Sprintf("%s = %s", indexedExpr, param), []interface{}{f.Value}, paramIdx + 1, nil
    case OpIn:
        param := fmt.Sprintf("$%d", paramIdx)
        values, _ := extractInValues(f.Value)
        return fmt.Sprintf("%s = ANY(%s::text[])", indexedExpr, param), []interface{}{values}, paramIdx + 1, nil
    default:
        return "", nil, paramIdx, fmt.Errorf("operator %s not supported on string-subject filter", f.Operator)
    }
}
```

Dispatch in `buildSingleFilter`:

```go
case KindStringSubject:
    return buildStringSubjectFilter(f, paramIdx)
```

### 4.4 Schema registry

New constant + registry entry in
`internal/graphql/schema/where.go`:

```go
// graphFollowSubjectDescription is pinned verbatim so consumers
// see the policy at schema introspection.
const graphFollowSubjectDescription = `Filter follows by the subject DID — the account being followed. Use this to assemble a followers list: WHERE subject = <did> returns every follow record pointing at that DID. The follower is the record author (filter via the did field, or read from node.did). DIDs only — handle values are rejected at the GraphQL layer. Compose with the did filter via _or to express "I follow OR am followed by": where: { _or: [ { did: { eq: "did:plc:me" } }, { subject: { eq: "did:plc:me" } } ] }.`

var filterRegistry = map[string]map[string]filterDescriptor{
    // ... existing entries ...
    "app.certified.graph.follow": {
        "subject": {
            Kind:        repositories.KindStringSubject,
            FieldName:   "subject",
            Description: graphFollowSubjectDescription,
        },
    },
}
```

### 4.5 Regression + pin tests

New tests in `internal/database/repositories/filter_unit_test.go`,
modeled on `TestContributorFilter_IndexExpressionMatchesMigration024`:

- **`TestStringSubjectFilter_IndexExpressionMatchesMigration029`** —
  reads the migration file, extracts the expression inside the
  outer `((...))` after `ON record`, compares against
  `buildStringSubjectFilter`'s emitted clause (normalised by
  stripping the `r.` alias). Fails loudly with a "update BOTH
  the migration AND `buildStringSubjectFilter`" message if they
  drift.

  **Helper note (R1.2 plan-review correction)**: the existing
  `extractGinExpression` helper hard-codes `USING\s+gin\s*\(`
  and won't match a btree expression index whose syntax is
  `ON record ((expr))` (double parens, no `USING` clause). Add
  a sibling `extractBtreeExpression` that anchors on
  `ON\s+record\s*\(\(` and paren-balance extracts the inner
  expression.

- **Unit tests** for `buildStringSubjectFilter`:
  - `TestBuildSingleFilter_StringSubject_Eq` — `OpEq` emits
    `r.json->>'subject' = $1`.
  - `TestBuildSingleFilter_StringSubject_In` — `OpIn` emits
    `r.json->>'subject' = ANY($1::text[])`.
  - `TestBuildSingleFilter_StringSubject_UnsupportedOperator` —
    `OpNeq`, `OpGt`, `OpLt`, `OpGte`, `OpLte`, `OpContains`,
    `OpStartsWith` return errors.

New test in `internal/graphql/schema/where_test.go`
(R2.3 plan-review addition):

- **`TestFilterRegistry_GraphFollowSubject`** — mirrors
  `TestFilterRegistry_BadgeAwardSubject`. Asserts that the
  `app.certified.graph.follow` entry exists, that the field name
  is `subject`, that the descriptor's `Kind` is
  `repositories.KindStringSubject`, and that the description
  matches the pinned constant. Pinning the schema-side
  registration prevents accidental removal.

### 4.6 Behavioral test catalogue

Add entry E9 to `docs/behavioral-tests.md` mirroring E7/E8 —
follow subject filter, wire-level half (EITHER) and an
EXPLAIN-ANALYZE half (LOCAL). Add a row to the catalogue table.

## 5. Acceptance criteria

1. `GOARCH=arm64 go build ./...` clean.
2. `GOARCH=arm64 go vet ./...` clean.
3. `GOARCH=arm64 golangci-lint run ./...` returns `0 issues.`
4. `CGO_ENABLED=1 GOARCH=arm64 go test -race -short ./internal/database/...` passes — includes the new regression test and the unit tests.
5. New migration files exist and are syntactically valid (no `psql -f` needed; the existing migrations-package test loads them and validates structure).
6. Schema introspection on a service with the follow lexicon registered shows `AppCertifiedGraphFollowWhereInput.subject` field of type `DIDFilterInput`.
7. Querying `appCertifiedGraphFollow(where: { subject: { eq: "did:plc:..." } }, first: 5)` returns records (or empty) without error.
8. (LOCAL only) `EXPLAIN ANALYZE` of the equivalent SQL shows
   `Index Scan using idx_record_follow_subject`.

## 6. Alternatives considered

| Alternative | Why not |
|---|---|
| Reuse `record.subject_did` (badge.award column) | Forces an ALTER TABLE column-expression change — not directly supported in Postgres; would require DROP + ADD which is a table rewrite. Bad on production. |
| Add a new STORED generated column `follow_subject_did` | Triggers a table rewrite to populate existing rows. Same operator pain. Unnecessary for a bare-string extraction. |
| GIN expression index instead of btree | btree is sufficient for `=` and `= ANY(...)` against a single text value. GIN buys nothing here and costs more index maintenance per write. |
| Add the filter to the polymorphic `records` resolver | Records is a polymorphic resolver — its `where` doesn't take per-collection fields. Per-collection auto-generated resolvers are the right surface. |
| New, narrower `FilterKind` like `KindGraphFollowSubject` | Equivalent to `KindStringSubject` in implementation but tied to one lexicon. `KindStringSubject` reads as "any bare-string subject filter" and is reusable when the next lexicon with the same shape lands — small generality buys real future-proofing. |

## 7. Rollback

- Migration 029 is `IF NOT EXISTS` on up and `IF EXISTS` on down. Drop is `DROP INDEX CONCURRENTLY`. Safe to apply and revert at any time.
- Filter and registry changes are pure Go diffs; `git revert` restores the prior state.
- Lexicon file in `testdata/` is data only. Removing it leaves the auto-generated resolver gone from the next schema rebuild.

## 8. Out of scope

- Backfilling existing PDS follows into the indexer (the firehose will populate organically as new follows happen). If a bulk-backfill is needed, that's a separate operation outside this PR.
- The `via` strongRef field on nodes — auto-generated by the schema builder from the lexicon; no special wiring needed. The issue calls it nice-to-have; it works for free.
- `totalCount` — already auto-generated on the standard connection shape; no work.
- Any client-side certified-app changes. The issue notes the
  client (`src/hooks/use-followers.ts`) already targets the
  expected query and degrades gracefully until this ships.
- Notifications (e.g. "X started following you"). Separate
  feature, separate PR.

## 9. Open questions for the operator

1. **Lexicon coordination**: the upstream `hypercerts-lexicon`
   PR (`feature/add-graph-follow-lexicon`) needs to land before
   the production indexer can register the lexicon via the admin
   upload path. The testdata copy in this PR is fine for local
   tests and CI but the production deploy will need either (a)
   the upstream PR merged and a fresh upload, or (b) the
   testdata file used as the upload payload directly. Operator
   call on timing.

2. **Filter kind naming**: I chose `KindStringSubject`
   (generalisable to any bare-DID-subject filter). Alternative:
   `KindGraphFollowSubject` (lexicon-specific). The general name
   is forward-looking; pick if a near-term reuse seems
   unlikely we'll regret either way.

## 9b. Operator note — `CREATE INDEX CONCURRENTLY IF NOT EXISTS` foot-gun

(Pre-existing across migrations 024 / 026 / 028 — surfacing in
plan-review round 1 for completeness, R1.3.)

If a `CREATE INDEX CONCURRENTLY` build fails partway through
(e.g. operator interrupt, hard timeout, broken constraint), the
index lands in `INVALID` state. The `IF NOT EXISTS` clause then
*skips* re-creation on the next migration run, leaving production
silently un-indexed and the planner falling back to seq scans.

After deploying migration 029, verify the index is valid:

```sql
SELECT indexrelid::regclass
FROM pg_index
WHERE NOT indisvalid;
```

If `idx_record_follow_subject` shows up there, recover with:

```sql
REINDEX INDEX CONCURRENTLY idx_record_follow_subject;
-- or, if that fails:
DROP INDEX CONCURRENTLY IF EXISTS idx_record_follow_subject;
-- then re-run the migration.
```

## 10. Sequencing

Commit order on `staging`:

1. Lexicon: `testdata/lexicons/app/certified/graph/follow.json`.
2. Migration 029 (up + down).
3. Filter SQL: `KindStringSubject` + `buildStringSubjectFilter` + dispatch arm + unit tests + regression test.
4. Schema registry: filterRegistry entry + pinned description.
5. Behavioral test catalogue entry E9.

Each commit builds and tests clean on its own. After all land,
Draft PR `staging → main` referencing issue #86.
