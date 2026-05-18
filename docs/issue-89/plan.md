# Implementation plan — issue #89

> Add an `awardCount` field to `AppCertifiedBadgeDefinitionRecord`
> returning the number of `app.certified.badge.award` records whose
> `badge.uri` equals the definition's URI. Unblocks the certified-app's
> Lists section reading the master view in one indexer round-trip
> instead of two PDS calls.

## 1. Goal & scope

The certified-app's `use-endorsement-lists.ts` hook today does two PDS
round-trips: pull every definition on the repo, then pull every award
on the repo, then client-side group awards by `value.badge.uri` to
attach a count to each list row. The master view only needs the
**count** per list, not the full award rows — the second PDS call is
wasted work that scales with the size of the repo's awards collection.

Shape A from issue #89 ships a per-definition `awardCount` field
returning the count of awards strong-ref'ing that definition. One
round-trip for the entire Lists section.

### Shipping looks like

1. Schema introspection on `AppCertifiedBadgeDefinitionRecord` shows
   a new `awardCount: Int!` field.
2. The issue's example query returns each definition's awardCount in
   one round-trip; matches the client-side aggregate that
   `use-endorsement-lists.ts` builds today.
3. `EXPLAIN ANALYZE` on the awardCount resolver SQL shows a single
   `Index Scan` on a new partial expression index — sub-millisecond
   per definition.

### Non-goals

- **Shape B** (separate `appCertifiedBadgeAwardCountByBadge`
  aggregation query). Less ergonomic, more API surface, no use case
  asks for it today.
- **Dataloader / batched COUNT** across a page of definitions. v1 is
  N+1 (one COUNT per definition). At a bounded page size (typically
  ≤100 definitions) + indexed lookup, the cost is ≤100ms total. The
  dataloader path is documented as a follow-up.
- **awardCount field on other record types.** The field is
  registry-opt-in per (lexicon, fieldName); v1 wires only
  `app.certified.badge.definition.awardCount`.
- **Generic "joined-count" mechanism** the way #87 added
  `joinedWhereRegistry`. This is a derived-field add, not a
  filter-input add — different surface, different machinery.
- **Counts filtered by inner where.** v1 awardCount is unconditional
  (all awards pointing at this definition). A future
  `awardCount(where: AppCertifiedBadgeAwardWhereInput)` shape
  composes cleanly with the #87/#88 input types but adds review
  surface; document as follow-up §9.3.
- **Schema-level result caching.** Per-request only; no
  cross-request memoisation. (Resolver fires once per request per
  definition row.) Dataloader work in §9.1 also stays per-request.
- **Admin-API toggle to enable/disable per-DID.** Registry is
  global per-lexicon; no per-DID gating of derived fields. If a
  consumer needs to disable awardCount for a specific DID's
  records, they filter client-side.

## 2. Decided shape

**Registry-driven synthetic record field + repository COUNT query +
new partial expression index.**

### 2.1 Field placement

The field lives on the per-collection record GraphQL type
(`AppCertifiedBadgeDefinitionRecord`), not as a top-level query. The
client's natural read path is `appCertifiedBadgeDefinition(where:
{...}) { edges { node { uri title awardCount } } }` — selecting
awardCount as one column of the node.

**This is a NEW pattern** (R1.4): the codebase today has NO
Resolve-bearing field on per-collection record types. The only
precedent — `labels` at `internal/graphql/schema/builder.go:243-263`
— lives on `genericRecord` (the event-payload type), not on
per-collection records. The per-collection `labels` field at
`internal/graphql/types/object.go:153-156` has NO Resolve; its
value is pre-baked into the node map at `builder.go:719`
(`nodeMap["labels"] = labelsByURI[rec.URI]`).

graphql-go honours a field's `Resolve` when present (it overrides
the default map-key lookup), so the new pattern works
mechanically. But the impl reviewer should explicitly audit the
resolver-invocation contract: `p.Source` for a per-collection
record field is the node map populated at `builder.go:952` (where
`sanitized["uri"] = rec.URI` is set on every row), so the URI is
always present.

### 2.2 Resolver strategy

Per-row COUNT subquery, no batching for v1:

```sql
SELECT COUNT(*)
FROM record
WHERE collection = 'app.certified.badge.award'
  AND json->'badge'->>'uri' = $1
