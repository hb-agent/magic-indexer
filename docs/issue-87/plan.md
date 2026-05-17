# Implementation plan — issue #87

> Add a nested-where on `AppCertifiedBadgeAwardWhereInput.badge` so
> the certified-app's Endorsements tab can filter awards by joined
> badge-definition properties (initial use case: `badgeType =
> "endorsement"`) in one indexer round-trip.

## 1. Evaluate the request

The client today does this in two indexer round-trips:

1. Pull every award targeting a profile via the subject filter
   (#65).
2. Pull all definition records for the unique issuers in step 1,
   filter to `badgeType = "endorsement"`.
3. Client-side join.

The certified-app team has measurements showing the second hop is
now the bottleneck of the Endorsements tab. A nested-where
collapses both calls and the join into one query that the indexer
can serve with a single SQL statement.

The issue author's suggested shape mirrors the GraphQL idiom
they're already using elsewhere:

```graphql
where: {
  subject: { eq: $did }
  badge: { badgeType: { eq: "endorsement" } }
}
```

Pushback considered: the 2-call workaround is shipped and fast
enough; we could defer. But the author explicitly wants
generality ("future-proof for filtering by other fields on the
joined definition — title, description, …"), and adding a
narrow `badgeType`-only shortcut now would require ripping it
out later when the second use case appears. Better to build the
mechanism once.

The existing `badge` field on the award already exists as a
schema field (it returns the raw strongRef value). This issue is
about adding a *filter input* of the same name on the WhereInput;
the field on the record type is unaffected.

**Decision: implement Route B (general nested-where on the badge
join), scoped today to the badge.award → badge.definition pair
via a registry. Other joins (e.g. activity → activity for
referenced activities) follow the same pattern via additional
registry entries.**

## 2. Mechanism

The whole filter→SQL pipeline today assumes a single table
referenced as `r` (the `record` table aliased by
`GetByCollectionFiltered`). The strongRef → joined-record filter
needs a *second* aliased reference inside an EXISTS subquery so
the planner can run an index lookup on the joined collection's
primary key.

Target SQL shape for the issue's example:

```sql
SELECT r.uri, r.cid, r.did, r.json::text, …
FROM record r LEFT JOIN actor a ON r.did = a.did
WHERE r.collection = $1
  AND r.subject_did = $2
  AND EXISTS (
    SELECT 1
    FROM record d
    WHERE d.collection = 'app.certified.badge.definition'
      AND d.uri = r.json->'badge'->>'uri'
      AND d.json->>'badgeType' = 'endorsement'
  )
ORDER BY r.indexed_at DESC, r.uri DESC
LIMIT 100
```

The inner clause `d.json->>'badgeType' = 'endorsement'` is what
`buildFilterGroupRecursive` already produces — except every
existing site hardcodes `r` as the alias and `json` (no prefix)
as the column. The mechanism change is:

- **Plumb an alias context** through the recursive builder. Today
  the alias is implicit (`r`); after the change it's a parameter.
- **Add a `Joined []JoinedFilter` slice** to `FilterGroup` to
  represent the EXISTS subqueries. A `JoinedFilter` carries the
  target collection, the join expression on the outer record, and
  the inner `FilterGroup` to apply with the joined alias.

The wrapping `EXISTS (SELECT 1 FROM record d WHERE d.collection
= $coll AND d.uri = <join-expr> AND <inner>)` is built once in
the recursive builder, not per join.

## 3. Surface area

| Layer | File | Change |
|---|---|---|
| Filter SQL | `internal/database/repositories/filter.go` | (a) Plumb a `tableAlias string` parameter through `BuildFilterGroupClause`, `buildFilterGroupRecursive`, `buildSingleFilter`, `jsonExtract`, `jsonExtractTyped`, `qualifyColumn`. Today's call sites pass `"r"`; the new EXISTS subquery passes `"d"`. (b) Add `JoinedFilter` struct + `Joined []JoinedFilter` field on `FilterGroup`. (c) Emit EXISTS wrapping in the recursive builder. The lexicon-specific filter kinds (`KindArrayContributor`, `KindUnionSubject`, `KindStringSubject`) stay locked to `r`-only — they're collection-specific shapes and the joined badge.definition doesn't have those fields. If a future joined collection needs one, that's a per-kind generalisation. |
| Schema registry | `internal/graphql/schema/where.go` | New `joinedWhereRegistry` (parallel to the existing `filterRegistry`): maps `(parentLexiconID, fieldName)` → `(targetLexiconID, joinExprTemplate)`. First entry: `(app.certified.badge.award, "badge")` → `(app.certified.badge.definition, "json->'badge'->>'uri'")`. |
| Schema builder | `internal/graphql/schema/where.go` | In `buildWhereInputType`, after the property loop + filter-registry loop, iterate the `joinedWhereRegistry` for the lexicon and inject a field named after the join's field name (e.g. `badge`) typed as the target collection's WhereInput. The target's WhereInput must already exist — needs a `whereInputByLexiconID` lookup the builder maintains. |
| Schema extractor | `internal/graphql/schema/where.go` | In `extractFieldFiltersRecursive`, after the existing per-field branches, recognise joined-where fields by looking up `(lex.ID, fieldName)` in `joinedWhereRegistry`. Recursively extract the inner where against the *target* lexicon, wrap in a `JoinedFilter`, append to the current group's `Joined`. |
| Filter unit tests | `internal/database/repositories/filter_unit_test.go` | New tests covering: (a) alias plumbing — same FilterGroup with alias `"r"` vs `"d"` produces clauses with corresponding prefixes; (b) JoinedFilter emission — the EXISTS subquery shape, the join-expr inline, the inner filter clause with `d.` prefix; (c) nested composition — joined filter inside an `_or` group with other field filters. |
| Schema tests | `internal/graphql/schema/where_test.go` (or new `where_join_test.go`) | (a) Registry pin: `TestJoinedWhereRegistry_BadgeAwardBadge`; (b) extractor: feed a `{badge: {badgeType: {eq: "endorsement"}}}` payload, assert the resulting `JoinedFilter` shape and inner FilterGroup; (c) end-to-end SQL: feed the same payload to `GetByCollectionFiltered`'s SQL build path and assert the output string. |
| Behavioral test catalogue | `docs/behavioral-tests.md` | New entry E10 — nested-where on `badge` filter, mirroring E7/E8/E9. |

No new database migrations. The query plan for the inner EXISTS
uses the existing primary-key index on `record.uri` (the
definition lookup) plus the partial btree on `subject_did`
(migration 026) for the outer subject filter. No new indexes
needed for the immediate use case — measure first if a future
filter (e.g. `badge: { title: { contains: ... } }`) becomes hot.

## 4. Detail per layer

### 4.1 Alias plumbing — Step 1 of the refactor

Today's hot paths in `filter.go`:

- `BuildFilterGroupClause(group, paramOffset)` → recursive
- `buildSingleFilter(f, paramIdx)` → per-leaf dispatch
- `jsonExtract(fieldName, isJSON)` → `r.<col>` for non-JSON,
  `json->>'<field>'` for JSON
- `jsonExtractTyped(fieldName, lexiconType, isJSON)` → CAST
  wrappers on top
- `qualifyColumn(name)` → returns `"r." + name`

Change shape:

- Add a leading `alias string` parameter to each of those
  functions. Default behaviour preserved when callers pass
  `"r"`. JSON extraction becomes `alias + ".json->>'<field>'"`.
  Nested JSON path likewise.
- `qualifyColumn(alias, name)` returns `alias + "." + name`.
- **`jsonExtractTyped`'s non-JSON early-return must qualify with
  the alias, not return the bare field name** (filter.go:615 —
  R1.2 in plan-review round 1). Pre-existing latent: no
  column-level field uses `gt`/`lt` today, so the un-qualified
  return is silent — but once the alias is plumbed everywhere
  else it would be the only un-qualified site left, and a
  future addition would silently regress against `r.json->>` if
  not fixed now.
- Public `BuildFilterGroupClause` either grows the parameter
  (breaking) or stays as a thin wrapper around a private
  `buildFilterGroupClauseWithAlias(group, paramOffset, "r")`.
  The wrapper is cheaper for callers; pick that.
- **Runtime sentinel in `buildSingleFilter`** (R2.8): if `alias
  != "r"` and `f.Kind` is `KindArrayContributor`,
  `KindUnionSubject`, or `KindStringSubject`, return an error
  rather than emit SQL. These kinds hardcode `r.` SQL fragments
  that would be silently wrong inside a joined-Inner subquery.
  Today no joined collection has fields that route through
  these kinds (the badge.definition lexicon has none), but the
  sentinel future-proofs against an accidental registry edit.

Lexicon-specific filters (`KindArrayContributor`,
`KindUnionSubject`, `KindStringSubject`) are *not* alias-
parameterised in this change. They contain SQL that's
collection-specific (`record_contributor_identities(r.json)`,
`r.subject_did`, `r.json->>'subject'` for follow), and the
joined collection in scope today (badge.definition) doesn't have
any of those fields. If a future joined collection needs them,
that's a per-kind generalisation — flagged as out of scope here.

The plumbing is a no-op-equivalent refactor — landed as its own
commit so review can verify zero behaviour change before the
JoinedFilter introduction.

### 4.2 `JoinedFilter` struct + `Joined []JoinedFilter` on `FilterGroup`

```go
// JoinedFilter applies an inner FilterGroup to records in a
// different collection, joined by a strongRef-style URI lookup.
// The SQL shape is:
//
//   EXISTS (
//     SELECT 1 FROM record d
//     WHERE d.collection = $N
//       AND d.uri = <JoinExpr>            -- evaluated in the outer scope
//       AND (<Inner with alias "d">)
//   )
//
// JoinExpr is the SQL fragment that extracts the referenced URI
// from the outer record's JSON. For badge.award → badge.definition
// it's `r.json->'badge'->>'uri'`. The outer alias must be `r` for
// today's call sites (no nested joins yet); a future need would
// generalise via the alias plumbing in §4.1.
type JoinedFilter struct {
    TargetCollection string
    JoinExpr         string
    Inner            FilterGroup
}
```

Recursion safety: `Inner` is a `FilterGroup` (with its own
`_and`/`_or`/`Joined` slots), so technically you could nest
joins inside joins. We bound this at one level — `Inner.Joined`
must be empty, enforced by the extractor. Reason: nested EXISTS
subqueries inside an inner clause get planner-unfriendly fast,
and no current client wants this. If a future use case appears,
remove the guard and add an EXPLAIN ANALYZE test.

Condition counting: `FilterGroup.CountConditions()` today
walks `Filters` and `Children` (filter.go:208-214) but NOT
`Joined`. **Update it** to also sum `j.Inner.CountConditions()`
for each `j` in `Joined`, so the global
`MaxFilterConditions = 20` cap still applies after the
introduction of joined filters. (R1.1 in plan-review round 1
caught this — without the explicit code edit, a query with 19
outer leaves + 1 joined filter with 5 inner leaves would
report `CountConditions() = 20` while emitting 24 SQL
conditions, bypassing the cap.)

### 4.3 Recursive builder — EXISTS emission

In `buildFilterGroupRecursive`, after the leaf-filter loop and
before the child-group loop, iterate `group.Joined`:

```go
for _, j := range group.Joined {
    // Inner clause uses the joined alias "d".
    innerClause, innerParams, err := buildFilterGroupClauseWithAlias(
        j.Inner, paramIdx, "d")
    if err != nil {
        return "", nil, err
    }
    // Bind the target collection as the next param.
    collParam := fmt.Sprintf("$%d", paramIdx+len(innerParams))
    var existsClause string
    if innerClause == "" {
        existsClause = fmt.Sprintf(
            "EXISTS (SELECT 1 FROM record d WHERE d.collection = %s AND d.uri = %s)",
            collParam, j.JoinExpr)
    } else {
        existsClause = fmt.Sprintf(
            "EXISTS (SELECT 1 FROM record d WHERE d.collection = %s AND d.uri = %s AND (%s))",
            collParam, j.JoinExpr, innerClause)
    }
    clauses = append(clauses, existsClause)
    params = append(params, innerParams...)
    params = append(params, j.TargetCollection)
    paramIdx += len(innerParams) + 1
}
```

A `JoinedFilter` with an empty `Inner` (just the existence check)
is still legal — it asks "does this award point at a definition
that exists?" Useful for filtering out broken refs; cheap to
support.

**Depth budget across the EXISTS boundary** (R1.3): the inner
call to `buildFilterGroupClauseWithAlias(j.Inner, ...)` restarts
the SQL-builder's depth counter at 0, so the inner
`FilterGroup` gets its own `MaxFilterDepth = 3` budget under
the EXISTS. Combined with the §4.2 one-level-joined bound
(`Inner.Joined` must be empty), the worst-case nesting is
depth-3-outer + EXISTS + depth-3-inner. This is intentional —
the extractor side counts the joined entry as `depth+1` so
input rejection of deeply-nested *requests* still works; the
SQL-side reset just keeps the EXISTS clause's own
`_and`/`_or` recursion budget honest.

### 4.4 Schema registry

```go
// joinedWhereDescriptor describes a strongRef-style filter
// that joins to records in a different collection.
//
// SECURITY NOTE: JoinExpr is emitted verbatim into SQL.
// Registry values are code-defined and must NEVER be sourced
// from request data — they form the SQL fragment for the
// EXISTS subquery's join predicate. Treat additions to this
// registry as a SQL diff and review accordingly.
type joinedWhereDescriptor struct {
    FieldName     string
    TargetLexicon string  // the joined collection
    JoinExpr      string  // SQL fragment extracting the referenced URI from the outer record
    Description   string  // pinned consumer-facing text
}

// joinedWhereRegistry maps parentLexiconID → fieldName → descriptor.
var joinedWhereRegistry = map[string]map[string]joinedWhereDescriptor{
    "app.certified.badge.award": {
        "badge": {
            FieldName:     "badge",
            TargetLexicon: "app.certified.badge.definition",
            JoinExpr:      "r.json->'badge'->>'uri'",
            Description:   badgeAwardBadgeDescription,
        },
    },
}
```

`badgeAwardBadgeDescription`: pinned text documenting the join
shape, the supported inner fields, and the `_or` composition
example. Following the existing `badgeAwardSubjectDescription`
shape (where.go:47): one paragraph, ending with a composition
example. Use the saner intersection+disjunction shape (R2.3 in
plan-review round 1):

```
where: {
  subject: { eq: "did:plc:me" },
  badge: { _or: [
    { badgeType: { eq: "endorsement" } },
    { badgeType: { eq: "verification" } }
  ] }
}
```

(the earlier draft used an outer `_or` which is rarely what a
client wants — "subject is X OR there exists ANY definition
with badgeType=endorsement" returns the whole badge.award
collection when any endorsement-typed definition exists).

### 4.5 Schema builder injection

**Explicit two-phase construction** (R2.1 + R2.6 in plan-review
round 1). Today `buildWhereInputType` is called lazily inside
`buildQueryType` per-lexicon, mid-loop — incompatible with the
joined-where injection because target lexicons may not be built
when the parent is constructed. The plan adds an explicit
`buildWhereInputTypes()` phase between phases 3 and 4 of
`Build()` (`builder.go:50-65`). Two passes inside that phase:

- **Pass 1**: iterate `GetCollectionLexicons()`; call the
  existing `buildWhereInputType(lex)` for each and store the
  result in a new `b.whereInputByLexiconID[lexiconID]` map.
  Result: every WhereInput exists with its property-derived
  and filter-registry fields, but no joined fields yet.
- **Pass 2**: iterate again; for each lexicon's entry in
  `joinedWhereRegistry`, look up the target's WhereInput from
  the now-complete map and inject the joined field via
  `*graphql.InputObject.AddFieldConfig` (the same mechanism
  `_and`/`_or` already use at where.go:146-153).

```go
// Pass 2: wire joined fields after all base WhereInputs exist.
for _, lex := range b.registry.GetCollectionLexicons() {
    parentInput := b.whereInputByLexiconID[lex.ID]
    if parentInput == nil {
        continue
    }
    for _, jd := range joinedWhereRegistry[lex.ID] {
        targetInput := b.whereInputByLexiconID[jd.TargetLexicon]
        if targetInput == nil {
            // Target lexicon not registered (e.g. dev hasn't uploaded
            // badge.definition yet). Skip silently — the absence of the
            // field is the safe behaviour. Log at warn so an operator
            // can see why a documented filter is missing.
            slog.Warn("joinedWhereRegistry entry references unregistered lexicon — skipping field",
                "parent", lex.ID, "field", jd.FieldName, "target", jd.TargetLexicon)
            continue
        }
        parentInput.AddFieldConfig(jd.FieldName, &graphql.InputObjectFieldConfig{
            Type:        targetInput,
            Description: jd.Description,
        })
    }
}
```

**Constraint**: do NOT switch the `Fields` arg of
`graphql.NewInputObject` to an
`InputObjectConfigFieldMapThunk`. `*graphql.InputObject.AddFieldConfig`
(graphql-go's definition.go) checks the non-Thunk path on every
call; making it a Thunk would break both the existing
`_and`/`_or` injection and this new joined-field injection.
Keep `Fields` as a concrete `InputObjectConfigFieldMap`.

`buildQueryType` (`builder.go` around line 440 today) becomes a
simple lookup — `whereInput := b.whereInputByLexiconID[lexiconID]`
instead of an inline `buildWhereInputType(lex)` call.

### 4.6 Schema extractor

In `extractFieldFiltersRecursive`, after the existing per-field
branches and before falling through to error:

```go
if jd, ok := lookupJoinedWhereDescriptor(lex.ID, fieldName); ok {
    targetLex, ok := registry.Get(jd.TargetLexicon)
    if !ok {
        return repositories.FilterGroup{}, fmt.Errorf(
            "field %q: target lexicon %q not registered", fieldName, jd.TargetLexicon)
    }
    inner, err := extractFieldFiltersRecursive(filterInput, targetLex, registry, depth+1)
    if err != nil {
        return repositories.FilterGroup{}, fmt.Errorf("field %q: %w", fieldName, err)
    }
    if len(inner.Joined) > 0 {
        // See §4.2 — joined-where nesting is bounded to one level.
        return repositories.FilterGroup{}, fmt.Errorf(
            "field %q: nested joined-where not supported (max one level)", fieldName)
    }
    group.Joined = append(group.Joined, repositories.JoinedFilter{
        TargetCollection: jd.TargetLexicon,
        JoinExpr:         jd.JoinExpr,
        Inner:            inner,
    })
    continue
}
```

**Signature change** (R2.7): the registry must be threaded
through `extractFieldFiltersRecursive` and `extractFieldFilters`.
Today these are free functions taking only `lex *lexicon.Lexicon`
(where.go:195, 199); the joined-where branch needs to look up
the target lexicon by ID. The cleanest local fix is adding a
`registry *lexicon.Registry` parameter. The single caller in
`builder.go` already has the registry in scope.

### 4.7 Tests

#### Filter unit tests (filter.go)

- `TestBuildFilterGroupClause_AliasParameter` — same filter set
  emitted twice with `r` and `d`; assert the prefix differs in
  every emitted clause.
- `TestBuildFilterGroupClause_JoinedFilter_Eq` — single joined
  filter with one inner `badgeType: { eq: "endorsement" }` leaf;
  assert the EXISTS shape including `d.collection = $N`,
  `d.uri = r.json->'badge'->>'uri'`, and the inner clause uses
  `d.` prefix.
- `TestBuildFilterGroupClause_JoinedFilter_EmptyInner` — bare
  existence check produces the simpler `EXISTS (... WHERE
  d.collection = $N AND d.uri = …)` shape without the `AND (…)`
  tail.
- `TestBuildFilterGroupClause_JoinedInsideOr` — `_or` group with
  one normal leaf and one joined filter; assert both clauses are
  joined by ` OR ` with correct parameter numbering.
- `TestJoinedFilter_CountConditions` — verify the inner's leaf
  count rolls up to the outer's `CountConditions()`.
- `TestJoinedFilter_NoNestedJoinedAtRuntime` — feeding a
  JoinedFilter whose Inner has its own Joined slot returns an
  error or silently empty; lock the bound.
- `TestBuildSingleFilter_LockedKindsRejectNonRSeqAlias` (R2.8)
  — feeding a `FieldFilter` with `Kind = KindArrayContributor`,
  `KindUnionSubject`, or `KindStringSubject` and an alias other
  than `"r"` returns an error rather than silently emitting
  collection-specific SQL with the wrong alias.

#### Schema tests (where.go)

- `TestJoinedWhereRegistry_BadgeAwardBadge` — pin shape:
  field name, target lexicon, JoinExpr literal.
- `TestExtractFieldFilters_NestedBadgeWhere` — synthetic where
  argument `{badge: {badgeType: {eq: "endorsement"}}}` extracts
  to a FilterGroup with one Joined and the expected inner
  FieldFilter.
- `TestBuildWhereInput_BadgeAwardHasBadgeFilter` — schema
  introspection-style assertion that
  `AppCertifiedBadgeAwardWhereInput.badge` is typed as
  `AppCertifiedBadgeDefinitionWhereInput` with the pinned
  description.
- `TestExtractFieldFilters_NestedBadgeWhere_DepthCap` (R2.4) —
  worst-case shape `where: { _and: [{ _and: [{ badge: { _and:
  [{ _and: [{ badgeType: {eq: "x"} }] }] } }] }] }` exceeds
  `MaxFilterDepth = 3` at the inner. Assert the extractor
  rejects it (depth+1 across the joined boundary plus the
  inner's own two `_and` levels = total of 4).

#### Behavioral test (catalogue)

`E10 — nested-where on badge.award.badge` — wire-level
(`EITHER`) issuing the issue's exact query against the deployed
instance, plus the `EXPLAIN ANALYZE` half (`LOCAL`) checking the
plan uses index lookups on both sides of the EXISTS.

## 5. Acceptance criteria

1. `GOARCH=arm64 go build ./...` clean.
2. `GOARCH=arm64 go vet ./...` clean.
3. `GOARCH=arm64 golangci-lint run ./...` returns `0 issues.`
4. `CGO_ENABLED=1 GOARCH=arm64 go test -race -short ./internal/database/... ./internal/graphql/...` passes — includes the alias plumbing + JoinedFilter unit tests + schema-side extractor tests.
5. Schema introspection on
   `AppCertifiedBadgeAwardWhereInput.badge` returns type
   `AppCertifiedBadgeDefinitionWhereInput`.
6. The issue's example query:
   ```graphql
   {
     appCertifiedBadgeAward(
       where: { subject: { eq: "<did>" }, badge: { badgeType: { eq: "endorsement" } } }
       first: 100
     ) { edges { node { uri did createdAt } } }
   }
   ```
   returns awards whose joined definition has
   `badgeType = "endorsement"` and excludes others.
7. (LOCAL) `EXPLAIN ANALYZE` on the equivalent SQL shows the
   inner EXISTS resolves via index lookup on `record(uri)`.

## 6. Alternatives considered

| Alternative | Why not |
|---|---|
| Route A — minimal `badgeType: { eq: ... }` shortcut only | Doesn't deliver the generality the author explicitly asked for ("future-proof for filtering by other fields"). Would have to be replaced when the next use case lands. |
| New `KindBadgeJoin` filter that hardcodes the EXISTS shape per-call | Same problem as Route A but uglier (lexicon-specific filter kind for an inherently-generic capability). |
| Refactor to a real query builder (squirrel, etc.) | Massive scope creep. The audit didn't ask for it. |
| INNER JOIN instead of EXISTS | Would change result shape (potentially duplicate award rows when a definition matches the join) and complicate the existing keyset cursor. EXISTS is the right shape for "filter without changing output." |
| Materialise the badge.definition's badgeType into the award's record at ingestion | Stale-read problem (definition updated after award is materialised — the materialised copy lags). Adds an ingestion-side dependency that doesn't exist today. EXISTS is the natural denormalisation point at read time. |
| Skip the registry, hard-code the badge.award→badge.definition pair in the extractor | Same coupling problem as the audit's `KindUnionSubject` hardcoding for badge.award. Registry is one map plus a lookup function — trivial cost, big future-proofing win. |

## 7. Rollback

- No schema changes. Pure code changes.
- Alias plumbing is a no-op-equivalent refactor and can be reverted independently. Reverts cleanly because every callsite was updated mechanically to pass `"r"`.
- `JoinedFilter` introduction + registry entry are additive (no existing test or query was using a `Joined` field that's empty). Revert removes the new code paths without touching anything else.

## 8. Out of scope

- Other join targets (e.g. `claim.activity` ref'ing other activities, `report` → reporter actor row). Each is a new registry entry plus a description constant — cheap follow-ups.
- Generalising the alias-plumbing change to the lexicon-specific filter kinds (`KindArrayContributor`, `KindUnionSubject`, `KindStringSubject`). They stay locked to `r.`. The joined badge.definition doesn't have any fields that route through these kinds, so the restriction is silent today.
- Index optimisation for the inner EXISTS. The current plan relies on the primary-key index on `record.uri` for the join condition. If a heavy filter like `badge: { title: { contains: ... } }` becomes hot, we'd want an expression index on `record(json->>'title') WHERE collection = 'app.certified.badge.definition'` — measure first.
- Multi-level nested where (a joined filter that itself joins). Bounded at one level by the extractor.
- Removing the existing `subject` field on award's WhereInput in favour of routing through the join (you could equivalently write `where: { subject: { eq: ... } }` as a join filter via the subject strongRef on union shapes, but #65's column-based path is faster and the API contract is established). The two filter shapes will coexist.

## 9. Open questions for the operator

1. **Description text on the `badge` filter input**: the pinned
   description should set expectations. Plan to include: which
   inner fields are supported (everything in
   `AppCertifiedBadgeDefinitionWhereInput`), the underlying SQL
   shape (EXISTS), and the join-shape gotcha (an award pointing
   at a missing/deleted definition fails the existence check —
   `{badge: {}}` filters out broken refs).

2. **Pre-merge test**: do you want me to do a Postgres-backed
   test of the EXISTS SQL against the live data on dev (read-
   only) before merging, or is the unit-test coverage + the
   behavioral test catalogue entry sufficient? The pre-merge
   test would be a single `curl` to dev after a temporary upload
   of a test build — extra friction; pick if you want the safety.

## 10. Sequencing

Commit order on `staging`:

1. **Plan + plan-review** docs.
2. **Alias plumbing + `CountConditions` extension + locked-kind
   sentinel** — the refactor side of filter.go. All existing
   callers pass `"r"`, no behaviour change for them. New
   `CountConditions` arm walks `Joined` (currently a no-op
   since nothing produces them yet). New sentinel in
   `buildSingleFilter` rejects non-`r` alias + lexicon-specific
   Kind. Verified by the existing test suite passing untouched
   plus the new `TestBuildSingleFilter_LockedKindsRejectNonRSeqAlias`
   test exercising the new error path.
3. **`JoinedFilter` + recursive builder emission** — adds the
   new data structure on `FilterGroup` and the EXISTS SQL
   emission, but nothing yet produces `JoinedFilter` values
   from request inputs. Unit tests on the SQL shape (eq,
   empty-Inner, _or composition).
4. **Schema registry + two-phase builder + extractor** — adds
   `joinedWhereRegistry`, the explicit `buildWhereInputTypes()`
   phase with its two passes, `whereInputByLexiconID` map, and
   the extractor branch that recognises joined-where fields.
   Schema-side pin test for the registry, extractor test for
   the happy path, depth-cap test. Smoke: schema introspection
   on dev now shows the `badge` field on
   `AppCertifiedBadgeAwardWhereInput`. The phase move (R2.6)
   lands here, not as a separate refactor commit, because the
   joined-where injection is exactly what motivates it.
5. **Behavioral test catalogue** entry E10.
6. **Impl-review round 2 doc + Draft PR.**

After PR merges, redeploy + verify on dev with the actual issue
query.
