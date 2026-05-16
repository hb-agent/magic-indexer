# Case-insensitive string equality on `StringFilterInput`

**Status**: planning (round-1 review folded in)
**Owner**: hb-agent
**Drafted**: 2026-05-16
**Last revised**: 2026-05-16 (post-round-1)

> Audit trail for round-1 review decisions:
> `docs/case-insensitive-string-eq/review-round-1.md`.

## Larger goal

`org.hypercerts.collection` records carry a free-form `type`
discriminator (`"project"`, `"favorites"`, …). The lexicon does
not constrain its casing. Producers in the wild write `"project"`,
`"Project"`, `"PROJECT"`; the certified-app needs to list "all
projects of a user" reliably regardless of which casing landed on
chain.

Today `where: { type: { eq: "project" } }` matches the literal
string only. The discriminator behaves as a categorical value but
is stored as a free string, and the AppView should be tolerant of
that producer-side variance — the lexicon explicitly invites it
("any other type of collection").

This is the immediate driver, but the right shape is **a general
operator** on `StringFilterInput`, not a one-off normalization
hack for `type`. Other free-form string properties have the same
producer-variance risk.

## Chosen approach (option A)

Add two opt-in operators to `StringFilterInput`:

- `eqi: String`           — case-insensitive equality
- `ini: [String!]`        — case-insensitive `IN`

The existing `eq` / `in` are **unchanged**: case-sensitive,
JSONB-containment-indexable. Consumers opt into case-insensitive
behaviour explicitly, the same way `contains` and `startsWith`
already are case-insensitive today (precedent for per-operator
case behaviour is already set).

The `-i` suffix is the going-forward convention for
case-insensitive variants on filter operators in this repo. Pinned
in `StringFilterInput`'s type-level description and in AGENTS.md so
future operators (`neqi`, etc.) don't get bikeshedded.

### Field descriptions (pinned verbatim)

`StringFilterInput.eqi`:

> Equal to (case-insensitive, ASCII fold via Postgres
> `lower(... COLLATE "C")`). Both sides are lower-cased before
> comparison; non-ASCII characters pass through unchanged (no
> Unicode confusable folding). On its own, `eqi` does not use the
> JSONB GIN index — pair it with a column-level filter such as
> `did: { eq: ... }` for selective queries. No-op for content
> hashes (`cid`, `cid-link`) and lowercase TIDs; use `eq` for
> exact-identity match on DID-bearing strings such as `at-uri`
> authorities.

`StringFilterInput.ini`:

> In list (case-insensitive, ASCII fold via Postgres
> `lower(... COLLATE "C")`, max 50 values, min 1 value). Both
> sides are lower-cased; same non-ASCII and indexing caveats as
> `eqi`.

### Example consumer query (certified-app projects view)

```graphql
where: {
  did:  { eq: "did:plc:..." }
  type: { eqi: "project" }      # matches Project / PROJECT / project
}
```

## Scope and file ownership

The change is one operator pair added to the existing filter
framework. No new `FilterKind` (this is plain `KindScalar`), no
migration, no new indexes.

Edits (all on `staging`, atomic commits, each ending with
`Co-authored-by: Claude Opus 4.7 (1M context) <noreply@anthropic.com>`):

- `internal/database/repositories/filter.go`
  - Add `OpEqi` and `OpIni` constants.
  - Extend `Validate()`:
    - Type-check string value for `OpEqi`; type-check `[]string` /
      `[]interface{}` for `OpIni`.
    - `OpIni`: require `len(values) >= 1`, enforce
      `MaxInListSize`. Also tighten `OpIn` to require non-empty.
    - Refuse `OpEqi`/`OpIni` when `IsJSON=false` (defense-in-depth
      — `did` column is wired through `DIDFilterInput` so this
      case is unreachable from the GraphQL surface, but the guard
      is cheap).
    - Lower-case the value(s) in place: for `OpEqi`, replace
      `f.Value` with `strings.ToLower(s)`; for `OpIni`, replace
      with the lowered `[]string`. The emitter then binds the
      already-lowered value with no further transformation.
  - Extend `buildSingleFilter()`:
    - `OpEqi`: emit `lower((json->>'field') COLLATE "C") = $n`.
      Bind the already-lowered string.
    - `OpIni`: emit `lower((json->>'field') COLLATE "C") = ANY($n::text[])`.
      Mirror `OpIn`'s null-element handling for parity (in
      practice unreachable from the GraphQL `[String!]` surface,
      but the emitter parity is cheap and keeps the code shapes
      symmetric).
- `internal/graphql/types/filters.go`
  - Add `eqi` and `ini` fields to `StringFilterInput`. Pin the
    descriptions verbatim from above.
  - Update `StringFilterInput`'s top-level `Description` to note
    the `-i` suffix convention.
  - Append "DIDs are spec-case-sensitive; no case-insensitive
    operators on this filter input." to
    `DIDFilterInput.Description`.