```

The resolver pulls `p.Source.(map[string]interface{})["uri"]` to get
the definition's URI and calls a new
`RecordsRepository.CountAwardsByBadgeURI(ctx, uri)`. N+1 in the
limit, but bounded by GraphQL page size and made cheap by the
expression index from §2.3.

### 2.3 Index

New migration 030: a partial btree expression index on
`(json->'badge'->>'uri') WHERE collection = 'app.certified.badge.award'`.
Same shape as migration 029 (follow.subject) — bare expression, no
typeof predicate (per #86 R1.1, those break `predicate_implied_by`).

The index is partial on collection rather than full-table because
only one collection's badge.uri lookups matter. Smaller index, no
write amplification on other collections.

### 2.4 Registry pattern

Parallel to `joinedWhereRegistry` (#87) and `arrayWhereRegistry`
(#88): a new `derivedFieldRegistry` that maps `lexicon ID → fieldName
→ descriptor`. The descriptor carries the GraphQL field config + a
factory function returning the resolver. The record-type builder
injects these fields after the lexicon-derived ones.

Why registry rather than hardcode in `buildRecordFields`: same
reasons as #87/#88 — pinned description, explicit opt-in, easy to
add more later without growing the builder.

## 3. Surface area

| Layer | File | Change | LOC (rough) |
|---|---|---|---|
| Migration | `internal/database/migrations/postgres/030_add_award_badge_uri_index.up.sql` + `.down.sql` | New partial btree expression index on `(json->'badge'->>'uri')` filtered by collection. CONCURRENTLY for online safety. | ~30 |
| Repository | `internal/database/repositories/records.go` | New `CountAwardsByBadgeURI(ctx context.Context, badgeURI string) (int64, error)` — sibling to `CountByDID` (line 741). | ~25 |
| Repository test | `internal/database/repositories/records_test.go` (or new file) | TestCountAwardsByBadgeURI — seeds award rows, asserts count matches. | ~70 |
| Registry | `internal/graphql/schema/derived_fields.go` (NEW) | `derivedFieldDescriptor` struct + `derivedFieldRegistry` map + `lookupDerivedFields` helper. First entry: `app.certified.badge.definition.awardCount` with factory producing the per-row COUNT resolver. | ~120 |
| Schema builder | `internal/graphql/types/object.go` (`buildRecordFields` + new `NewObjectBuilderWithDerivedFields` ctor) | New post-loop hook that iterates the derived-fields map and appends fields. To keep the existing `NewObjectBuilder` signature unchanged (5 test sites in `types_test.go`), add a backward-compat ctor `NewObjectBuilderWithDerivedFields(mapper, derivedFieldsByLexicon)` that delegates after setting the new field; `NewObjectBuilder` stays as a thin shim calling the new ctor with `nil`. | ~40 |
| Schema tests | `internal/graphql/schema/derived_fields_test.go` (NEW) | Registry pin + resolver invocation test (with mock repository). | ~150 |
| Behavioral catalogue | `docs/behavioral-tests.md` | New entry E12 covering the wire-level + EXPLAIN ANALYZE halves. | ~80 |
| Drift-detection test | `internal/database/repositories/records_test.go` | `TestCountAwardsByBadgeURI_IndexExpressionMatchesMigration030` — reads migration file and asserts byte-for-byte match with the runtime SQL. Pattern from #86's `TestStringSubjectFilter_IndexExpressionMatchesMigration029`. | ~50 |

Total: ~555 LOC across 6 files (tests account for ~320).
Directional estimate; R2.1 flagged this is likely 30-40% under,
realistic outturn ~700-750 LOC.

## 4. Migration

### 030_add_award_badge_uri_index.up.sql

```sql
-- no-transaction
--
-- Partial btree expression index on (json->'badge'->>'uri') for
-- app.certified.badge.award records. Backs the awardCount derived
-- field on AppCertifiedBadgeDefinitionRecord (issue #89), which
-- needs fast equality lookup of "every award pointing at this
-- definition URI."
--
-- The index is partial on collection (not full-table) because
-- only the badge.award collection uses this JSON path. Smaller
-- footprint, no write amplification on unrelated record writes.
--
-- Bare expression match — no jsonb_typeof predicate. Postgres'
-- predicate_implied_by is structural, not value-based, so adding
-- AND jsonb_typeof(json->'badge') = 'object' would make the
-- runtime r.json->'badge'->>'uri' = $1 query miss the index. See
-- docs/issue-86/review-round-1.md R1.1 for the precedent.
--
-- CONCURRENTLY for online safety on the production-sized record
-- table. IF NOT EXISTS guards re-runs but does NOT recover from
-- an INVALID partial build — manual DROP and re-run is the
-- recovery procedure (follow-up §9.5 adds a RUNBOOK section).
--
-- The `-- no-transaction` directive on line 1 tells the migration
-- runner to skip its BEGIN/COMMIT wrapper. Without it pgx
-- rejects CREATE INDEX CONCURRENTLY with SQLSTATE 25001
-- ("CREATE INDEX CONCURRENTLY cannot run inside a transaction
-- block"). See migration 029's file-header comment for the
-- precedent.
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_record_award_badge_uri
ON record ((json->'badge'->>'uri'))
WHERE collection = 'app.certified.badge.award';
```

### 030_add_award_badge_uri_index.down.sql

```sql
DROP INDEX CONCURRENTLY IF EXISTS idx_record_award_badge_uri;
```

## 5. Repository

```go
// CountAwardsByBadgeURI returns the number of
// app.certified.badge.award records whose `badge.uri` strongRef
// equals the given URI. Backs the awardCount derived field on
// AppCertifiedBadgeDefinitionRecord (issue #89).
//
// The query uses a partial expression index from migration 030
// — byte-for-byte expression match is load-bearing, pinned by
// TestCountAwardsByBadgeURI_IndexExpressionMatchesMigration030.
//
// Empty / unknown URIs return (0, nil) — not an error. The
// caller (the awardCount resolver) treats missing source URI
// the same as zero awards.
func (r *RecordsRepository) CountAwardsByBadgeURI(ctx context.Context, badgeURI string) (int64, error) {
    if badgeURI == "" {
        return 0, nil
    }
    var count int64
    err := r.db.QueryRow(ctx,
        `SELECT COUNT(*) FROM record
         WHERE collection = $1
           AND json->'badge'->>'uri' = $2`,
        []database.Value{
            database.Text("app.certified.badge.award"),
            database.Text(badgeURI),
        },
        &count,
    )
    if err != nil {
        return 0, fmt.Errorf("count awards by badge URI: %w", err)
    }
    return count, nil
}
```

**Signature note (R1.1)**: the `[]database.Value` second arg +
variadic `dest ...any` shape comes from
`internal/database/executor.go:96` (`QueryRow(ctx, sql,
[]Value, dest...)`). Sibling `CountByDID` at
`records.go:741-746` is the literal template.

## 6. Registry + resolver

```go
// internal/graphql/schema/derived_fields.go

package schema

import (
    "context"
    "log/slog"

    "github.com/graphql-go/graphql"

    "github.com/GainForest/hypergoat/internal/database/repositories"
    "github.com/GainForest/hypergoat/internal/graphql/resolver"
)

// derivedFieldDescriptor describes a synthetic field that lives on a
// per-collection record GraphQL type but is computed by a Resolve
// function rather than read from the record's JSON. Examples: a
// joined-collection count (this file's first entry), a derived
// aggregate, a related-records list.
//
// SECURITY: derived field resolvers run with the request context's
// repositories handle. The Resolve func is registry-defined and
// must NEVER source SQL fragments from request data — keep all SQL
// inside the repositories package.
type derivedFieldDescriptor struct {
    FieldName   string
    Field       *graphql.Field // type + description; Resolve set per-entry below
}

// awardCountDescription is pinned verbatim so consumers see the
// policy at schema introspection.
const awardCountDescription = `Number of app.certified.badge.award records whose badge strongRef points at this definition. Independent of the award subject (returns the total count of awards strong-ref'ing this definition across all subjects + DIDs). For the certified-app's Lists section: this collapses the master-view aggregate (definitions + per-list count) to a single indexer query. The count uses a partial expression index on (json->'badge'->>'uri') filtered by collection, so per-row cost is sub-millisecond. Filtered counts (e.g. by issuer or by award properties) are not yet exposed.`

// derivedFieldRegistry maps lexiconID → fieldName → descriptor.
// First entry (issue #89): app.certified.badge.definition.awardCount,
// returning the count of awards strong-ref'ing the definition.
var derivedFieldRegistry = map[string]map[string]derivedFieldDescriptor{
    "app.certified.badge.definition": {
        "awardCount": {
            FieldName: "awardCount",
            Field: &graphql.Field{
                Type:        graphql.NewNonNull(graphql.Int),
                Description: awardCountDescription,
                Resolve:     resolveAwardCount,
            },
        },
    },
}

func resolveAwardCount(p graphql.ResolveParams) (interface{}, error) {
    src, ok := p.Source.(map[string]interface{})
    if !ok {
        return 0, nil
    }
    uri, _ := src["uri"].(string)
    if uri == "" {
        return 0, nil
    }
    repos := resolver.GetRepositories(p.Context)
    if repos == nil {
        // Mirrors the labels resolver pattern at builder.go:255-258.
        slog.WarnContext(p.Context, "awardCount: repositories unavailable in context")
        return 0, nil
    }
    return repos.Records.CountAwardsByBadgeURI(p.Context, uri)
}

func lookupDerivedFields(lexiconID string) map[string]derivedFieldDescriptor {
    return derivedFieldRegistry[lexiconID]
}

// MustNotReserveField asserts at init() time that a derived
// field name does not collide with one of the reserved record
// metadata fields (uri, cid, did, rkey, labels, pds). Called
// automatically from the package init() below — startup-fail
// mode, surfaces immediately on `go build`-then-run rather than
// at first query (R2.4).
func MustNotReserveField(lexiconID, fieldName string) {
    if types.ReservedRecordFields[fieldName] {
        panic(fmt.Sprintf("derived field %q on %q collides with reserved record field — rename in derivedFieldRegistry", fieldName, lexiconID))
    }
}

func init() {
    for lexID, fields := range derivedFieldRegistry {
        for fieldName := range fields {
            MustNotReserveField(lexID, fieldName)
        }
    }
}

// Import block (R1.6): the snippet above needs `fmt` for the
// panic message and `internal/graphql/types` for
// ReservedRecordFields; both are added at code-time.
```

## 7. Schema builder integration

`internal/graphql/types/object.go` `buildRecordFields` is the natural
injection point. The schema package owns `derivedFieldRegistry`; the
types package owns the field builder. Cleanest option: expose a
hook from the schema package that `ObjectBuilder` can call. The
existing types package has no schema-layer imports today; keep it
that way by passing the derived-field map in via `ObjectBuilder`
config.

**Backward-compat ctor (R1.5)**: keep `NewObjectBuilder(mapper)`
unchanged so the 5 test sites in `types_test.go` (line 184, 247,
366, 383, 401) stay green. Add a sibling
`NewObjectBuilderWithDerivedFields(mapper, derivedFieldsByLexicon)`
for the schema-builder caller:

```go
// internal/graphql/types/object.go

type ObjectBuilder struct {
    mapper *Mapper
    // derivedFieldsByLexicon is a per-lexicon-ID map of synthetic
    // record-level fields (e.g. awardCount) injected by the schema
    // package. Nil-safe — empty map is fine.
    derivedFieldsByLexicon map[string]map[string]*graphql.Field
}

// NewObjectBuilder is the legacy ctor — no derived fields. Existing
// callers (5 test sites at types_test.go) continue to work
// unchanged.
func NewObjectBuilder(mapper *Mapper) *ObjectBuilder {
    return NewObjectBuilderWithDerivedFields(mapper, nil)
}

// NewObjectBuilderWithDerivedFields constructs a builder that
// injects the given per-lexicon synthetic fields into each
// per-collection record type, after the lexicon-property loop.
// The schema-package Builder uses this ctor with the registry-
// derived map.
func NewObjectBuilderWithDerivedFields(mapper *Mapper, derivedFieldsByLexicon map[string]map[string]*graphql.Field) *ObjectBuilder {
    return &ObjectBuilder{
        mapper: mapper,
        derivedFieldsByLexicon: derivedFieldsByLexicon,
    }
}
```

Then in `buildRecordFields` after the lexicon-property loop:

```go
for _, entry := range def.Properties { /* existing */ }

for fieldName, field := range b.derivedFieldsByLexicon[lexiconID] {
    if _, collide := fields[fieldName]; collide {
        slog.Warn("Derived field collides with lexicon property — keeping lexicon property",
            "lexicon", lexiconID, "field", fieldName)
        continue
    }
    fields[fieldName] = field
}

return fields
```

The schema package's `Builder` flattens `derivedFieldRegistry` into
the `map[lexiconID]map[fieldName]*graphql.Field` shape at construction
time and passes to `NewObjectBuilder`. Schema package owns the
registry; types package consumes a generic field map.

## 8. Tests

| Test | What it pins |
|---|---|
| **E12.1** `TestCountAwardsByBadgeURI` | Seeds N awards with the same badge URI + M awards with a different URI, asserts CountAwardsByBadgeURI returns N. |
| **E12.2** `TestCountAwardsByBadgeURI_EmptyURI` | Empty URI returns (0, nil). |
| **E12.3** `TestCountAwardsByBadgeURI_NoMatches` | URI with zero matching awards returns (0, nil). |
| **E12.4** `TestCountAwardsByBadgeURI_IndexExpressionMatchesMigration030` | Reads migration 030's `.up.sql`, extracts the index expression, asserts byte-for-byte match with the runtime query's `WHERE` clause. Pattern from `TestStringSubjectFilter_IndexExpressionMatchesMigration029`. Mutating either the migration or the repository SQL fails the test with "update BOTH the migration AND CountAwardsByBadgeURI." |
| **E12.S1** `TestDerivedFieldRegistry_BadgeDefinitionAwardCount` | Pin registry shape: field name `awardCount`, type `Int!`. **Description drift pin (R2.3)**: assert `descriptor.Field.Description == awardCountDescription` byte-for-byte (mirrors `TestJoinedWhereRegistry_BadgeAwardBadge` at where_test.go:266-268). |
| **E12.S2** `TestResolveAwardCount_DelegatesToRepository` | Mock repository, assert the resolver pulls `uri` from `p.Source`, calls `CountAwardsByBadgeURI`, returns the int. |
| **E12.S3** `TestResolveAwardCount_EmptyURI` | Source without `uri` returns 0 without calling repository. |
| **E12.S4** `TestResolveAwardCount_NoRepositoriesInContext` | Context without repositories returns 0 (mirrors labels resolver pattern). |
| **E12.S5** `TestBuildRecordFields_BadgeDefinitionHasAwardCount` | Schema-introspection-style: `AppCertifiedBadgeDefinitionRecord.awardCount` field exists, typed `Int!`, has pinned description. |
| **E12.S6** `TestBuildRecordFields_DerivedFieldCollisionWithLexicon` | If a lexicon adds a property that collides with a registered derived field, lexicon wins (logs warn). Pin via a synthetic test registry entry. |
| **E12.S7** `TestResolveAwardCount_FiresOncePerRow` | Mock repository with a call counter; issue a connection query returning N definitions; assert `CountAwardsByBadgeURI` was called exactly N times (one per row). Baseline for the dataloader-follow-up §9.1 — when the dataloader lands, this test flips to assert a single batched call. |

## 8.5 Behavioral catalogue entry E12 (paste-ready)

Add to the table at `docs/behavioral-tests.md` (after E11 around
line 108):

```markdown
| [E12](#e12) | BadgeDefinition `awardCount` returns per-list count | EITHER | issue #89 |
```

Detail section:

```markdown
### E12
**BadgeDefinition awardCount returns per-list count in one
round-trip.**

- **Coverage**: issue #89 — the certified-app's Lists section
  reads the master view via
  `appCertifiedBadgeDefinition(where: { did: { eq: $did },
  badgeType: { eq: "endorsement" } }) { edges { node { uri title
  awardCount } } }`. The `awardCount` field returns the count of
  awards whose `badge.uri` strongRef points at this definition.
  Without this the client makes a second PDS round-trip to
  listAwards(did) and groups client-side.
- **Target**: EITHER for the wire-level query; LOCAL for the
  EXPLAIN ANALYZE check.
- **Preconditions**:
  - `app.certified.badge.definition` and
    `app.certified.badge.award` lexicons registered (already
    present on DEV-DEPLOYED post-#65/#87).
  - At least one definition with badgeType="endorsement" with
    at least one award pointing at it.
- **Steps**:
  1. Schema introspection:
     ```bash
     curl -s -X POST $BASE_URL/graphql -H 'Content-Type: application/json' \
       -d '{"query":"{ __type(name:\"AppCertifiedBadgeDefinitionRecord\") { fields { name type { name kind ofType { name } } } } }"}' \
       | jq '.data.__type.fields | map(select(.name == "awardCount"))'
     ```
     Expect a single entry with type kind `NON_NULL` wrapping
     name `Int`.
  2. Wire-level query:
     ```bash
     curl -s -X POST $BASE_URL/graphql -H 'Content-Type: application/json' \
       -d '{"query":"{ appCertifiedBadgeDefinition(where: { did: { eq: \"did:plc:...\" }, badgeType: { eq: \"endorsement\" } }, first: 100) { edges { node { uri title awardCount } } pageInfo { hasNextPage } } }"}' \
       | jq
     ```
  3. Cross-check: sum `awardCount` across the returned page;
     compare to
     `appCertifiedBadgeAward(where: { did: { eq: <did> } }) {
     totalCount }`. The sum should equal the totalCount minus
     any awards pointing at definitions outside the
     badgeType="endorsement" set.
  4. (LOCAL only) EXPLAIN ANALYZE the per-definition COUNT:
     ```sql
     EXPLAIN ANALYZE
     SELECT COUNT(*) FROM record
     WHERE collection = 'app.certified.badge.award'
       AND json->'badge'->>'uri' = 'at://did:plc:.../app.certified.badge.definition/...';
     ```
- **Expected**:
  - Step 1: `awardCount` field present, non-null Int.
  - Step 2: each definition's `awardCount` is a non-negative
    integer; no GraphQL errors.
  - Step 3: sums match within the badgeType filter scope.
  - Step 4: plan shows `Index Scan using
    idx_record_award_badge_uri on record`. No sequential scan
    over the award collection.
- **N+1 note**: the resolver fires once per definition row in
  the page. Bounded cost (≤100 rows × <1ms indexed COUNT each).
  Dataloader batching is a documented follow-up
  (docs/issue-89/plan.md §9.1).
- **Cleanup**: none.
- **Refs**: issue #89; resolver at
  `internal/graphql/schema/derived_fields.go`
  (`derivedFieldRegistry` + `resolveAwardCount`); repository
  helper at `internal/database/repositories/records.go`
  (`CountAwardsByBadgeURI`); migration
  `internal/database/migrations/postgres/030_add_award_badge_uri_index.up.sql`.
```

## 9. Known follow-ups

| # | Follow-up | Trigger to act |
|---|---|---|
| 9.1 | **Dataloader / batched COUNT.** Replace N+1 with `SELECT json->'badge'->>'uri', COUNT(*) FROM record WHERE collection='app.certified.badge.award' AND json->'badge'->>'uri' = ANY($uris) GROUP BY json->'badge'->>'uri'`, called once per page of definitions. | First page-size complaint (>100 definitions) OR measured slow-tab telemetry. |
| 9.2 | **awardCount on the connection envelope** (not just the node). Return `totalAwardCount` summed across all matched definitions for the "lists totals" use case. | A consumer requests it. |
| 9.3 | **Filtered awardCount** — `awardCount(where: AppCertifiedBadgeAwardWhereInput): Int!`. Compose with the existing AppCertifiedBadgeAwardWhereInput so the certified-app can count "awards from this DID pointing at this definition." Adds a filterable Args block to the derived field. | A consumer requests it. |
| 9.4 | **Generic "joinedCountRegistry" pattern.** Once a second derived count field lands (e.g. `endorsementCount` on actor profiles), abstract the factory shape. v1 ships the one entry inline. | Second registry entry lands. |
| 9.5 | **RUNBOOK section: "Recovering from an INVALID CONCURRENTLY index."** Migration 030 (and earlier 029, 026) use `CREATE INDEX CONCURRENTLY IF NOT EXISTS`. A failed build leaves an INVALID partial index that IF NOT EXISTS then SKIPS re-creating, so the next `migrate up` is a no-op and the index stays unusable. Recovery: `DROP INDEX CONCURRENTLY <name>;` then re-run the migration. Document once in RUNBOOK so future migrations don't repeat the comment block. | Next time an operator hits the INVALID-index state, OR proactively before #89 hits production. |

## 10. Rollback

- **Migration 030 is reversible** via the `.down.sql` (`DROP INDEX
  CONCURRENTLY IF EXISTS`). Reverting the PR + running `migrate down`
  one step drops the index cleanly.
- **Per-commit bisectability**: the migration + repository commit
  introduces the COUNT helper but no field producer; the schema commit
  wires the registry + builder injection. A `git revert` of either
  individual commit leaves the tree compiling. Reverting the schema
  commit alone leaves the repository helper and migration index
  unused (harmless). Reverting the migration commit alone leaves the
  repository helper pointed at a non-existent index — sub-optimal
  perf but still correct.
- **Single revert drops everything**: revert the merge commit, run
  `migrate down 1`, redeploy.

## 11. Sequencing

1. **Plan + plan-review** docs (`docs/issue-89/plan.md`,
   `docs/issue-89/review-round-1.md` after reviewers run).
2. **Migration 030 + repository helper + tests** in one commit —
   `feat(filter): CountAwardsByBadgeURI + partial expression index`.
3. **Registry + resolver + builder hook + tests** in one commit —
   `feat(schema): derivedFieldRegistry + awardCount on badge.definition`.
4. **Behavioral test catalogue entry E12** —
   `docs/behavioral-tests.md`.
5. **Impl-review round 2 doc + Draft PR.** Stop here; user merges.

## 12. Acceptance criteria

1. `GOARCH=arm64 go build ./...` clean.
2. `GOARCH=arm64 go vet ./...` clean.
3. `GOARCH=arm64 golangci-lint run ./...` returns 0 issues.
4. `CGO_ENABLED=1 GOARCH=arm64 go test -race -short ./internal/database/... ./internal/graphql/...` passes — includes E12.1–E12.4 + E12.S1–E12.S6.
5. Schema introspection on `AppCertifiedBadgeDefinitionRecord` returns an `awardCount: Int!` field with the pinned description.
6. Cross-check (R2.8): sum `awardCount` across a page of definitions filtered by `where: { did: { eq: <issuer>, badgeType: { eq: "endorsement" } } }` equals `appCertifiedBadgeAward(where: { did: { eq: <issuer> } }) { totalCount }` MINUS any awards whose badge points at definitions outside the badgeType="endorsement" set. (Direct per-definition cross-check via `badge: { uri: { eq } }` is impossible today — `uri` is not exposed on AppCertifiedBadgeDefinitionWhereInput; see R2.8 for the discovered constraint.)
7. (LOCAL) `EXPLAIN ANALYZE` shows `Index Scan using idx_record_award_badge_uri on record` for the COUNT query.

## 13. PR body template

```markdown
## Summary

Adds an `awardCount` field on `AppCertifiedBadgeDefinitionRecord`
returning the count of awards strong-ref'ing the definition.
Unblocks the certified-app's Lists section reading the master view
in a single indexer round-trip (replaces today's two PDS calls +
client-side group-by).

- **Mechanism** — registry-driven derivedFieldRegistry pattern
  (parallel to joinedWhereRegistry from #87 and arrayWhereRegistry
  from #88). The per-collection record-type builder injects the
  field from the registry; the resolver consults a new
  RecordsRepository.CountAwardsByBadgeURI helper.
- **Index** — new migration 030 adds a partial btree expression
  index on (json->'badge'->>'uri') filtered by collection. Bare
  expression match (no typeof predicate — see #86 R1.1).
- **Safety** — registry-defined SQL means no request data is ever
  interpolated; the new helper has the standard "" early-return.

Follows the deep-flow process:
- Plan: `docs/issue-89/plan.md`
- Plan review (2 parallel reviewers): `docs/issue-89/review-round-1.md`
- Implementation review (2 parallel reviewers): `docs/issue-89/review-round-2.md`

## Breaking changes

None. The new `awardCount` field is purely additive.

## Out of scope

- Dataloader batching (plan §9.1; v1 is N+1, acceptable at bounded
  page sizes).
- Filtered awardCount (plan §9.3).
- Connection-level totalAwardCount (plan §9.2).
- Generic derived-count abstraction (plan §9.4; trigger: 2nd entry).

## Test plan

- [x] `go build ./...` clean
- [x] `go vet ./...` clean
- [x] `golangci-lint run ./...` returns 0 issues
- [x] `go test -race -short ./internal/database/... ./internal/graphql/...` passes (~11 new tests across both packages: 4 repository + 7 schema/resolver)
- [x] Behavioral-tests catalogue updated (E12) with wire-level + EXPLAIN ANALYZE halves
- [ ] CI green on this PR
- [ ] After merge: redeploy dev, verify with the certified-app's exact Lists query

Refs: #89
```
