# 02 — Reuse / Consistency Findings

Date: 2026-05-18 (overnight pass).
Scope: 8 lenses from the brief (within-file dup, across-file dup, registry
alignment, deprecated/one-shot, defensive-impossible-state, comment noise,
single-caller helpers, pattern divergence). Cap: 20 findings.
Calibration: high = duplication that caused or will cause a bug; medium =
duplication that adds noise + maintenance cost; low/nit = preference.
Bias: deletion over abstraction.

Per-finding format: title, severity, locations, problem (with excerpt),
why-it-matters, proposed fix, effort, fix-risk, reversibility.

Explicitly out of scope (per directive): consolidating
`joinedWhereRegistry` / `arrayWhereRegistry` / `derivedFieldRegistry`
into a single generic registry. Plan §9.4 in each defers that until
a 2nd-or-3rd of the SAME kind appears.

---

### R-1: `BuildFieldFilterClause` is dead production code (no callers anywhere)
**Severity:** medium
**Locations:** `internal/database/repositories/filter.go:596-639`
**Problem:** The "legacy non-recursive path" `BuildFieldFilterClause`
has zero call sites in the entire active tree — not even in tests:

```bash
$ grep -rn "BuildFieldFilterClause" internal cmd
internal/database/repositories/filter.go:370:  # comment referencing the function
internal/database/repositories/filter.go:596:  // BuildFieldFilterClause builds...
internal/database/repositories/filter.go:603:  func BuildFieldFilterClause(...)
# (no other matches)
```