- `internal/graphql/schema/where.go`
  - Extend `parseOperator()` to recognize `"eqi"` → `OpEqi`,
    `"ini"` → `OpIni`. Nothing else.
- `AGENTS.md`
  - One paragraph in the filter section documenting the `-i`
    suffix convention for case-insensitive operator variants.
- `CHANGELOG.md`
  - Unreleased entry under "Server" with the note "additive;
    existing query shapes unchanged" for schema-diff consumers.

### Test surface

- `internal/graphql/types/filters_test.go` — assert `eqi` and
  `ini` appear on `StringFilterInput` with the pinned
  descriptions; assert `DIDFilterInput` retains only `eq` / `in`
  and gains the new description suffix.
- `internal/graphql/schema/where_test.go` and
  `builder_test.go` —
  - parse `eqi` / `ini` into `FieldFilter` with `OpEqi` / `OpIni`;
  - assert `eqi` / `ini` are present on any
    `StringFilterInput`-typed property of a real lexicon's
    generated `WhereInput`, and absent from `DIDFilterInput`-typed
    fields (`did`, `contributor`, `subject`).
- `internal/database/repositories/filter_unit_test.go` — SQL
  fragment shape:
  - `lower((expr) COLLATE "C") = $1` for `OpEqi`;
  - `lower((expr) COLLATE "C") = ANY($1::text[])` for `OpIni`;
  - parameter is `strings.ToLower(input)` (Go-side), passed as
    `string` for `OpEqi` and `[]string` (not `[]interface{}`) for
    `OpIni`;
  - SQL adversarial table (`'; DROP TABLE record; --`, `"`, `\`,
    `%`, `_`, NUL, leading/trailing whitespace): SQL contains no
    payload bytes, parameter equals `strings.ToLower(input)`;
  - field-name injection test: a field name violating
    `fieldNameRegex` is rejected before SQL emission;
  - `OpEqi` with non-string value rejected at `Validate()`;
  - `OpIni` empty list rejected with message referencing
    `MaxInListSize`;
  - `OpIni` at exactly `MaxInListSize` succeeds; at
    `MaxInListSize+1` rejected;
  - `OpIni` with NULL-element list mirrors `OpIn` exactly;
  - `OpEqi`/`OpIni` with `IsJSON=false` rejected (defense-in-depth).
- `internal/database/repositories/records_filter_test.go` —
  Postgres-backed integration:
  - records with `type ∈ {"project","Project","PROJECT","PrOjEcT","projects"}`,
    plus a record from a different `did`; `type: { eqi: "project" }`
    matches the first four and not `"projects"`; combined with
    `did: { eq: ... }` returns only the user's matches;
  - `ini: ["project","favorites"]` matches `type ∈ {"project",
    "PROJECT", "Project", "favorites", "Favorites"}` and excludes
    `"side-project"`;
  - `_or` composition: `where: { _or: [ { did: { eq: D } }, { type: { eqi: "project" } } ] }`
    returns the expected union;
  - `_and` composition: `where: { _and: [ { did: { eq: D } }, { type: { eqi: "project" } } ] }`
    equals the field-level AND case (regression for the parser);
  - nested composition: `_or` inside `_and` and the `MaxFilterDepth`
    boundary still pin correctly with `eqi` operands;
  - regression: `type: { eq: "project" }` does NOT match
    `"Project"`; `type: { in: ["project"] }` does NOT match
    `"Project"` — pins acceptance criterion 6 to runnable code;
  - `type: { eqi: "" }` matches a record whose `type` is literally
    empty and nothing else;
  - `type: { eqi: " project " }` does NOT match `"project"` (no
    implicit trim);
  - Cyrillic `р` (U+0440) record vs Latin `eqi: "p"` (U+0070): no
    match (pins no confusable folding);
  - error-message substring assertions on each refusal so the
    actionable text doesn't regress;
  - `neq: "project"` continues to be case-sensitive (pins that
    `neqi` absence is deliberate).

## Alternatives considered

**B. Normalize at ingest** — lowercase `type` server-side at write
time, backfill historical rows. *Rejected.* The AppView's job is
to read the network truthfully, not silently rewrite producer
records.

**C. Producer-side fix (certified-app only)** — only ever write
lowercase, accept that mixed-case records from other producers
stay invisible. *Rejected.* Doesn't generalize; the operator
surface is small enough that fixing it once at the AppView is
cheaper.

**D. Reuse `contains`** — `type: { contains: "project" }` is
already case-insensitive. *Rejected.* Substring match catches
`"projects"`, `"side-project"`, etc. — wrong semantics.

**E. Citext column / functional index up-front** — store `type` in
a `citext` column or add `lower(type)` as an indexed expression
in this round. *Rejected for this round.* `type` is one JSON
property among many; case-insensitivity is the operator's
concern, not the column's. If a single field becomes a hot filter
target later, a functional index keyed on
`lower((json->>'<field>') COLLATE "C")` is a follow-up that
doesn't change the API. The `COLLATE "C"` choice in this round's
emitted SQL is what unlocks the follow-up: the expression is
IMMUTABLE under any database collation and so can serve as an
index expression.

**F. Split `StringFilterInput` into case-sensitive vs
case-insensitive-capable variants.** *Rejected* (R2 item 7) —
forces every `WhereInput` builder to know which to wire,
contradicts the existing precedent that `contains`/`startsWith`
already mix case behaviours on one type.

**G. Refuse `eqi`/`ini` per-type for `cid`, `cid-link`, `tid`,
`at-uri`, `uri`.** *Rejected* — see `review-round-1.md` "Conflict
resolved up front". The existing precedent is that
`contains`/`startsWith` are exposed on these same types without
restriction. Per-type refusal would create an asymmetric surface
plus a schema-vs-runtime mismatch (the operator would appear in
introspection and fail at validation). Instead the field
descriptions guide consumers to `eq` when exact identity is
required.

## Acceptance criteria

1. `StringFilterInput` exposes `eqi: String` and `ini: [String!]`
   with the pinned descriptions; introspection surfaces them.
2. `DIDFilterInput` retains only `eq` and `in`; its description
   pins the no-case-insensitive contract.
3. A record with `type = "Project"` matches `type: { eqi:
   "project" }` (and `"PROJECT"`, `"PrOjEcT"`). A record with `type
   = "projects"` does not match. `type: { eqi: " project " }` does
   not match `"project"` (no implicit trim).
4. `ini: ["project","favorites"]` matches `type ∈ {"project",
   "PROJECT", "Project", "favorites", "Favorites"}` and excludes
   `"side-project"`. `ini: []` is rejected at validation with a
   message referencing `MaxInListSize`. `ini` at exactly
   `MaxInListSize` (50) succeeds; at 51 is rejected.
5. `_or`, `_and`, and `_or`-inside-`_and` composition with `eqi`
   and `ini` all return the expected sets. `MaxFilterDepth`
   continues to bound the depth.
6. **Regression**: `eq` and `in` continue to be case-sensitive
   and continue to use JSONB containment / `ANY(...::text[])`. No
   behaviour change for existing consumers.
7. SQL emitted for `eqi` / `ini` contains no user input — values
   are bound as parameters only, even against an adversarial
   value table (SQL-injection payloads, NUL, whitespace, LIKE
   metacharacters). The emitted SQL shape is
   `lower((json->>'<field>') COLLATE "C") {= | = ANY}` with
   `<field>` validated by `ValidateFieldName` before any
   `Sprintf` interpolation.
8. `OpEqi` with non-string value rejected at `Validate()`;
   `OpEqi`/`OpIni` with `IsJSON=false` rejected (defense-in-depth);
   each refusal carries an actionable error message asserted by
   substring tests.
9. Cyrillic `р` does not match Latin `eqi: "p"` (no Unicode
   confusable folding).
10. `neq` remains case-sensitive — pinned in tests to make the
    absence of `neqi` deliberate.
11. CHANGELOG entry under Unreleased documents the addition with
    "additive; existing query shapes unchanged".
12. AGENTS.md documents the `-i` suffix convention.
13. All four local quality gates pass with no new errors against
    the captured baseline.

## Out of scope

- Functional index `lower((json->>'<field>') COLLATE "C")` on
  `record.json` per hot lexicon. Not needed for the certified-app's
  per-user view (always AND'ed with `did` which is column-indexed).
  Defer until a global case-insensitive scan shows up in hot-path
  metrics. The `COLLATE "C"` choice in this round's SQL keeps the
  follow-up index expression-compatible.
- Locale / Unicode case-folding (Turkish `İ`, German `ß`,
  confusables). `lower(... COLLATE "C")` is ASCII-only by design;
  documented as a known limitation in the field descriptions.
- `eqi` / `ini` on `DIDFilterInput`. DIDs are spec-case-sensitive;
  introducing case folding would be a spec violation.
- `neqi`, `gti`, `ltei`, etc. YAGNI; the `-i` suffix convention
  is documented so future additions don't need a new debate.
- Splitting `StringFilterInput` or providing a per-property
  opt-out for case-insensitivity (G in alternatives).
- Producer-side guidance for the certified-app. Handled as a
  separate handoff to the certified-app implementer once the
  operator ships.

## Rollback plan

Revert the staging commits. The operator pair is purely additive
on `StringFilterInput`; no migration, no data change, no breaking
schema change. Existing clients using `eq` / `in` are unaffected.
Schema-diff CI for downstream consumers will register the
addition as informational, not breaking.

## Open questions

None remaining. Round-1 review closed both originally-open
questions:

- ~~`cid` / `cid-link` exposure~~ — exposed everywhere (R2 item 3
  / R3 item 10 conflict resolution).
- ~~Field description surface in introspection~~ — confirmed by
  R2 item 4.
