# Implementation plan — issue #88

> Add a generic array-element nested-where on lexicon array fields,
> first instance: `OrgHypercertsCollectionWhereInput.items`. The
> certified-app's "what collections contain this activity?" query
> becomes one round-trip:
>
> ```graphql
> appCertifiedHypercertsCollection(where: {
>   type: { eqi: "project" },
>   items: { itemIdentifier: { uri: { eq: $certUri } } }
> })
> ```
>
> Semantics: row matches iff ANY items[i] satisfies the inner
> where ("some" / "any" semantics, not "all"). Same `_and`/`_or`
> machinery available inside the inner element predicate.

## 1. Goal & scope

The certified-app today reads the cert-projects relationship by
scanning every collection of type `project` and joining client-
side on `items[*].itemIdentifier.uri`. At the bounded scale of
the use case (≤ a few thousand `project` collections) this is
fine, but the per-cert read fanout is N (one round-trip per
cert page). The author wants the same single-shot read-shape
already established for strong-refs (issue #87): one query, one
SQL statement.

The decided shape is **Shape A from the issue** — a generic
array-element nested-where input that mirrors `_and`/`_or` at
the GraphQL surface and emits one EXISTS subquery per
registered array field. The shape is reusable for any lexicon's
array-of-objects field (`app.certified.badge.definition.allowed
Issuers`, contributor arrays on activity, etc.); v1 ships the
`org.hypercerts.collection.items` registration only.

### Shipping looks like

1. Schema introspection of `OrgHypercertsCollectionWhereInput`
   shows an `items` input field typed as a new
   `OrgHypercertsCollectionItemWhereInput` input object whose
   fields mirror the `#item` def's properties (`itemIdentifier`,
   `itemWeight`). Pinned description on the `items` field
   documents the "any items[i] satisfies" semantics.
2. The issue's example query returns the matching collections
   in one round-trip; identical result-set to a hand-rolled
   client-side join.
3. `EXPLAIN ANALYZE` on the equivalent SQL shows the EXISTS
   subquery uses `jsonb_array_elements` per row with no
   secondary collection scan (the array is local to `r.json`).

### Non-goals

- Auto-derivation of array-element inputs from lexicon shape
  alone (followup §9).
- `@>` containment-based emission for index-friendly equality
  inner predicates (followup §9).
- New indexes / migrations — bounded volume; sequential scan +
  per-row `jsonb_array_elements` is acceptable at < 10⁴ rows.
- "All items must match" semantics — not asked for, and SQL
  shape for it (`NOT EXISTS ... NOT matching`) is awkward
  enough to defer until a use case exists.
- Multi-level array-of-arrays (e.g. an array element whose own
  field is itself a registered array). Bounded at one array
  level by the extractor, same shape as #87's joined-where
  one-level bound.

## 2. Decided shape

**Registry-based, EXISTS + `jsonb_array_elements` aliased to
`e(json)`.** Three decisions, each justified:

### 2.1 Registry vs lexicon-derived