The function exists, takes 35 LOC, duplicates the leaf-filter loop
from `buildFilterGroupRecursive`, and is exported only for callers
that never materialised. The header comment ("Used by callers that
don't compose _and/_or") is now false — every caller composes through
`FilterGroup`.

**Why this matters here:** The duplicate body has already drifted
once — `buildFilterGroupRecursive` added the value-level `f.Validate()`
call at line 376 with an explicit comment ("Run value-level validation
here so the recursive path matches `BuildFieldFilterClause`'s
contract"). The asymmetry comment is now load-bearing for nothing.
Future edits to the leaf-loop must remember a function no caller
will ever exercise.

**Proposed fix:** Delete `BuildFieldFilterClause` (lines 596-639) and
the comment at lines 369-375 that explains the contract-mirroring.
The unit-test file (`filter_unit_test.go`) almost certainly has tests
against it — delete those too. The function is exported, so check
external usage in the certified-app/client repo before deletion;
that's a 30-second `gh search code` against the org.

**Effort:** S
**Risk of fix:** low (zero in-tree callers; one external check)
**Reversibility:** easy (git revert)

---

### R-2: `buildBadgeAwardSubjectFilter` and `buildStringSubjectFilter` are byte-identical modulo one constant
**Severity:** medium
**Locations:** `internal/database/repositories/filter.go:953-965` and
`internal/database/repositories/filter.go:987-1009`
**Problem:** Side-by-side:

```go
// buildBadgeAwardSubjectFilter (953)
func buildBadgeAwardSubjectFilter(f FieldFilter, paramIdx int) (string, []interface{}, int, error) {
    switch f.Operator {
    case OpEq:
        param := fmt.Sprintf("$%d", paramIdx)
        return fmt.Sprintf("r.subject_did = %s", param), []interface{}{f.Value}, paramIdx + 1, nil
    case OpIn:
        param := fmt.Sprintf("$%d", paramIdx)
        values, _ := extractInValues(f.Value)
        return fmt.Sprintf("r.subject_did = ANY(%s::text[])", param), []interface{}{values}, paramIdx + 1, nil
    default:
        return "", nil, paramIdx, fmt.Errorf("operator %s not supported on badge-award subject filter", f.Operator)
    }
}

// buildStringSubjectFilter (987)
func buildStringSubjectFilter(f FieldFilter, paramIdx int) (string, []interface{}, int, error) {
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

The only differences: (1) the indexable expression
(`r.subject_did` is a column, `r.json->>'subject'` is a JSON
extraction); (2) the error-message tag string. Body shape, operator
dispatch, parameter formatting, return tuple — all identical.
~12 lines of body, duplicated.

`buildContributorFilter` (903-923) is structurally similar but uses
`@>`/`&&` instead of `=`/`= ANY`, so it is NOT a duplicate of these
two — only badge-award-subject and string-subject collapse.

**Why this matters here:** Two specific risks. (1) The "byte-for-byte
index expression match" contract is repeated as a const-and-comment
in `buildStringSubjectFilter` but lives implicitly as a string-literal
in `buildBadgeAwardSubjectFilter`. The next operator addition (say
`neq`) would need to be patched in both places identically, and the
discipline of "edit both" has already silently degraded once for
the comment block. (2) A future "filter by `r.cid`" or "filter by a
new bare-column generated for a different lexicon" would naturally
copy-paste one of these two and now there are three.

**Proposed fix:** Extract a single helper:

```go
func buildIndexedScalarEqFilter(expr string, errLabel string, f FieldFilter, paramIdx int) (string, []interface{}, int, error) {
    switch f.Operator {
    case OpEq:
        param := fmt.Sprintf("$%d", paramIdx)
        return fmt.Sprintf("%s = %s", expr, param), []interface{}{f.Value}, paramIdx + 1, nil
    case OpIn:
        param := fmt.Sprintf("$%d", paramIdx)
        values, _ := extractInValues(f.Value)
        return fmt.Sprintf("%s = ANY(%s::text[])", expr, param), []interface{}{values}, paramIdx + 1, nil
    default:
        return "", nil, paramIdx, fmt.Errorf("operator %s not supported on %s filter", f.Operator, errLabel)
    }
}
```

Then both call sites become a one-liner with the indexed expression
hardcoded inline (preserving the "must match migration N expression
byte-for-byte" comment at each caller). NOT a generic abstraction —
the pinned-expression comment stays at the call site where the
migration-coupling lives.

**Effort:** S
**Risk of fix:** low (no behaviour change, byte-identical SQL output;
verified by existing index-expression drift tests)
**Reversibility:** easy

---

### R-3: `GetByCollection` and `GetByCollectionWithCursor` are dead production code
**Severity:** medium
**Locations:** `internal/database/repositories/records.go:283-285`
(`GetByCollection`) and `internal/database/repositories/records.go:290-314`
(`GetByCollectionWithCursor`)
**Problem:** Both are only called from `records_test.go`:

```bash
$ grep -rn "Records.GetByCollection\b\|GetByCollectionWithCursor\b" --include="*.go" \
    | grep -v worktrees | grep -v "records.go\|_test.go"
# (no output)
```

`GetByCollection` (3-line wrapper) and `GetByCollectionWithCursor`
(~25 lines) both predate `GetByCollectionFiltered`, which is the
canonical path every resolver now takes. The "fast path" inside
`GetByCollectionFiltered` calls `GetByCollectionWithKeysetCursor`,
not these two — see records.go:492-494.

**Why this matters here:** Two read paths that look like the
production code path but aren't. A future contributor reading the
repo's surface will see four "get records by collection" methods
(`GetByCollection`, `GetByCollectionWithCursor`,
`GetByCollectionWithKeysetCursor`, `GetByCollectionFiltered`) and
not know which is canonical. Two of the four are exercised only by
their own tests, so test coverage looks misleadingly broad.

**Proposed fix:** Delete `GetByCollection` (records.go:283-285),
`GetByCollectionWithCursor` (records.go:287-314), and their
corresponding tests (`TestRecordsRepository_GetByCollection*` in
records_test.go). Keep `GetByCollectionWithKeysetCursor` — it's
used by the fast path. Same external-usage check as R-1: grep
the certified-app client before deleting (these are exported).

**Effort:** S
**Risk of fix:** low (zero in-tree callers outside their own tests;
one external check)
**Reversibility:** easy

---

### R-4: `RecordsRepository.Insert` (non-WithParams) is dead production code
**Severity:** low
**Locations:** `internal/database/repositories/records.go:129-133`
**Problem:** The thin positional wrapper:

```go
func (r *RecordsRepository) Insert(ctx context.Context, uri, cid, did, collection, jsonData string) (InsertResult, error) {
    return r.InsertWithParams(ctx, InsertParams{
        URI: uri, CID: cid, DID: did, Collection: collection, JSONData: jsonData,
    })
}
```

Production callers all use `InsertWithParams` directly
(`internal/ingestion/processor.go:211` is the only non-test caller of
either). `Insert` is called from many test files
(`records_filter_test.go`, `records_labels_test.go`, etc.).

**Why this matters here:** Marginal — header comment ("Kept as the
thin positional wrapper for historical callers") admits this. The
asymmetry between production going through `InsertParams` and tests
using the positional form means the test surface doesn't exercise the
parameter-struct path the production path takes. A future test
regression on the positional API would not catch a production-path
break.

**Proposed fix:** Either (a) delete `Insert` and update the ~30 test
sites to use `InsertWithParams`, or (b) leave it — it's a 5-LOC
wrapper and the tests genuinely benefit from the brevity. My
recommendation is (b) and add a one-line comment noting that
production callers must use `InsertWithParams`. The duplication cost
here is not worth the test-site churn.

**Effort:** S (option b) / M (option a, ~30 test edits)
**Risk of fix:** low
**Reversibility:** easy

---

### R-5: Three repository methods build IN-clause placeholders with identical 6-line loop
**Severity:** medium
**Locations:** `internal/database/repositories/records.go:266-269`
(`GetByURIs`), `internal/database/repositories/records.go:879-883`
(`GetCollectionStatsFiltered`),
`internal/database/repositories/records.go:1018-1022`
(`GetCIDsByURIs` inner batch), `internal/database/repositories/records.go:1067-1071`
(`GetExistingCIDs` inner batch). Same shape echoes in
`internal/database/repositories/records.go:520-523` (authors loop
inside `GetByCollectionFiltered`) and
`internal/database/repositories/records.go:642-645` (pdsPhs loop).
**Problem:** Six near-identical instances of the same idiom:

```go
phs := make([]string, len(items))
params := make([]database.Value, len(items))
for i, item := range items {
    phs[i] = fmt.Sprintf("$%d", i+1)
    params[i] = database.Text(item)
}
// then: " ... IN (" + strings.Join(phs, ", ") + ")"
```

(The two inside `GetByCollectionFiltered` use a `ph()` closure that
increments a counter instead of `i+1`, but the shape is the same.)

**Why this matters here:** Each instance duplicates the same
"start-at-1 / Text(item)" assumption. A subtle bug surface: any of
them silently mis-numbering the placeholders (e.g., starting at 0)
would produce an invalid SQL error. Centralising the loop removes
six edit sites if `database.Value` ever needs an alternative
constructor (`Bytea`, `Numeric`, etc.).

**Proposed fix:** Add a tiny helper in `internal/database/`:

```go
// TextINClause builds an "$N, $N+1, ..." placeholder string and the
// matching []database.Value slice for a string IN clause. paramStart
// is 1-based.
func TextINClause(items []string, paramStart int) (string, []database.Value) {
    phs := make([]string, len(items))
    params := make([]database.Value, len(items))
    for i, s := range items {
        phs[i] = fmt.Sprintf("$%d", paramStart+i)
        params[i] = database.Text(s)
    }
    return strings.Join(phs, ", "), params
}
```

Apply to the four "standalone" sites (`GetByURIs`,
`GetCollectionStatsFiltered`, `GetCIDsByURIs`, `GetExistingCIDs`).
The two `GetByCollectionFiltered` loops use the `ph()` closure
because they interleave multiple variable-length slices into one
parameter run — those stay as-is (different problem).

**Effort:** S
**Risk of fix:** low (mechanical extraction, byte-identical SQL)
**Reversibility:** easy

---

### R-6: `GetCIDsByURIs` and `GetExistingCIDs` are 47 lines of near-identical batched query code
**Severity:** medium
**Locations:** `internal/database/repositories/records.go:1002-1047`
(`GetCIDsByURIs`) and `internal/database/repositories/records.go:1051-1096`
(`GetExistingCIDs`)
**Problem:** Both functions implement the same pattern: batch-of-900 IN
query, with result accumulation. The only differences:
- input slice (URIs vs CIDs)
- SELECT column list (`uri, cid` vs `cid`)
- result type (`map[string]string` vs `map[string]bool`)
- scan target

Body LOC: 46 each, ~42 of which are byte-identical (loop variable
names included).

**Why this matters here:** Both are exercised in production by the
backfill (`internal/backfill/backfill.go:609,616`), and a third
"batch DID lookup" or "batch CID-by-URI lookup" would naturally
become a third near-identical copy. The pattern is hand-rolled in
six places already (this finding + R-5); a small helper would
collapse them.

**Proposed fix:** Extract `batchSelectByColumn`:

```go
// batchSelectByColumn runs a SELECT in batches of SQLParamBatchSize
// against an IN clause on a single column, invoking scan(rows) for
// each batch. column is the column name (validated by the caller);
// selectCols is the projected column list. paramStart is 1.
func (r *RecordsRepository) batchSelectByColumn(
    ctx context.Context, table, selectCols, column string, values []string,
    scan func(*sql.Rows) error,
) error { ... }
```

Then `GetCIDsByURIs` and `GetExistingCIDs` reduce to ~10 lines each:
build the scan closure that populates the local map. NOT a public
extraction — keep it on `*RecordsRepository`. Reduces ~90 LOC to
~50.

**Effort:** S
**Risk of fix:** low (signature changes are internal; tests are
behavioural)
**Reversibility:** easy

---

### R-7: Three near-identical lookup helpers — but unification is out of scope per directive
**Severity:** nit (informational; documenting the conscious non-finding)
**Locations:** `internal/graphql/schema/where.go:100-106`
(`lookupJoinedWhereDescriptor`), `internal/graphql/schema/where.go:145-151`
(`lookupArrayWhereDescriptor`), `internal/graphql/schema/where.go:192-198`
(`lookupFilterDescriptor`)
**Problem:** Each helper is structurally identical:

```go
func lookupXDescriptor(lexID, fieldName string) (XDescriptor, bool) {
    if byField, ok := xRegistry[lexID]; ok {
        d, ok := byField[fieldName]
        return d, ok
    }
    return XDescriptor{}, false
}
```

Three functions, ~6 LOC each. With Go 1.21+ generics this would be
one helper.

**Why this matters here:** It doesn't, today. The brief is explicit:
"DO NOT propose consolidating those three registries tonight." A
generic `lookup[T]` helper would unify them, but the moment that
exists, the next contributor will reach for it as the abstraction
point that pulls in shared descriptor interfaces, validation, …
That is the wedge the deferral exists to prevent.

**Proposed fix:** None. Leaving as-is is the right call per the
guidance. Flag for the morning brain so the operator knows the
lens looked here and consciously skipped it. Re-evaluate when a
fourth-registry candidate appears (the directive's own escape
hatch).

**Effort:** N/A
**Risk of fix:** N/A
**Reversibility:** N/A

---

### R-8: `extractFieldFiltersRecursive` and `extractElementFilters` duplicate the scalar-operator dispatch block (~35 LOC)
**Severity:** medium
**Locations:** `internal/graphql/schema/where.go:510-545` (scalar
loop inside `extractFieldFiltersRecursive`) and
`internal/graphql/schema/where.go:779-813` (scalar loop inside
`extractElementFilters`).
**Problem:** Both functions iterate `filterMap`, dispatch through
`parseOperator`, handle the `isNull` branch, and emit a
`repositories.FieldFilter` with the same fields:

```go
// extractFieldFiltersRecursive:510-545
for opStr, value := range filterMap {
    op, isNullOp := parseOperator(opStr)
    if isNullOp {
        boolVal, ok := value.(bool)
        if !ok { continue }
        f := repositories.FieldFilter{
            FieldName: fieldName, IsNull: &boolVal,
            IsJSON: isJSON, LexiconType: lexiconType,
        }
        if err := f.Validate(); err != nil {
            return repositories.FilterGroup{}, fmt.Errorf("field %q: %w", fieldName, err)
        }
        group.Filters = append(group.Filters, f)
        continue
    }
    if op == "" {
        slog.Warn("Unknown filter operator", "field", fieldName, "op", opStr)
        continue
    }
    f := repositories.FieldFilter{
        FieldName: fieldName, Operator: op, Value: value,
        IsJSON: isJSON, LexiconType: lexiconType,
    }
    if err := f.Validate(); err != nil {
        return repositories.FilterGroup{}, fmt.Errorf("field %q, op %q: %w", fieldName, opStr, err)
    }
    group.Filters = append(group.Filters, f)
}

// extractElementFilters:779-813 — same body, only diff is the hard-coded
// `IsJSON: true` (vs `IsJSON: isJSON`) and the LexiconType derivation
// path (`effectiveType(prop)` vs a pre-set variable).
```

The blocks are 90% identical line-for-line.

**Why this matters here:** A drift here would silently change input
acceptance rules between the outer where and the array-element
inner. The error-message format is already subtly different
("field %q, op %q" outer vs same inner — actually identical here,
but the next edit could split them). And the directive on issue #88
plan §9.4 (R1.7) explicitly called out the precedence ordering as
"deliberate edit rather than accident" — the duplicated dispatch
loop is the place that future deliberate edit will need to land in
TWO places.

**Proposed fix:** Extract one helper:

```go
// appendScalarOpFilters parses opStr→op pairs from filterMap into the
// group's Filters. fieldName, isJSON, lexiconType are fixed for the field.
func appendScalarOpFilters(
    group *repositories.FilterGroup,
    fieldName string, isJSON bool, lexiconType string,
    filterMap map[string]interface{},
) error { ... }
```

Call from both extractors. ~35 LOC → ~10 LOC at each call site;
~30 LOC total saved; one place to edit when adding a new operator
or changing error format.

**Effort:** S
**Risk of fix:** low (signature mechanical, behaviour byte-identical)
**Reversibility:** easy

---

### R-9: `if rec.PDS != "" { ... } else { = nil }` block duplicated three times in builder.go
**Severity:** low
**Locations:** `internal/graphql/schema/builder.go:723-727`
(inside `resolveRecordConnection`'s edge loop) and
`internal/graphql/schema/builder.go:1007-1011`
(inside `createSingleRecordResolver`).
**Problem:** Same 5-line block repeated:

```go
if rec.PDS != "" {
    nodeMap["pds"] = rec.PDS
} else {
    nodeMap["pds"] = nil
}
```

The header comment ("pds is nullable in the schema. Empty string
means 'actor row had no resolved pds' — surface as GraphQL null so
clients can distinguish 'unknown' from 'set'") is at line 720-722
in one location and absent at the other.

**Why this matters here:** Low-leverage but real: the empty-string-vs-nil
mapping is a contract between the DB (NULL via COALESCE-to-empty-string
in `actors.go:138`) and the GraphQL surface. Two encoders means two
places that could drift if the storage convention changes (say to a
genuine sql.NullString).

**Proposed fix:** Extract a one-line helper:

```go
// nullablePDS returns rec.PDS or nil for empty, suitable for assigning
// directly to a GraphQL-nullable map entry.
func nullablePDS(rec *repositories.Record) interface{} {
    if rec.PDS == "" {
        return nil
    }
    return rec.PDS
}
```

Both call sites become `nodeMap["pds"] = nullablePDS(rec)`. Or
inline the ternary equivalent. Keep the explanatory comment at the
helper, not at each call site.

**Effort:** S
**Risk of fix:** low
**Reversibility:** easy

---

### R-10: `parseLabelFilter` has three near-identical truncating loops
**Severity:** low
**Locations:** `internal/graphql/schema/builder.go:797-810`
(labelerDids), `internal/graphql/schema/builder.go:812-823`
(labels Include), `internal/graphql/schema/builder.go:824-835`
(excludeLabels).
**Problem:** Three copy-paste blocks differing only in the destination
slice and the cap constant:

```go
if raw, ok := args["labelerDids"].([]interface{}); ok {
    for _, v := range raw {
        if len(filter.LabelerSrcs) >= MaxLabelFilterLabelers { break }
        if s, ok := v.(string); ok && s != "" {
            filter.LabelerSrcs = append(filter.LabelerSrcs, s)
        }
    }
    if len(raw) > MaxLabelFilterLabelers {
        slog.Warn("GraphQL: labelerDids argument truncated", ...)
    }
}
// ...same shape for Include and Exclude...
```

Trailing log-warn for over-cap inputs is at the bottom for the two
label lists but inlined for labelerDids; the loop body is otherwise
identical.

**Why this matters here:** Low — these are stable filters. But this
is the kind of block that a future "newField on the label filter"
would add a fourth copy of without thinking. The over-cap log is
already slightly asymmetric (labelerDids logs inside the if-block,
labels logs after the loop), which is the first sign of drift.

**Proposed fix:** Extract a tiny helper:

```go
// appendStringArgWithCap pulls a string list from a graphql arg, drops
// empty strings, and caps the result. Returns the (possibly truncated)
// list and whether the input exceeded the cap.
func appendStringArgWithCap(args map[string]interface{}, key string, cap int) (out []string, truncated bool) {
    raw, ok := args[key].([]interface{})
    if !ok { return nil, false }
    for _, v := range raw {
        if len(out) >= cap { break }
        if s, ok := v.(string); ok && s != "" {
            out = append(out, s)
        }
    }
    return out, len(raw) > cap
}
```

Then `parseLabelFilter` becomes three 4-line blocks calling the
helper. ~40 LOC → ~20 LOC; symmetric log handling.

**Effort:** S
**Risk of fix:** low (behaviour byte-identical)
**Reversibility:** easy

---

### R-11: `ActorsRepository.GetByDID` and `GetByHandle` are clones with one substituted WHERE column
**Severity:** low
**Locations:** `internal/database/repositories/actors.go:133-147`
(`GetByDID`) and `internal/database/repositories/actors.go:149-163`
(`GetByHandle`).
**Problem:** 14-line functions that differ only in one substring:

```go
err := r.db.QueryRow(ctx,
    "SELECT did, handle, indexed_at::text, COALESCE(pds, '') FROM actor WHERE did = $1",
    ...)
// vs
err := r.db.QueryRow(ctx,
    "SELECT did, handle, indexed_at::text, COALESCE(pds, '') FROM actor WHERE handle = $1",
    ...)
```

**Why this matters here:** Low-leverage but worth flagging because
the SELECT column list is duplicated here too — if a new column
(e.g. `created_at`) lands on `actor`, both functions need the
identical edit, and the actor-scan call sites in records.go (which
also COALESCE on pds) would also need it. The drift surface is
small but real.

**Proposed fix:** Extract `getActorBy(column, value)` as a private
helper sharing the column list and parse logic, then both `GetByDID`
and `GetByHandle` become one-liner wrappers selecting the column.
Or just leave it — 14 LOC × 2 is genuinely below the "abstract"
threshold, and the named methods document the intent. Honest
empties OK per the brief; flagging as nit.

**Effort:** S
**Risk of fix:** low
**Reversibility:** easy

---

### R-12: `ValidateFieldName` runs twice on the same FieldFilter in the recursive path
**Severity:** low
**Locations:** `internal/database/repositories/filter.go:376` (calls
`f.Validate()`, which internally calls `ValidateFieldName` at line
499) and `internal/database/repositories/filter.go:659` (calls
`ValidateFieldName(f.FieldName)` again at the top of
`buildSingleFilter`).
**Problem:** Within `buildFilterGroupRecursive`'s leaf loop:

1. Line 376: `f.Validate()` → which internally line 499 calls
   `ValidateFieldName(f.FieldName)`.
2. Line 379: `buildSingleFilter(f, ...)` → line 659 calls
   `ValidateFieldName(f.FieldName)` AGAIN.

The schema-side extractor (`where.go:523, 541, 792, 809, 861`) ALSO
calls `f.Validate()` per filter at extraction time before the SQL
layer ever sees it. So each FieldFilter has `ValidateFieldName` run
on it ~3 times for a typical query.

**Why this matters here:** Marginal performance cost (regex match
per filter × ~3 = sub-millisecond), but the duplicated
"defensive-validation at every layer" idiom is the kind of accretion
the directive calls out as a deletion target. The schema-side
validation is the canonical authoritative check; the SQL layer
re-validating is defensive against direct repository callers.

**Proposed fix:** Two options:

(a) Delete the redundant `ValidateFieldName` call at filter.go:659
(the top of `buildSingleFilter`). Justification: every public entry
point into the SQL builder (`BuildFilterGroupClause`) already calls
`f.Validate()` for every leaf via `buildFilterGroupRecursive` line
376. The defensive check at line 659 is for internal callers — but
there are none other than the recursive path itself.

(b) Leave both — the redundancy is genuinely defensive against a
future internal caller adding a path that skips `f.Validate()`. The
performance cost is negligible.

My recommendation is (a) with a comment at `buildSingleFilter`
noting "Callers must invoke f.Validate() first." The other
defensive-redundancy in this file (sentinel at line 665 against
nested-subquery misuse) stays — it's guarding a different invariant
that the validator does not.

**Effort:** S
**Risk of fix:** low (the schema-extractor and recursive-builder
both validate; removing the third check loses only defence against
a hypothetical future caller bypassing both)
**Reversibility:** easy

---

### R-13: `// TODO: Add GetByCollectionSortedWithKeysetCursor for non-default sorts.` — stale or live?
**Severity:** nit
**Locations:** `internal/graphql/schema/builder.go:679`
**Problem:** A TODO comment from an earlier sort-aware-keyset push
that never landed:

```go
// For now, sorting is still using indexed_at keyset cursor through
// the existing GetByCollectionFiltered method. Full sort-aware keyset
// pagination (Phase 2.5 of the plan) requires more repository changes.
// This wires up the sort arguments and cursor format; the SQL sort
// is applied when sortField == "indexed_at" (default case).
//
// TODO: Add GetByCollectionSortedWithKeysetCursor for non-default sorts.
records, err := repos.Records.GetByCollectionFiltered(...)
```

The "Phase 2.5 of the plan" is unattributed (which plan?), and
`GetByCollectionFiltered` does actually handle custom sorts now
(records.go:554-572 builds the sort expression from `sortOpt`). The
TODO appears to be outdated.

**Why this matters here:** Lo. The TODO points at a non-existent
follow-up that's already been done. A future contributor reading
this will spend time tracing the "Phase 2.5" reference for nothing.

**Proposed fix:** Verify by manually running a non-default-sort query
end-to-end (or read `GetByCollectionFiltered`'s sort plumbing —
which already covers the gap). If it works: delete the TODO and the
"For now" paragraph; replace with a 1-line comment noting that
non-default sorts go through the same `GetByCollectionFiltered` and
are keyset-paginated by the chosen sort field.

**Effort:** S
**Risk of fix:** low (documentation only)
**Reversibility:** easy

---

### R-14: `cmd/hypergoat/main.go:424-440` repeats three identical "GetCount + log on error + sentinel -1" blocks
**Severity:** nit
**Locations:** `cmd/hypergoat/main.go:424-440` inside the `/stats`
handler.
**Problem:**

```go
recordCount, err := svc.records.GetCount(reqCtx)
if err != nil {
    slog.Error("Failed to get record count", "error", err)
    recordCount = -1
}

actorCount, err := svc.actors.GetCount(reqCtx)
if err != nil {
    slog.Error("Failed to get actor count", "error", err)
    actorCount = -1
}

lexiconCount, err := svc.lexicons.GetCount(reqCtx)
if err != nil {
    slog.Error("Failed to get lexicon count", "error", err)
    lexiconCount = -1
}
```

15 lines, 3 blocks. Same shape, only `svc.X` + label string change.

**Why this matters here:** Lo. The `/stats` handler is a low-traffic
debug endpoint; adding a fourth counter would copy-paste a fourth
block, and that's not a real cost. Flag as nit.

**Proposed fix:** Optional one-liner:

```go
safeCount := func(name string, fn func(context.Context) (int64, error)) int64 {
    n, err := fn(reqCtx)
    if err != nil {
        slog.Error("Failed to get "+name+" count", "error", err)
        return -1
    }
    return n
}
recordCount := safeCount("record", svc.records.GetCount)
actorCount := safeCount("actor", svc.actors.GetCount)
lexiconCount := safeCount("lexicon", svc.lexicons.GetCount)
```

Or leave it. The brief says "leave it alone" if no current pain.

**Effort:** S
**Risk of fix:** low
**Reversibility:** easy

---

### R-15: Defensive nil-check `if repos == nil || repos.Records == nil` is impossible at the production entry point
**Severity:** low
**Locations:** `internal/graphql/schema/builder.go:579`
(`resolveRecordConnection`), :977 (`createSingleRecordResolver`),
:1026 (`createCollectionStatsResolver`), :1069
(`createCollectionTimeSeriesResolver`).
**Problem:** Four resolvers all start with the same defensive
two-check:

```go
repos := resolver.GetRepositories(p.Context)
if repos == nil || repos.Records == nil {
    return emptyConnection(), nil   // or: return nil, nil
}
```

`repos.Records == nil` is true only when a caller constructs
`*Repositories` literally with `&Repositories{Records: nil}`.
Production goes through `resolver.NewRepositories(db)` (`context.go:34-41`)
which unconditionally assigns all four fields. The `Records: nil`
shape is only used in tests that need to assert the early-return
path.

The `repos == nil` branch IS reachable: if a resolver runs against
a context that did not pass through `WithRepositories`, the lookup
returns nil. The handler installs the middleware
(`internal/graphql/handler.go`), but a test or future direct
schema-mount could skip it.

**Why this matters here:** The `repos == nil` check is honest
defence. The `repos.Records == nil` companion check is defending
against an internal-API contract — every Repositories struct
constructed through the public constructor is fully populated.
Removing the second clause removes 4 × 5 chars of nothing-noise.

**Proposed fix:** Two options:

(a) Tighten the check to just `if repos == nil` in all four
locations. Lean on the constructor contract that `Records` is always
set.

(b) Leave it. The cost is trivial and the defence is genuine if a
test ever constructs a partial Repositories. (Per the brief's
"prime deletion target," this is exactly the type of check that
qualifies, but the cost here is so low that it's nit-territory.)

I lean (a), but it's marginal. Worth keeping the equivalent check
at line 875 (`if repos.Labels == nil || len(records) == 0`) because
that path tolerates Labels being nil and the early return is
behavioural, not defensive.

**Effort:** S
**Risk of fix:** low
**Reversibility:** easy

---

### R-16: `loadLabelsByURI` over-defends — `if repos.Labels == nil` is unreachable from the only call site
**Severity:** nit
**Locations:** `internal/graphql/schema/builder.go:875` inside
`loadLabelsByURI`.
**Problem:**

```go
result := make(map[string][]string, len(records))
for _, rec := range records {
    result[rec.URI] = []string{}
}
if repos.Labels == nil || len(records) == 0 {
    return result
}
```

The `len(records) == 0` short-circuit IS genuine — saves a DB call.
The `repos.Labels == nil` arm only triggers if a caller passes a
partial Repositories — same situation as R-15. All three call sites
of `loadLabelsByURI` (builder.go:260, 387, 701, 1014) go through
the resolver-context path that constructs the full Repositories.

**Why this matters here:** Same low-leverage as R-15 — the defensive
check is for a contract the constructor enforces. Different
direction: here the check sits inside the helper, not at the
resolver. Removing it tightens "if you call this with a partial
Repositories, you'll panic on the next line" — which is fine,
because the caller is internal.

**Proposed fix:** Drop the `repos.Labels == nil` clause; keep the
`len(records) == 0` short-circuit. Add a one-line precondition
comment ("caller must supply repos with Labels set; verified by
resolver context middleware").

**Effort:** S
**Risk of fix:** low
**Reversibility:** easy

---

### R-17: `error` return wrapped sometimes, returned raw other times — inconsistent within records.go
**Severity:** low (style + diagnostic noise)
**Locations:** `internal/database/repositories/records.go` —
wrapped: lines 190, 209, 545, 560, 755, 759, 806, 821, 830, 839,
845, 924, 952, 985 (14 sites). Raw: lines 170, 251, 275, 309, 338,
685, 699, 745, 768, 777, 856, 864, 890, 898, 935, 945, 993, 1028,
1035, 1042 (20 sites).
**Problem:** Within the same file, 20 `return ..., err` paths
return the raw pgx error and 14 wrap it with `fmt.Errorf("...:
%w", err)`. The split isn't by call shape (both `Exec` and
`QueryRow` paths have both). Examples:

```go
// records.go:170 — raw
res, err := r.db.Exec(ctx, sqlStr, ...)
if err != nil {
    return Skipped, err
}
// records.go:755 — wrapped
res, err := tx.ExecContext(ctx, "DELETE FROM record WHERE did = $1", did)
if err != nil {
    return 0, fmt.Errorf("delete records by did: %w", err)
}
```

Both Exec paths inside the same file, different error policies. The
caller upstream sometimes sees "ERROR: duplicate key value..." from
pgx directly, sometimes sees "delete records by did: ERROR:
duplicate key value...".

**Why this matters here:** Low — observable in log noise (operators
can't tell which repository method failed from a raw pgx string).
Real impact when triaging an oncall page: a wrapped error tells you
which path; a raw error needs a stack trace. The existing
inconsistency is already long-standing and isn't causing operational
problems today.

**Proposed fix:** Add wrapping to the 20 raw sites, choosing
identifiers that match the method name (e.g.,
`fmt.Errorf("records GetByURI: %w", err)`). Mechanical, ~20-line
diff. Stays within the file. No behaviour change for callers using
`errors.Is/As` — wrapping preserves the chain.

Alternative: leave it — the inconsistency has zero current incident
attached. Honest empty.

**Effort:** M (20 sites to touch + tests if any assert on the
unwrapped error text)
**Risk of fix:** low
**Reversibility:** easy

---

### R-18: The three "registry" types (filterDescriptor, joinedWhereDescriptor, arrayWhereDescriptor) have unrelated shapes — the duplication is honest
**Severity:** nit (informational; documenting the conscious non-finding)
**Locations:** `internal/graphql/schema/where.go:24-36`
(`filterDescriptor`), `internal/graphql/schema/where.go:72-77`
(`joinedWhereDescriptor`), `internal/graphql/schema/where.go:118-123`
(`arrayWhereDescriptor`).
**Problem:** Each descriptor carries:
- `filterDescriptor`: Kind + FieldName + Description
- `joinedWhereDescriptor`: FieldName + TargetLexicon + JoinExpr +
  Description
- `arrayWhereDescriptor`: FieldName + ArrayPath + ElementDef +
  Description

Shared: FieldName + Description. Distinct: Kind / TargetLexicon+JoinExpr
/ ArrayPath+ElementDef. Three of the five carry their own semantics
(Kind dispatches on the SQL emitter; JoinExpr/ArrayPath are emitted
verbatim into SQL; ElementDef indexes into the parent's Others map).

**Why this matters here:** It doesn't. The brief asked "is the
duplication honest or sharable?" — answer: honest. There is no
extractable shared "Descriptor" interface that wouldn't be
premature. The Description and FieldName are the only commonalities,
and pulling them into a base struct would saddle every future
descriptor with a "DescriptionBase" embed for two fields. Per the
directive, leaving alone.

**Proposed fix:** None.

**Effort:** N/A
**Risk of fix:** N/A
**Reversibility:** N/A

---

### R-19: `looksLikeDID` (config) vs `did.IsValid` (atproto) — two parsers, but documented and contained
**Severity:** nit (informational; documenting the conscious non-finding)
**Locations:** `internal/config/config.go:346-348` (`looksLikeDID`)
and `internal/atproto/did/did.go:41-84` (`IsValid`).
**Problem:** Two DID validators:

```go
// config.go:346 — typo gate
func looksLikeDID(s string) bool {
    return strings.HasPrefix(s, "did:plc:") || strings.HasPrefix(s, "did:web:")
}

// atproto/did/did.go:41 — security boundary
func IsValid(s string) bool {
    // 40+ LOC of strict per-character validation, length bounds,
    // charset enforcement, ...
}
```

**Why this matters here:** It doesn't. The package doc of
`atproto/did` (lines 1-17) explicitly calls out this divergence and
articulates the intent: `IsValid` is the canonical security
predicate; `looksLikeDID` is a startup-only typo gate that
deliberately doesn't import the strict package to keep the
config-load path dependency-free. Both helpers are correctly
narrowly scoped.

This is the kind of "two functions doing similar things" that the
brief warned to flag — but in this case the divergence is by
design, documented, and the boundary is enforced (only `config.Load`
calls `looksLikeDID`; everything in the data path calls
`did.IsValid`).

**Proposed fix:** None.

**Effort:** N/A
**Risk of fix:** N/A
**Reversibility:** N/A

---

### R-20: `JoinExpr` and `ArrayPath` SQL-fragment registries: the security comment is duplicated verbatim in 4 places
**Severity:** nit (informational; intentional repetition for security
load-bearing comments)
**Locations:** `internal/graphql/schema/where.go:66-72`
(`joinedWhereDescriptor` doc), `internal/graphql/schema/where.go:111-118`
(`arrayWhereDescriptor` doc), `internal/database/repositories/filter.go:223-227`
(`JoinedFilter.JoinExpr` doc), `internal/database/repositories/filter.go:263-269`
(`ArrayFilter.ArrayPath` doc).
**Problem:** The "SECURITY: <Field> is emitted verbatim into SQL.
Registry values are code-defined and must NEVER source from request
data — treat additions as a SQL diff" warning is repeated four times
across the two files.

**Why this matters here:** It doesn't — for security-load-bearing
comments, repetition at every load-bearing call site is the
defensive pattern. A reader looking at `JoinedFilter` shouldn't
have to grep across packages to find the contract. Each comment is
~5 lines and they're worth their weight.

This is the inverse of the brief's "comments restating code" target
— these are comments stating WHY (security model + injection
prevention contract), not what. Keeping them is the right call.

**Proposed fix:** None. Flagging only to document that the lens
looked at the repetition and chose to leave it.

**Effort:** N/A
**Risk of fix:** N/A
**Reversibility:** N/A

---

## Summary

20 findings (the hard ceiling), of which:

- **High:** 0
- **Medium:** 6 (R-1, R-2, R-3, R-5, R-6, R-8)
- **Low:** 5 (R-4, R-9, R-10, R-11, R-12, R-15, R-16, R-17)
- **Nit / informational:** 7 (R-7, R-13, R-14, R-18, R-19, R-20, plus
  some "low" entries above straddle the boundary)

**Concrete deletion targets** (R-1, R-3, R-4): three exported but
unused functions in `internal/database/repositories/`. ~80 LOC of
dead code with externally-visible signatures — check certified-app
client for callers before deletion. If clean, low-risk delete.

**Concrete extraction targets** (R-2, R-5, R-6, R-8, R-9, R-10):
six places with line-for-line duplication that fits the brief's
"a dozen lines repeated" trigger. None requires new abstraction
layers — all are local helpers within the same file or package.
Combined estimated saving: ~150 LOC, ~6 fewer drift surfaces.

**Honest empties** (R-7, R-18, R-19, R-20): four places where the
lens looked, found surface-level duplication, and judged the
duplication justified (registry pattern deferral per directive;
descriptor shapes are genuinely different; security comments
load-bearing).

**Defensive-check pruning targets** (R-12, R-15, R-16): three
"impossible-state" checks the brief identified as the prime
deletion target. All low-leverage individually; combined cleanup is
~15 LOC and tightens "what does this function require of its
caller."

**Pattern divergence not flagged:** Repository error-wrapping policy
(R-17) is inconsistent but the variance is established and isn't
causing operational pain. Flagged at low severity for completeness;
operator may judge it not worth touching.

Closing note on the registries: per the brief's explicit guidance,
no consolidation of joinedWhereRegistry / arrayWhereRegistry /
derivedFieldRegistry is proposed. R-7 and R-18 explicitly document
that the lens considered and declined. Re-evaluate when a fourth
entry in any one of them appears (the brief's stated escape hatch).