**Decision: registry for v1.** Same pattern as `joinedWhereRegistry`
(#87) and `filterRegistry` (#64/65/86). The registry gives:

- Explicit per-field opt-in (no surprise broad-surface changes
  when a new lexicon registers an array property).
- A pinned-description hook (consumers see "any items[i]
  satisfies" at schema introspection).
- A future ladder to per-element security policy (e.g. some
  array element might want DID-only filtering on a contributor
  field — registry can pin operator subsets later).

Auto-derivation would be cleaner once we have 3+ entries; for
1 entry the trade-off doesn't justify the extra schema-walker
code. Documented as follow-up §9.1.

### 2.2 SQL emission shape

Three candidates discussed in the brief; **picking (a): EXISTS
+ `jsonb_array_elements`**.

```sql
EXISTS (
  SELECT 1
  FROM jsonb_array_elements(r.json->'items') AS e(json)
  WHERE <inner predicates against alias e>
)
```

Justification:

- **General** — works for any inner operator, not just
  equality.
- **Alias-plumbable** — uses the existing `buildFilterGroupClauseWithAlias`
  with `alias="e"` and the existing `jsonExtract(alias, fieldName, isJSON)`
  unchanged.
- **Reviewer-friendly** — pattern reviewers already understand
  from #87's joined EXISTS.
- **Volume-appropriate** — the cert-projects collection count
  is bounded (≤ 10⁴), and `jsonb_array_elements` per row is
  cheap (single-digit ms per 1000 rows for items arrays capped
  at lexicon's `maxLength: 1000`).

Candidate (b) `r.json->'items' @> $1::jsonb` is index-friendly
via a GIN expression index, but limited to pure equality
conjunctions on a fixed inner subtree shape, and a new index
costs more than the query is currently worth. Candidate (c)
hybrid is documented in §9 as a follow-up once we measure a
hot equality-only query.

### 2.3 Alias plumbing inside the EXISTS

The inner element variable is exposed as a one-column row
`e(json)` so the existing `jsonExtract(alias, fieldName, isJSON)`
emits `e.json->>'itemIdentifier'` and `e.json->'itemIdentifier'->>'uri'`
exactly like it does for `r.`-prefixed JSON paths. No new
helper needed.

**Decision: emit `FROM jsonb_array_elements(<arrayPath>) AS
e(json)`.** Alternative (introduce an `e` sentinel that means
"element is the bare jsonb, not a row with a json column") would
require a parallel `jsonExtractElement` helper. The aliased-as-row
form keeps the single SQL-emission path: alias `r` for outer,
`d` for joined-where (#87), `e` for array-element. Same
`jsonExtract(alias, ...)` signature, three valid alias values.

This is the third alias value the locked-kind sentinel in
`buildSingleFilter` (filter.go:586) must reject — `KindArrayContributor`,
`KindUnionSubject`, `KindStringSubject` already error for alias
`!= "r"`; the test gains a new arm for alias `"e"`.

## 3. Surface area

| Layer | File | Change | LOC (rough) |
|---|---|---|---|
| Filter AST | `internal/database/repositories/filter.go` | (a) Add `ArrayFilter` struct + `Arrays []ArrayFilter` slot on `FilterGroup`. (b) Extend `IsEmpty` and `CountConditions` to walk `Arrays`. (c) Add new arm in `buildFilterGroupRecursive` between the `Joined` arm and the clause join — emits the `EXISTS (SELECT 1 FROM jsonb_array_elements(...) AS e(json) WHERE ...)` shape with `alias="e"` recursion into `j.Inner`. | ~80 |
| Filter SQL sentinel | `internal/database/repositories/filter.go` (line ~586) | Extend the locked-kind comment (sentinel itself already rejects every `alias != "r"`) to call out alias `e` as a valid-but-rejected case; no code change required, but tighten the comment so a future reader knows the array path also goes through. | ~5 |
| Filter unit tests | `internal/database/repositories/filter_unit_test.go` | New tests E11.1–E11.7 (see §7). | ~250 |
| Schema registry | `internal/graphql/schema/where.go` | (a) New `arrayWhereDescriptor` struct + `arrayWhereRegistry` map. (b) First entry: `org.hypercerts.collection.items`. (c) `lookupArrayWhereDescriptor` helper. (d) New `collectionItemsArrayDescription` pinned text. (e) New `buildArrayElementInputType(parentLex, elementDefName)` helper that constructs the per-element input object from the lexicon's element-def properties + `_and`/`_or` injected via `AddFieldConfig` (same pattern as `buildWhereInputType`). | ~140 |
| Schema builder | `internal/graphql/schema/builder.go` | Extend `buildWhereInputTypes` Pass 2 (the existing joined-where injection) to ALSO iterate `arrayWhereRegistry` and inject the array-element field on each parent input. New `b.arrayElementInputs` map cached by `(parentLexicon, fieldName)` for the inner input type. | ~50 |
| Schema extractor | `internal/graphql/schema/where.go` | New branch in `extractFieldFiltersRecursive` (after the joined-where branch, before the filter-registry branch) — recognises array fields via `lookupArrayWhereDescriptor`, recursively extracts inner against the synthetic "element pseudo-lexicon" (a thin wrapper exposing the element def's properties via the same `GetProperty` shape), wraps in `ArrayFilter`, appends to `group.Arrays`. Enforces one-level bound: inner must not contain `Arrays` of its own. | ~80 |
| Schema tests | `internal/graphql/schema/where_test.go` | New tests E11.S1–E11.S5 (see §7). | ~220 |
| Behavioral catalogue | `docs/behavioral-tests.md` | New entry E11 (template in §8). Add row to the checklist table at line ~97. | ~80 |

**No migrations.** No indexes. Total ~900 LOC across 5 files
(test files account for ~470 of that).

## 4. AST design

### 4.1 `ArrayFilter` struct

```go
// ArrayFilter applies an inner FilterGroup to elements of a
// JSON array field on the outer record. Match semantics are
// "any element satisfies" — emitted as:
//
//   EXISTS (
//     SELECT 1
//     FROM jsonb_array_elements(<ArrayPath>) AS e(json)
//     WHERE <Inner with alias "e">
//   )
//
// ArrayPath is the SQL fragment that returns the jsonb array
// from the outer record's JSON. For the org.hypercerts.collection
// `items` field it's `r.json->'items'`. The outer alias must be
// `r` for today's call sites (no nested arrays inside joins yet);
// a future need would generalise via the alias plumbing.
//
// SECURITY: ArrayPath is emitted verbatim into SQL. Values come
// from arrayWhereRegistry in internal/graphql/schema/where.go —
// code-defined, never sourced from request data. Treat additions
// to that registry as a SQL diff. Same contract as JoinedFilter.JoinExpr.
//
// One-level bound: Inner.Arrays must be empty (enforced by the
// schema-side extractor). Nested EXISTS-over-array elements is
// expressible but adds no value at the bounded volume here, and
// the SQL planner has no way to share work across the two
// jsonb_array_elements calls. If a future use case appears,
// remove the guard and add an EXPLAIN ANALYZE test.
//
// Coexistence with Joined: an outer FilterGroup may have BOTH
// Joined and Arrays entries; they're independent EXISTS clauses
// AND-composed (or OR-composed via the group's operator).
// However an inner FilterGroup inside a Joined or Arrays entry
// cannot itself contain Arrays — bounded at one level for the
// same planner-cost reason.
type ArrayFilter struct {
    FieldName string      // diagnostic; the JSON property name on the outer record (e.g. "items")
    ArrayPath string      // SQL fragment returning the jsonb array, e.g. "r.json->'items'"
    Inner     FilterGroup // predicates on each element, evaluated with alias "e"
}
```

### 4.2 `FilterGroup` additions

```go
type FilterGroup struct {
    Operator GroupOperator
    Filters  []FieldFilter
    Children []FilterGroup
    Joined   []JoinedFilter   // existing (#87)
    Arrays   []ArrayFilter    // NEW
}

func (g *FilterGroup) IsEmpty() bool {
    return len(g.Filters) == 0 && len(g.Children) == 0 &&
        len(g.Joined) == 0 && len(g.Arrays) == 0
}

func (g *FilterGroup) CountConditions() int {
    count := len(g.Filters)
    for i := range g.Children {
        count += g.Children[i].CountConditions()
    }
    for i := range g.Joined {
        count += g.Joined[i].Inner.CountConditions()
    }
    for i := range g.Arrays {
        count += g.Arrays[i].Inner.CountConditions() // NEW — same R1.1 hazard
    }
    return count
}
```

The `Arrays` walk in `CountConditions` is **load-bearing** —
without it, a query with 19 outer leaves + 1 array-element
filter with 5 inner leaves passes the `MaxFilterConditions=20`
cap while emitting 24 SQL conditions. Same hazard as #87 R1.1;
explicit test pins this (E11.5 below).

## 5. Schema design

### 5.1 `arrayWhereRegistry` shape

```go
// arrayWhereDescriptor describes an array-element nested-where
// filter on a lexicon's array-of-objects property.
//
// SECURITY: ArrayPath is emitted verbatim into SQL by the
// EXISTS-subquery builder. Registry values are code-defined
// and must NEVER source from request data. Same contract as
// joinedWhereDescriptor.JoinExpr.
type arrayWhereDescriptor struct {
    FieldName   string // the array property name on the outer record (e.g. "items")
    ArrayPath   string // SQL fragment yielding the jsonb array, e.g. "r.json->'items'"
    ElementDef  string // the local def name within the parent lexicon (e.g. "item")
    Description string // pinned consumer-facing text
}

// arrayWhereRegistry maps parentLexiconID → fieldName → descriptor.
// First entry (issue #88): org.hypercerts.collection.items, an
// array of #item objects, each with itemIdentifier:strongRef and
// optional itemWeight:string.
var arrayWhereRegistry = map[string]map[string]arrayWhereDescriptor{
    "org.hypercerts.collection": {
        "items": {
            FieldName:   "items",
            ArrayPath:   "r.json->'items'",
            ElementDef:  "item",
            Description: collectionItemsArrayDescription,
        },
    },
}

func lookupArrayWhereDescriptor(lexID, fieldName string) (arrayWhereDescriptor, bool) {
    if byField, ok := arrayWhereRegistry[lexID]; ok {
        d, ok := byField[fieldName]
        return d, ok
    }
    return arrayWhereDescriptor{}, false
}
```

### 5.2 Pinned description

```
const collectionItemsArrayDescription = `Filter collections to those whose items array contains at least one element matching the inner where (any-element semantics). Every property on the #item element def is filterable — itemIdentifier (a com.atproto.repo.strongRef, queryable via {uri: {eq: "at://..."}} or {cid: {eq: "..."}}) and itemWeight (string). Compose with the outer type / title / did filters via the usual _and (default) or _or, and inside the inner you can use _and/_or to mix item-element conditions. The filter resolves via an EXISTS subquery over jsonb_array_elements(json->'items'); a collection with an empty or absent items array fails the existence check, so {items: {}} (no inner predicate) filters to collections that have at least one item. Example, "project collections containing this cert": where: { type: { eqi: "project" }, items: { itemIdentifier: { uri: { eq: "at://did:plc:.../org.hypercerts.claim.activity/abc" } } } }.`
```

### 5.3 Per-element input type construction

The inner input shape for the `items` field is derived from
the `#item` def's properties: `itemIdentifier` (ref to
`com.atproto.repo.strongRef`) and `itemWeight` (string).

```go
// buildArrayElementInputType constructs the per-element WhereInput
// for a registered array-where descriptor. The input name reuses
// the existing response-side element type name with a WhereInput
// suffix — for org.hypercerts.collection.items, the response type
// is OrgHypercertsCollectionItem, so the input is
// OrgHypercertsCollectionItemWhereInput. Matches the
// <RecordType>WhereInput precedent (e.g. AppCertifiedBadgeAward
// → AppCertifiedBadgeAwardWhereInput).
//
// Property dispatch mirrors buildWhereInputType:
//   - scalar properties (string / integer / datetime / etc.)
//     route through propertyToFilterInput.
//   - ref properties pointing at a strongRef route through a
//     new StrongRefFilterInput (uri + cid as StringFilterInput).
//   - other ref / union / array / object properties are skipped
//     silently (not filterable in v1; follow-up §9.4 to relax).
//
// _and / _or are injected via AddFieldConfig at the bottom so the
// inner can use the same boolean composition as the outer.
func (b *Builder) buildArrayElementInputType(
    parentLex *lexicon.Lexicon,
    descriptor arrayWhereDescriptor,
) *graphql.InputObject {
    elementDef, ok := parentLex.Defs.Others[descriptor.ElementDef]
    if !ok || !elementDef.IsObject() {
        slog.Warn("arrayWhereRegistry references missing element def",
            "lexicon", parentLex.ID, "field", descriptor.FieldName,
            "elementDef", descriptor.ElementDef)
        return nil
    }
    // ... build fields from elementDef.Object.Properties ...
    // ... inject _and/_or ...
}
```

A new `StrongRefFilterInput` is needed for the `itemIdentifier`
ref. Shape:

```go
var StrongRefFilterInput = graphql.NewInputObject(graphql.InputObjectConfig{
    Name:        "StrongRefFilterInput",
    Description: "Filter conditions for a com.atproto.repo.strongRef ({uri, cid}). Both subfields use StringFilterInput semantics; equality on uri is the load-bearing case (matching a specific record identity).",
    Fields: graphql.InputObjectConfigFieldMap{
        "uri": {Type: StringFilterInput, Description: "Filter by the strongRef's uri (at://...)."},
        "cid": {Type: StringFilterInput, Description: "Filter by the strongRef's cid (content hash)."},
    },
})
```

Lives next to `DIDFilterInput` in `internal/graphql/types/filters.go`.
Adding it does not affect existing record-type generation —
the existing `FilterInputForLexiconType` doesn't dispatch refs
(returns nil), so no behaviour change.

### 5.4 Two-phase build extension

The existing `Builder.buildWhereInputTypes` already runs in two
passes (Pass 1: build all `WhereInput`s; Pass 2: inject joined
fields). The change extends Pass 2:

```go
// Pass 2: wire joined fields after all base WhereInputs exist.
for _, lex := range b.registry.GetCollectionLexicons() {
    parentInput := b.whereInputs[lex.ID]
    if parentInput == nil {
        continue
    }
    // Existing: joined-where field injection.
    for _, jd := range joinedWhereRegistry[lex.ID] { ... }

    // NEW: array-element nested-where field injection.
    for _, ad := range arrayWhereRegistry[lex.ID] {
        elementInput := b.buildArrayElementInputType(lex, ad)
        if elementInput == nil {
            continue  // already logged inside the builder
        }
        // Cache for the extractor to look up the element-def lexicon shape.
        b.arrayElementInputs[lex.ID+"."+ad.FieldName] = elementInput
        parentInput.AddFieldConfig(ad.FieldName, &graphql.InputObjectFieldConfig{
            Type:        elementInput,
            Description: ad.Description,
        })
    }
}
```

The `arrayElementInputs` cache exists primarily as a debugging
hook (introspection-style tests can reach the synthesised
input type). The extractor doesn't need it — it pulls the
element def directly from `parentLex.Defs.Others[ad.ElementDef]`
when extracting inner filters.

### 5.5 Extractor flow

```go
// In extractFieldFiltersRecursive, after the joined-where branch
// and before the filter-registry branch:
if lex != nil {
    if ad, ok := lookupArrayWhereDescriptor(lex.ID, fieldName); ok {
        // Element pseudo-lexicon: wraps the parent's #<ElementDef>
        // ObjectDef in a *lexicon.Lexicon whose Defs.Main looks like
        // a record with the element's properties. This lets us recurse
        // into extractFieldFiltersRecursive with the standard signature.
        elemLex, err := makeElementPseudoLexicon(lex, ad)
        if err != nil {
            return repositories.FilterGroup{}, fmt.Errorf("field %q: %w", fieldName, err)
        }
        inner, err := extractFieldFiltersRecursive(filterInput, elemLex, registry, depth+1)
        if err != nil {
            return repositories.FilterGroup{}, fmt.Errorf("field %q: %w", fieldName, err)
        }
        if len(inner.Arrays) > 0 {
            return repositories.FilterGroup{}, fmt.Errorf(
                "field %q: nested array-where not supported (max one level)", fieldName)
        }
        // No restriction on inner.Joined — an element could legally
        // contain a strongRef that we want to joined-where on. Not
        // wired today (no registry entry combines), but the AST
        // supports it; defer until a use case appears.
        group.Arrays = append(group.Arrays, repositories.ArrayFilter{
            FieldName: ad.FieldName,
            ArrayPath: ad.ArrayPath,
            Inner:     inner,
        })
        continue
    }
}
```

`makeElementPseudoLexicon` is a thin helper that builds a
`*lexicon.Lexicon` whose `Defs.Main` is a synthetic `RecordDef`
populated from the element def's `Properties`.

**Struct conversion note (R1.3)**: `Defs.Others["item"]` returns
a `Def` whose `Object *ObjectDef` is non-nil; `Defs.Main` is
typed `*RecordDef`. Both `RecordDef` and `ObjectDef` carry an
identical `Properties map[string]PropertyEntry` field — the
helper does a struct-type conversion, not pointer reuse. The
`sync.Once` + `propIndex` private fields zero-init correctly.
Sketch:

```go
func makeElementPseudoLexicon(parent *lexicon.Lexicon, ad arrayWhereDescriptor) (*lexicon.Lexicon, error) {
    elemDef, ok := parent.Defs.Others[ad.ElementDef]
    if !ok || elemDef.Object == nil {
        return nil, fmt.Errorf("element def %q not found on %s", ad.ElementDef, parent.ID)
    }
    return &lexicon.Lexicon{
        ID: parent.ID + "#" + ad.ElementDef,  // diagnostic + collision-safe (R1.3)
        Defs: lexicon.Defs{
            Main: &lexicon.RecordDef{
                Properties: elemDef.Object.Properties,
                Required:   elemDef.Object.Required,
            },
        },
    }, nil
}
```

**Collision-safety contract (R1.3)**: the synthetic ID uses
the `#`-anchor convention (`<parent-lex-id>#<elementDef>`)
because real lexicon IDs never contain `#`. This guarantees
no `filterRegistry[elemLex.ID]` lookup can accidentally hit a
real lexicon's entries.

### 5.5.1 Property dispatch inside the element pseudo-lexicon (R1.4)

The extractor today (`internal/graphql/schema/where.go`'s scalar
branch) iterates `for opStr, value := range filterMap` and
parses each key as an operator (`eq`/`in`/`gt`/...). Passing
`{itemIdentifier: {uri: {eq: ...}}}` would hit "Unknown filter
operator" warn-and-continue at the `uri` key — a **silent
drop**, not an error.

So per-property dispatch on the element pseudo-lexicon must
run **BEFORE** the existing scalar-operator loop:

```go
for propName, propEntry := range elementDef.Properties {
    inputValue, present := filterMap[propName]
    if !present { continue }
    switch {
    case propEntry.Type == "ref" && propEntry.Ref == "com.atproto.repo.strongRef":
        leaves, err := extractStrongRefFilter(propName, inputValue.(map[string]interface{}))
        if err != nil { return ... }
        group.Filters = append(group.Filters, leaves...)
    default:
        // existing scalar-operator branch: parses {eq, in, gt, ...}
    }
}
```

`extractStrongRefFilter(fieldName, m)` decomposes
`{uri: {eq: ...}, cid: {eq: ...}}` into one `FieldFilter` per
sub-key with `FieldName: fieldName + "__" + subKey, IsJSON:
true, LexiconType: "string"`. Same helper-style decomposition
pattern as `buildDIDOnlyEqInFilter`. v1 only handles
`com.atproto.repo.strongRef`; follow-up §9.4 generalises when a
second ref type appears.

**Branch ordering inside the extractor**: joined-where check
runs first, then array-where, then filter-registry, then
scalar. Today there's no collision (different fieldNames map
to different registries), but ordering it consistently means
adding a future overlap is a deliberate edit rather than an
accident. Pin this with a test (E11.S4 below — extractor
precedence).

## 6. SQL emission

### 6.1 Sample full SQL for the issue's example

GraphQL input:

```graphql
appCertifiedHypercertsCollection(where: {
  type: { eqi: "project" },
  items: { itemIdentifier: { uri: { eq: "at://did:plc:xxx/org.hypercerts.claim.activity/abc" } } }
}) { edges { node { uri } } }
```

Note: the issue's example uses `appCertifiedHypercertsCollection`
but the lexicon ID is `org.hypercerts.collection`; the field name
on the resolver matches the lexicon ID. The compiled SQL the
resolver builds:

```sql
SELECT r.uri, r.cid, r.did, r.json::text, r.collection, r.rkey,
       r.indexed_at, a.pds
FROM record r
LEFT JOIN actor a ON r.did = a.did
WHERE r.collection = $1                          -- 'org.hypercerts.collection'
  AND lower((r.json->>'type') COLLATE "C") = $2  -- 'project'
  AND EXISTS (
    SELECT 1
    FROM jsonb_array_elements(
      CASE WHEN jsonb_typeof(r.json->'items') = 'array'
           THEN r.json->'items'
           ELSE '[]'::jsonb END
    ) AS e(json)
    WHERE e.json @> $3::jsonb
  )
ORDER BY r.indexed_at DESC, r.uri DESC
LIMIT 21;
```

With `$3 = '{"itemIdentifier":{"uri":"at://did:plc:xxx/..."}}'`.

Parameter binding order (mirrors #87's correlated-EXISTS
ordering): outer leaves first (`$1`=collection, `$2`=lowercased
`type`), then array-element inner leaves (`$3`=containment
literal). No collection-name param needed inside the EXISTS
(unlike #87, which had to qualify `record d` by collection —
here we're iterating `r.json->'items'`, no separate table).

### 6.2 Sanity-check: `buildSingleFilter(alias="e")` output

Inner extraction of `items: {itemIdentifier: {uri: {eq: <uri>}}}`
walks: array-where descriptor lookup → element pseudo-lexicon →
inner `extractFieldFiltersRecursive` against the `#item` element
properties. The `itemIdentifier` property is a `ref` to
`com.atproto.repo.strongRef`; the extractor recognises this and
calls a small `extractStrongRefFilter(fieldName, filterMap)`
helper that decomposes `{uri: {eq: ...}, cid: {eq: ...}}` into
one `FieldFilter` per sub-key with `FieldName: fieldName +
"__" + subKey, IsJSON: true, LexiconType: "string"`. Same
helper-style decomposition pattern as `buildDIDOnlyEqInFilter`.

Resulting AST:

```go
FilterGroup{
  Operator: GroupAND,
  Arrays: []ArrayFilter{{
    FieldName: "items",
    ArrayPath: "r.json->'items'",
    Inner: FilterGroup{
      Operator: GroupAND,
      Filters: []FieldFilter{{
        FieldName:   "itemIdentifier__uri",
        Operator:    OpEq,
        Value:       "at://did:plc:xxx/...",
        IsJSON:      true,
        LexiconType: "string",
        Kind:        KindScalar,
      }},
    },
  }},
}
```

Then `buildSingleFilter(f, paramIdx, "e")` for the `OpEq` JSON
case takes the containment path at filter.go:603-621:

```sql
e.json @> '{"itemIdentifier":{"uri":"at://..."}}'::jsonb
```

The existing `__`-path containment machinery
(`buildNestedContainment` at filter.go:935-947) handles
`"itemIdentifier__uri"` → `{"itemIdentifier": {"uri": <value>}}`
unchanged. Zero new SQL-emission code; the new code is all
AST + extractor wiring + the EXISTS wrapping in
`buildFilterGroupRecursive`.

### 6.3 EXISTS-emission code shape (mirror of #87 §4.3)

```go
// In buildFilterGroupRecursive, after the Joined-loop:
for _, arr := range group.Arrays {
    // Inner clause uses the element alias "e".
    innerClause, innerParams, err := buildFilterGroupClauseWithAlias(arr.Inner, paramIdx, "e")
    if err != nil {
        return "", nil, err
    }
    var existsClause string
    if innerClause == "" {
        // Bare existence: "the array has at least one element".
        existsClause = fmt.Sprintf(
            "EXISTS (SELECT 1 FROM jsonb_array_elements(CASE WHEN jsonb_typeof(%s) = 'array' THEN %s ELSE '[]'::jsonb END) AS e(json))",
            arr.ArrayPath, arr.ArrayPath)
    } else {
        existsClause = fmt.Sprintf(
            "EXISTS (SELECT 1 FROM jsonb_array_elements(CASE WHEN jsonb_typeof(%s) = 'array' THEN %s ELSE '[]'::jsonb END) AS e(json) WHERE %s)",
            arr.ArrayPath, arr.ArrayPath, innerClause)
    }
    clauses = append(clauses, existsClause)
    params = append(params, innerParams...)
    paramIdx += len(innerParams)
}
```

Two SQL-safety notes baked into the emitter (not the
registry, so every future array descriptor inherits):

1. **Non-array `items` values** — `jsonb_array_elements` raises
   `SQLSTATE 22023` if `r.json->'items'` is not an array (null,
   missing, or a scalar/object). The `CASE WHEN jsonb_typeof =
   'array' THEN ... ELSE '[]'::jsonb END` guard short-circuits
   to an empty array so a single corrupt record never bricks
   the query.

2. **Empty-Inner case** is legal and useful — "collections
   with at least one item." Mirrors #87's empty-`Inner`
   joined-existence shape. Pinned by E11.2 below.

### 6.4 Depth budget across the EXISTS boundary

Same intentional reset as #87 §4.3: the inner call to
`buildFilterGroupClauseWithAlias(arr.Inner, ..., "e")`
restarts the SQL-builder depth counter at 0. The extractor
side counts the array boundary as `depth+1` so deeply nested
input requests still reject; the SQL-side reset keeps the
inner's own `_and`/`_or` recursion budget honest. Bounded
overall by the one-level `Arrays` guard (§5.5).

## 7. Tests

### Filter unit tests (`filter_unit_test.go`)

| Test | What it pins |
|---|---|
| **E11.1** `TestBuildFilterGroupClause_ArrayFilter_Eq` | Single ArrayFilter with one inner `eq` leaf produces the EXISTS-jsonb_array_elements-CASE-WHEN shape; inner clause uses `e.json @> ...::jsonb`; correct param binding (inner params first, then no array-side params). |
| **E11.2** `TestBuildFilterGroupClause_ArrayFilter_EmptyInner` | Bare existence (`{items: {}}`) emits `EXISTS (SELECT 1 FROM jsonb_array_elements(...) AS e(json))` without the `WHERE` tail. |
| **E11.3** `TestBuildFilterGroupClause_ArrayFilter_InsideOr` | `_or` group with a normal leaf and an ArrayFilter; both clauses joined by ` OR ` with correct parameter numbering. |
| **E11.4** `TestBuildFilterGroupClause_ArrayFilter_AliasQualifiesColumns` | Inner FieldFilter on `itemWeight` (JSON `OpEq` non-containment via `OpNeq`/`OpGt` to bypass the JSON-containment short-circuit and force the `jsonExtract` path). Pin that `jsonExtract` with `alias="e"` produces `e.json->>'itemWeight'` and (for `__`-paths) `e.json->'itemIdentifier'->>'uri'`. **R2.1 note**: element rows have no column-style references (no `did`/`uri`/`subject_did` columns — only the synthetic `json` column from the `e(json)` alias), so unlike #87's joined-d alias test, this test only pins the JSON path. Inline comment in the test docstring explains the deliberate scope. |
| **E11.5** `TestArrayFilter_CountConditions` | Verify the inner's leaf count rolls up to the outer's `CountConditions()`; without the `Arrays` arm this would return outer-only count. |
| **E11.6** `TestBuildFilterGroupClause_ArrayFilter_CapEnforced` | 19 outer leaves + 1 ArrayFilter with 2 inner leaves = 21 total; assert `BuildFilterGroupClause` returns the "too many filter conditions" error. End-to-end pin of the R1.1 hazard. |
| **E11.7** `TestBuildSingleFilter_LockedKindsRejectArrayAlias` | Feed a `FieldFilter` with `Kind = KindArrayContributor / KindUnionSubject / KindStringSubject` and `alias="e"`; assert error. **R1.11 note**: parameterise over `["d", "e"]` (the two non-`r` aliases) rather than duplicate the #87 cases — the sentinel logic is alias-agnostic, so one parametric loop covers both. |

### Schema tests (`where_test.go`)

| Test | What it pins |
|---|---|
| **E11.S1** `TestArrayWhereRegistry_CollectionItems` | Pin shape: field name `items`, ArrayPath literal `r.json->'items'`, ElementDef literal `item`. Same shape as `TestJoinedWhereRegistry_BadgeAwardBadge`. |
| **E11.S2** `TestBuildWhereInput_CollectionHasItemsFilter` | Schema-introspection-style: `OrgHypercertsCollectionWhereInput.items` field exists, typed as `OrgHypercertsCollectionItemWhereInput`, has the pinned description. |
| **E11.S3** `TestExtractFieldFilters_NestedItemsWhere` | Feed `{items: {itemIdentifier: {uri: {eq: "at://..."}}}}`; assert the resulting FilterGroup has one `Arrays` entry with `FieldName=items`, `ArrayPath=r.json->'items'`, and an inner FilterGroup with one FieldFilter `{FieldName: "itemIdentifier__uri", Operator: OpEq, Value: "at://...", IsJSON: true}`. |
| **E11.S4** `TestExtractFieldFilters_ItemsExtractorPrecedence` | Synthetic registry entry where a fieldName appears in both `joinedWhereRegistry` and `arrayWhereRegistry` for the same lexID; assert joined wins (deterministic, documented). Or, simpler: pin the branch order via a comment-anchored test that asserts the array-where branch is reached only when joined-where lookup misses. |
| **E11.S5** `TestExtractFieldFilters_NestedItemsWhere_DepthCap` | Trace per where.go:263 — depth=0 top, +1 outer `_and`, +1 nested `_and`, +1 `items` array boundary (depth=3, the cap), +1 inner `_and` → **depth=4, rejected**. Minimal payload: `where: {_and: [{_and: [{items: {_and: [{itemWeight: {eq: "1"}}]}}]}]}` (one inner `_and` is enough — adding a second is unreachable). Assert error contains literal substring `"exceeds maximum depth"` (R2.6 — verbatim from the extractor's error format, so a future error-message refactor can't silently weaken the test). |
| **E11.S6** `TestExtractFieldFilters_NestedArrayWhere_Rejected` | Documented-tautology placeholder (same shape as #87's `TestExtractFieldFilters_NestedJoined_Rejected`): the one-level Arrays guard cannot fire today because no array element type has its own array-where registry entry. Test body acknowledges the tautology; flagged in §9 to extend when a second array-where registry entry lands. |

## 8. Behavioral-test catalogue addition (E11)

Add to the checklist table at `docs/behavioral-tests.md:97`:

```markdown
| [E11](#e11) | Collection nested-where on `items` filters by array element | EITHER | issue #88 |
```

Detail section, mirroring E10 byte-for-byte structure:

```markdown
### E11
**Collection nested-where on `items` filters by joined element.**

- **Coverage**: issue #88 — the certified-app reads "what
  project collections contain this cert?" via
  `orgHypercertsCollection(where: { type: { eqi:
  "project" }, items: { itemIdentifier: { uri: { eq: $certUri
  } } } })`. The nested `items` filter translates to an EXISTS
  subquery over `jsonb_array_elements(r.json->'items')`, with
  any-element semantics. Without this the client makes N
  client-side scans per cert page.
- **Target**: EITHER for the wire-level query; LOCAL for the
  `EXPLAIN ANALYZE` check.
- **Preconditions**:
  - `org.hypercerts.collection` lexicon registered. On DEV-
    DEPLOYED already present.
  - At least one collection of type `project` whose `items`
    contains a known cert URI.
- **Steps**:
  1. Verify schema:
     ```bash
     curl -s -X POST $BASE_URL/graphql \
       -H 'Content-Type: application/json' \
       -d '{"query":"{ __type(name:\"OrgHypercertsCollectionWhereInput\") { inputFields { name type { name } } } }"}' \
       | jq '.data.__type.inputFields | map(select(.name == "items"))'
     ```
     Expect type `OrgHypercertsCollectionItemWhereInput`.
  2. Issue the exact query:
     ```bash
     curl -s -X POST $BASE_URL/graphql \
       -H 'Content-Type: application/json' \
       -d '{"query":"{ orgHypercertsCollection(where: { type: { eqi: \"project\" }, items: { itemIdentifier: { uri: { eq: \"<cert-uri>\" } } } }, first: 50) { edges { node { uri } } } }"}' \
       | jq
     ```
  3. Cross-check against the 2-call workaround (pull all
     project collections; client-side filter on
     `items[*].itemIdentifier.uri`).
  4. (LOCAL only) `EXPLAIN ANALYZE` the equivalent SQL (see
     §6.1 above). Plan should show `Function Scan on
     jsonb_array_elements` per outer row; no secondary record
     scan.
- **Expected**:
  - Step 1: `items` field present, typed correctly.
  - Step 2: returns the collections whose `items` array
    contains a matching strongRef. No GraphQL errors.
  - Step 3: identical URI sets.
  - Step 4: per-row Function Scan; no scan of any second
    collection.
- **Composition check (optional)**: an `_or` inside the inner —
  ```graphql
  { orgHypercertsCollection(where: {
      items: { _or: [
        { itemIdentifier: { uri: { eq: "<uri-1>" } } },
        { itemIdentifier: { uri: { eq: "<uri-2>" } } }
      ] }
    }, first: 5) { edges { node { uri } } } }
  ```
  Confirms `_or` is wired inside the element pseudo-lexicon.
- **Cleanup**: none.
- **Refs**: issue #88; AST in
  `internal/database/repositories/filter.go` (`ArrayFilter`,
  search for "Arrays"); registry at
  `internal/graphql/schema/where.go` `arrayWhereRegistry`;
  extractor branch at `extractFieldFiltersRecursive`;
  builder extension at
  `internal/graphql/schema/builder.go` `buildWhereInputTypes`
  pass 2.
```

## 9. Known follow-ups

| # | Follow-up | Trigger to act |
|---|---|---|
| 9.1 | **Auto-derive array-element inputs from lexicon shape** instead of registry-required. The lexicon already declares `items.items.ref = #item`; the schema generator could detect array-of-object/array-of-ref properties and synthesise the input type automatically. Cleaner than the registry; gives up the pinned-description hook. | 3+ registry entries land, or a contributor proposes a one-off array filter that requires zero policy. |
| 9.2 | **Hybrid `@>` containment-friendly emission** (shape (c) in the design brief). When the inner FilterGroup contains only `KindScalar` `OpEq` leaves with no `_or` and no `__`-paths that span multiple branches, emit `r.json->'items' @> '[...]'::jsonb` instead of EXISTS+jsonb_array_elements. Index-friendly via GIN on `(json->'items') jsonb_path_ops`. | A specific array-where query measured > 100ms on dev EXPLAIN ANALYZE, or collection count > 10⁴. Add the GIN index in the same commit as the emission change. |
| 9.3 | **GIN index on `(json->'items') jsonb_path_ops`** for the collection lexicon specifically. Independent of 9.2 — useful any time we measure a slow array-where, even before adopting the containment emission. New migration; ~5 LOC. | EXPLAIN ANALYZE shows seq-scan-of-collection becoming the dominant cost. |
| 9.4 | **Filterable refs/unions on element properties beyond strongRef.** v1 handles `itemIdentifier:com.atproto.repo.strongRef` via the `StrongRefFilterInput` synthesis. Other ref types would need similar per-target synthesis (e.g. a future `app.certified.defs#did` ref → `DIDFilterInput`). | Second array-where entry lands whose element has a non-strongRef ref property. |
| 9.5 | **Extend E11.S6 (`TestExtractFieldFilters_NestedArrayWhere_Rejected`) with a real two-level payload** when a second array-where registry entry lands whose element type itself has another array-where entry. Today the guard is unreachable; mirrors #87 IR1.7. | Second array-where entry. |
| 9.6 | **Auto-derived "all elements match" semantics** via `NOT EXISTS (... WHERE NOT inner)` inversion, exposed as `itemsAll` or a `_all: true` modifier on the inner. No client wants it today. | A client query requests "every element satisfies." |
| 9.7 | **Multi-level array-of-arrays** — nested ArrayFilter inside another ArrayFilter's Inner. Bounded out today by the extractor; deferred for the same planner-cost reason as #87's nested-joined bound. | A use case appears. |
| 9.8 | **StrongRef filter inputs registered as a first-class scalar dispatch in `FilterInputForLexiconType`** — today the `StrongRefFilterInput` lives next to `DIDFilterInput` in `types/filters.go` but only used inside the array-element synthesis. Once a top-level record property of type `ref: com.atproto.repo.strongRef` becomes filterable directly, dispatch it through `FilterInputForLexiconType`. | First record property of strongRef type wants top-level filtering. |

## 10. Rollback

- **No migrations.** Pure code changes.
- **Per-commit bisectability** (sequencing in §11): each
  commit is independently revertable. The AST commit
  introduces `ArrayFilter` + EXISTS emission but no producer;
  the schema commit wires producers; the test/catalogue
  commits add coverage. A `git revert` of any single commit
  leaves the tree compiling and the existing test suite green.
- **Single revert drops everything.** All four commits live on
  staging; a `git revert <merge-commit>` (or revert of the
  staging→main merge) restores pre-#88 behaviour with no
  database state to undo.
- **Risk of partial revert**: reverting the schema commit
  without reverting the AST commit leaves an unreachable
  `Arrays` arm in `CountConditions` / `buildFilterGroupRecursive`
  — harmless dead code, not a regression. Reverting only the
  AST commit while leaving the schema commit fails the build
  (the schema commit references `repositories.ArrayFilter`).
  Document the revert-in-pairs requirement in the PR body.

## 11. Sequencing

Commit order on `staging` (mirrors #87 §10):

1. **Plan + plan-review** docs (`docs/issue-88/plan.md`,
   `docs/issue-88/review-round-1.md` after reviewers run).
2. **`ArrayFilter` AST + `CountConditions`/`IsEmpty` extension
   + EXISTS emission** — the SQL-side change in
   `internal/database/repositories/filter.go`. No producer
   yet; new tests E11.1–E11.7 cover the new code paths.
   Verified by existing test suite passing untouched plus the
   new tests.
3. **`arrayWhereRegistry` + `arrayWhereDescriptor` +
   `collectionItemsArrayDescription` + `StrongRefFilterInput`
   + builder Pass 2 extension + extractor branch** — all the
   schema-side wiring in
   `internal/graphql/schema/where.go`,
   `internal/graphql/schema/builder.go`,
   `internal/graphql/types/filters.go`. Tests E11.S1–E11.S6.
   Smoke: schema introspection on dev shows
   `OrgHypercertsCollectionWhereInput.items` typed as
   `OrgHypercertsCollectionItemWhereInput`.
4. **Behavioral test catalogue entry E11** —
   `docs/behavioral-tests.md` table row + detail section.
5. **Impl-review round 2 doc + Draft PR.** Stop here; user
   merges.

After PR merges + redeploy: verify on dev with the issue's
exact query and the EXPLAIN ANALYZE half of E11.

## 12. Acceptance criteria

1. `GOARCH=arm64 go build ./...` clean.
2. `GOARCH=arm64 go vet ./...` clean.
3. `GOARCH=arm64 golangci-lint run ./...` returns `0 issues.`
4. `CGO_ENABLED=1 GOARCH=arm64 go test -race -short ./internal/database/... ./internal/graphql/...` passes — includes E11.1–E11.7 + E11.S1–E11.S6.
5. Schema introspection on `OrgHypercertsCollectionWhereInput.items` returns type `OrgHypercertsCollectionItemWhereInput` with the pinned description.
6. The issue's example query returns collections whose `items` contain the requested cert URI and excludes others.
7. (LOCAL) `EXPLAIN ANALYZE` shows per-row `Function Scan on jsonb_array_elements`, no secondary record-table scan.

## 13. Open questions for the operator

1. **`StrongRefFilterInput` placement** — propose
   `internal/graphql/types/filters.go` next to `DIDFilterInput`,
   exported so a future record property of strongRef type can
   dispatch through `FilterInputForLexiconType` (follow-up
   §9.8). Confirm placement?
2. **Pre-merge dev test** — same offer as #87 §9.2: a single
   `curl` against dev after a temporary build, or rely on unit
   + E11 catalogue coverage? Default: rely on coverage; you
   merge, redeploy, verify with E11.
3. ~~**Field-name precedent for the synthesised inner type**~~ —
   **DECIDED in review-round-1 (R2.3)**: use
   `OrgHypercertsCollectionItemWhereInput`. The response-side
   element type is already `OrgHypercertsCollectionItem`, so
   the `<RecordType>WhereInput` precedent applies directly.
   Drops the stuttery "ItemsItem" segment.

## 14. PR body template

For the implementer to paste at PR-open time. Mirrors PR #90's
shape (issue #87):

```markdown
## Summary

Adds a generic array-element nested-where on lexicon array
fields. First instance: `OrgHypercertsCollectionWhereInput.items`,
unblocking the certified-app's cross-DID "projects containing
this cert" read in one indexer round-trip (replaces today's
PDS-only same-DID scan + client-side filter).

- **Mechanism** — registry-driven `arrayWhereRegistry` +
  per-element synthesised `*WhereInput` types + EXISTS
  correlated subquery over `jsonb_array_elements`, with element
  rows aliased as `e(json)` so the existing `jsonExtract` +
  JSONB containment machinery emits the inner predicates
  unchanged.
- **Surface** — 1 new GraphQL input shape
  (`OrgHypercertsCollectionItemWhereInput`), 1 new registry
  (`arrayWhereRegistry`), 1 new helper (`extractStrongRefFilter`),
  1 new exported type (`StrongRefFilterInput`), ~80 LOC in the
  filter package, ~140 LOC in the schema package.
- **Safety** — `jsonb_typeof = 'array'` guard on the array
  path so corrupt records never brick the query;
  `CountConditions` walks `Arrays` so the per-request filter
  cap can't be bypassed via array-element nesting;
  one-level-bound `Arrays.Arrays` guard.

Follows the deep-flow process:
- Plan: `docs/issue-88/plan.md`
- Plan review (2 parallel reviewers): `docs/issue-88/review-round-1.md`
- Implementation review (2 parallel reviewers): `docs/issue-88/review-round-2.md`

## Breaking changes

None. The new `items` field on
`OrgHypercertsCollectionWhereInput` is purely additive.

## Out of scope

- `@>` containment-friendly emission for pure-equality inner
  predicates (follow-up §9.2 in the plan; trigger:
  measurement showing the EXISTS path becomes the dominant
  cost).
- GIN index on `(json->'items') jsonb_path_ops` (follow-up
  §9.3; trigger: same).
- Auto-derivation of array-element inputs from lexicon shape
  alone (follow-up §9.1; trigger: 3+ registry entries).
- Multi-level array-of-arrays nesting and "all elements
  match" semantics (follow-up §9.6, §9.7).

## Test plan

- [x] `go build ./...` clean
- [x] `go vet ./...` clean
- [x] `golangci-lint run ./...` clean
- [x] `go test -race -short ./internal/database/... ./internal/graphql/...` passes (~13 new tests across both packages)
- [x] Behavioral-tests catalogue updated (E11) with wire-level + EXPLAIN ANALYZE halves
- [ ] CI green on this PR
- [ ] After merge: redeploy dev, verify with the certified-app's exact cert-projects query

Refs: #88
```
