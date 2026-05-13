# Issue #64 — Contributor identity filter for `orgHypercertsClaimActivity`

**Issue**: [#64 "Add contributor filter to OrgHypercertsClaimActivityWhereInput"](https://github.com/hb-agent/magic-indexer/issues/64)

**Status**: PLAN — pending review.

---

## Larger goal this serves

A profile page on `certified.app` needs an Activities tab that lists
every activity a user is involved in. Two roles to surface:

1. **Authored** — records in the user's own repo. Already supported via
   `OrgHypercertsClaimActivityWhereInput.did`.
2. **Contributed-to** — records authored by *someone else* in which the
   user appears as a contributor (DID or handle). **No server-side
   filter exists for this today**.

Without a server-side filter for (2), the consumer either pages
through every activity record and filters client-side (does not scale)
or skips the contributor-list view entirely. Issue #64 asks us to
close that gap with a multi-value identity filter so the consumer can
issue a small, paged query.

The narrower acceptance criterion is: *given a list of identity
strings, return paginated activity records whose `contributors[*]`
includes at least one matching identity*.

---

## Production data shape (and the lexicon drift)

### Lexicon (`testdata/lexicons/org/hypercerts/claim/activity.json`)

```
contributors: [#contributor]
#contributor.contributorIdentity: union of
    - #contributorIdentity  (type: "string")
    - com.atproto.repo.strongRef ({uri, cid})
```

The lexicon says the inline variant is a bare string.

### Live indexer data (per #64 sample)

```json
{
  "contributors": [
    {
      "contributorIdentity": {
        "$type": "org.hypercerts.claim.activity#contributorIdentity",
        "identity": "satyam-test.climateai.org"     // handle form
      }
    },
    {
      "contributorIdentity": {
        "$type": "org.hypercerts.claim.activity#contributorIdentity",
        "identity": "did:plc:cwqqmzquxrw6wqlpvk2h2s5x" // DID form
      }
    }
  ]
}
```

In production the inline variant is an **object** with `$type` + `identity`,
not a bare string. The producer (`certified.app`) wraps it. This is real
drift, and the existing notifications extractor
(`internal/notifications/extractors/shared.go:extractContributorDID`)
silently fails to extract the DID from this shape — it `json.Unmarshal`s
the union into `&s string` and returns `""` when the shape is an object.
This means **`ReasonActivityContributor` notifications are silently broken
for production-shape data**. Flagged here, deferred from this PR (see
"Out of scope" below) — separate issue should be opened.

For #64 we filter on `contributors[*].contributorIdentity.identity`
(the production shape). Strong-ref variant (`{uri, cid}`) is not in
the indexed activity data today and is acknowledged-as-deferred by
the issue author.

---

## Alternatives considered

| ID | Shape | SQL approach | Pros | Cons | Decision |
|----|-------|--------------|------|------|----------|
| A | Top-level connection arg: `contributorIdentities: [String!]` (matches `authors`, `excludePds` precedent) | `r.json @> ANY(ARRAY[…]::jsonb[])` over existing GIN index | Smallest surface area, matches existing top-level filter precedent, no schema-builder change | Activity-only by design, doesn't compose into `_and`/`_or`, off-pattern with where-input | **Rejected** — issue author explicitly asked for the WhereInput shape; composability matters for future "authored OR contributed" single-query unification |
| B | Field on `OrgHypercertsClaimActivityWhereInput`: `contributorIdentity: { in: [...], eq: "..." }` (uses existing `StringFilterInput`) | EXISTS subquery over `jsonb_array_elements(json->'contributors')` for IN; `@>` containment for `eq` | Composes with `_and`/`_or`/`did`; in the input where issue author asked for it; reusable `StringFilterInput` keeps API surface familiar | Single-collection filter coded as a special case in `where.go`; small departure from the lexicon-driven schema generator | **Chosen** — see rationale below |
| C | Generic nested filter generation: `contributors_some: { contributorIdentity: { identity_in: [...] } }` (Hasura-style) | Generic translation of nested input objects to JSONB array EXISTS clauses with per-segment validation | Solves the whole class of "filter through nested array of objects" problems generically | Large schema-builder change explicitly deferred in the repo; would need design-doc-level review; over-builds for the immediate use case; high risk of subtle bugs in nested operator handling | **Rejected** — premature generalization. The repo has exactly **one** lexicon today with this shape; cost is disproportionate. Revisit if a second collection lands with the same pattern. |
| D | Denormalized column on `record`: `contributor_identities text[]` populated at ingestion | Plain GIN(text[]) lookup with `&&` overlap | Fast, simple SQL, generalizes via additional columns | New migration, ingestion hook, backfill for ~all activity records, only solves activity; reintroduces denormalisation pattern that issue #63 explicitly rejected for PDS (cardinality logic differs but the precedent is informative) | **Rejected** — denormalisation should be an optimisation if EXISTS-on-JSONB is measurably too slow, not a starting point |
| E | Separate `contributor` table populated at ingestion (relational) | INNER JOIN `contributor c ON c.activity_uri = r.uri AND c.identity = ANY(...)` | Generalizes to roles, weights, future queries on contributor metadata | Heavy: migration, ingestion hook, backfill, write-side maintenance; ties activity model to relational schema this codebase deliberately avoids in favour of JSONB | **Rejected for v1** — strong candidate for a future iteration if multi-field queries on contributor metadata become common, but #64 only asks about identity matching |

### Chosen path: Alternative B, sharpened by an explicit DID-only policy

A field `contributor: DIDFilterInput` on
`OrgHypercertsClaimActivityWhereInput`. Operator scope is `eq` and `in`
only — the resolver activates an EXISTS subquery against
`jsonb_array_elements(json->'contributors')` and matches contributor
identities by exact equality.

#### DID-only policy (operator decision, 2026-05-13)

> *"Our indexer only reads `contributorIdentity` when it is a DID.
> Handle is a frontend thing. That's how we enforce that other apps do
> it like this. We aren't fixing any records in PDSs — that's not our
> job."*

Consequences baked into this PR:

1. **Filter input must be DIDs.** Validation rejects non-DID values with
   a clear error (`"contributor filter values must be DIDs"`).
   Consumers must resolve handles → DIDs in their own session layer
   before querying. The DID-validity check reuses the conservative
   `isValidDID` predicate from
   `internal/notifications/extractors/shared.go` (relocated to a
   shared helper).
2. **`eq` and `in` only.** `contains` / `startsWith` are meaningless on
   DIDs; `neq` / `isNull` have ambiguous semantics on a per-element
   array predicate. Use `DIDFilterInput` (already in the type system —
   it exposes `eq` and `in` only).
3. **Tolerate both lexicon-compliant and current-production shapes for
   `contributorIdentity`** when reading records:
   - bare string variant (lexicon-compliant): `"contributorIdentity": "did:plc:..."`
   - object variant (current `certified.app` drift):
     `"contributorIdentity": {"$type": "...", "identity": "did:plc:..."}`
   Both yield a candidate string; we exact-match against the validated
   DID list. **A record entry that resolves to a non-DID string
   automatically does not match**, because every valid input is a DID
   and equality is exact.
4. **Fold in the notifications extractor fix.** `extractContributorDID`
   currently `json.Unmarshal`s into a `string` and returns `""` for
   the object shape — that means `ReasonActivityContributor`
   notifications are silently broken for every record `certified.app`
   has written. The fix is small and same-lens: also read `.identity`
   when the value is an object, then run through the same `isValidDID`
   filter. Same code path as the new filter SQL — same policy, same
   tolerance.
5. **Add a `contributor_identity_total{outcome}` Prometheus counter**
   at ingest time so we can see how often producers write non-DID or
   unrecognised-shape contributor identities. Outcomes:
   - `did` — value resolved to a valid DID
   - `non_did` — value was a string (bare or `.identity` sub-field) but
     not a DID
   - `unrecognized_shape` — value was neither a string nor an object
     with a string `.identity`
   Mirrors the existing `hypergoat_pds_resolve_total{outcome}` shape
   from PR #63 (`internal/metrics/metrics.go:359`).

Why this path best serves the larger goal:

1. **Enforcement-by-reading** is the cheapest mechanism we have to
   nudge producers toward writing DIDs. No ingestion gates, no
   record rewrites, no PDS-side changes.
2. **No backfill, no migration.** Existing global GIN index on
   `record.json` (`idx_record_json_gin`) carries the SQL; the
   `(collection, indexed_at)` index prunes the row set before the
   JSONB scan.
3. **Composes** — `_and` / `_or` / `did` filter combine naturally.
4. **Reversible** — additive change. Revert by removing the new field
   in `where.go` + the `contributor`-aware branch in
   `filter.go`/`records.go`. No data shape changes.
5. **Notifications fix piggy-backs without expanding scope** — same
   policy, same code path; rolling them together avoids a second PR
   that revisits the same lexicon.

---

## Scope

### In scope

1. New GraphQL filter field `contributor: DIDFilterInput` on the
   `OrgHypercertsClaimActivityWhereInput` type. Wired only for the
   `org.hypercerts.claim.activity` lexicon — not generated for any
   other collection.
2. Backend SQL translation:
   - `eq` → EXISTS subquery over `jsonb_array_elements`, COALESCE
     across both contributorIdentity shapes (see SQL shape below).
   - `in` → same EXISTS pattern, `= ANY($N::text[])`.
3. Validation (at the resolver, before SQL is built):
   - Every value must satisfy `isValidDID` (`did:` prefix, length
     8–256, charset `[A-Za-z0-9:._-]`). Reject otherwise with
     `"contributor filter values must be DIDs"`.
   - Reuse the existing 50-value cap on IN lists.
   - Reuse the existing `MaxFilterConditions = 20`.
4. **Notifications extractor fix** in
   `internal/notifications/extractors/shared.go:extractContributorDID`:
   - Also read `.identity` when the JSON value is an object.
   - Continue running the candidate through `isValidDID`.
   - Add unit tests covering bare string, object variant, handle
     (non-DID), and object with non-string `.identity`.
5. **Metric** `contributor_identity_total{outcome}` in
   `internal/metrics/metrics.go`:
   - Outcomes: `did`, `non_did`, `unrecognized_shape`.
   - Incremented at ingest time from the (now unified) helper that
     extracts a DID from a contributor's `contributorIdentity`.
   - Same shape as the existing `pds_resolve_total{outcome}` counter.
6. Tests:
   - Filter-builder unit tests for `eq` and `in`.
   - Validation tests for non-DID rejection.
   - GraphQL schema builder test confirming `contributor` field exists
     on `OrgHypercertsClaimActivityWhereInput` and is **absent** from
     at least one other collection's WhereInput.
   - Postgres integration tests covering: DID-form bare string match,
     DID-form object variant match, multi-value IN, no match,
     multiple contributors per record (only one matches), record with
     `contributors` absent, empty `contributors` array, malformed
     contributorIdentity (object without `identity` field — must not
     match), record where contributor identity is a handle string
     (must not match a DID query — enforcement test), composition
     with `did` filter, composition with `_or`, composition with
     `excludePds`.
   - Pagination correctness — keyset cursor must still advance
     correctly when the filter is applied.
   - Extractor regression test: notifications are emitted for the
     object-shape contributor identity post-fix.
   - Metric increment test: each outcome is exercised by a unit test
     touching the helper directly.

### Out of scope (explicit deferrals)

1. **Strong-ref variant** (`com.atproto.repo.strongRef` for
   `contributorIdentity`). Resolution would require ingesting and
   joining `org.hypercerts.claim.contributorInformation` records, or
   a synthesised denormalised view. File a separate issue when a
   consumer needs it.
2. **Generalising nested filter generation** to other collections
   (Alternative C in this doc). Premature.
3. **`contributionWeight` / `contributionDetails` filters**. Not
   requested.
4. **Ingest-time DID canonicalisation** (Tier 4 from operator
   discussion). Explicitly *not* done — the operator chose
   enforcement-by-reading instead. The metric in (5) is the
   feedback loop; if non_did volume stays high after a producer
   notice, revisit.
5. **Fixing records in upstream PDSs** — out of scope by policy.
   That is the producers' concern, not the indexer's.

---

## File ownership

Single-track work — no parallel implementation. Listed by package:

| Path | Change |
|------|--------|
| `internal/database/repositories/filter.go` | New `FieldFilter` flag `IsArrayContributor bool` (specific marker for this filter; a generic "array element" abstraction would be premature — see Alternative C). When set, `buildSingleFilter` emits the EXISTS subquery shape. Adds `isValidDIDList` validator used by the resolver. |
| `internal/database/repositories/records_filter_test.go` | New SQL-correctness cases: `TestGetByCollectionFiltered_Contributor_Eq`, `…_In`, `…_NoMatch`, `…_AbsentContributors`, `…_EmptyArray`, `…_MultipleContributors`, `…_BareStringVariant`, `…_ObjectVariant`, `…_HandleEntryDoesNotMatch`, `…_ComposeWithDid`, `…_ComposeWithOr`, `…_ComposeWithExcludePds`, `…_Pagination`. |
| `internal/database/repositories/filter_test.go` (new) | Unit-level tests for the SQL fragment produced by `eq` and `in`. Validation tests for non-DID rejection. |
| `internal/graphql/schema/where.go` | Special-case branch: when building the WhereInput for `org.hypercerts.claim.activity`, inject `contributor: DIDFilterInput` after the lexicon-driven loop. In `extractFieldFilters`, when the lexicon ID matches and `fieldName == "contributor"`, validate every value with `isValidDID` and emit a `FieldFilter` with `IsArrayContributor = true`. |
| `internal/graphql/schema/builder_test.go` | Field exists on activity WhereInput, absent from at least one other (e.g. `app.certified.temp.graph.endorsement`). |
| `internal/notifications/extractors/shared.go` | `extractContributorDID` extended: also reads `.identity` when raw is an object. Increments the new metric with the appropriate outcome label before returning. `isValidDID` stays here; **re-exported** via a tiny shim so `filter.go`/`where.go` can reuse it without circular imports (or moved to a new shared package — see Open questions below). |
| `internal/notifications/extractors/activity_contributor_test.go` | Add cases for object-shape `contributorIdentity` (notification IS emitted post-fix) and for handle-shape (notification still NOT emitted, but metric increments `non_did`). |
| `internal/notifications/extractors/shared_test.go` (new or existing) | Unit tests for `extractContributorDID` across bare string, object variant, handle, non-string `.identity`, missing field, malformed JSON. |
| `internal/metrics/metrics.go` | Add `contributor_identity_total{outcome}` CounterVec with `ContributorIdentityDID()`, `ContributorIdentityNonDID()`, `ContributorIdentityUnrecognizedShape()` accessor functions. Mirrors the existing `pds_resolve_total` pattern. |
| `internal/metrics/metrics_test.go` | Exercise each accessor and verify the gathered metric values. |
| `CHANGELOG.md` | New entry under Unreleased — both the filter and the extractor fix. |
| `AGENTS.md` | Append a short note under "Items deliberately deferred" or a new "Posture" bullet: contributor identity is read as a DID or not at all; handle-form is treated as missing. |
| `docs/issue-64/plan.md` | This file. |
| `docs/issue-64/review-round-N.md` | Created by review steps. |

**No migrations.** **No ingestion topology changes.** **No new tables.**
**No new external dependencies.**

---

## SQL shape

Let `$P` be the validated DID value (or `text[]` for `in`).

Both operators use the same EXISTS pattern, guarded against
non-array shapes and oversized arrays, COALESCing the two
contributor-identity shapes so a record matches if **any** of its
contributors carries the DID either as a bare string or inside the
production-drift object:

**`in` (canonical form):**

```sql
jsonb_typeof(r.json->'contributors') = 'array'
AND jsonb_array_length(r.json->'contributors') <= 200
AND EXISTS (
  SELECT 1
  FROM jsonb_array_elements(r.json->'contributors') AS c
  WHERE COALESCE(
          c->>'contributorIdentity',                  -- bare string variant
          c->'contributorIdentity'->>'identity'       -- object variant
        ) = ANY($P::text[])
)
```

Why the two guard clauses:

- `jsonb_typeof(...) = 'array'`: `jsonb_array_elements` **raises** when
  passed a non-array (including `NULL` from a missing key — that part
  is actually safe, but a record where `contributors` is a string or
  object would brick every query touching this filter). Cheapest defence.
- `jsonb_array_length(...) <= 200`: caps per-row scan cost so a
  pathological record with a 100k-element `contributors` array cannot
  weaponise the public filter. 200 mirrors the existing
  `MaxContributorsBeforeReject` constant in
  `internal/notifications/types.go:26`. Records that exceed it become
  *invisible* to this filter — fail-safe semantics. Filed follow-up
  issue: hard-cap `contributors` array at ingestion time.

Author both guards **before** the EXISTS clause in the SQL text so the
planner short-circuits on cheap scalar predicates before invoking the
set-returning function.

**`eq` (degenerate IN with a single value):** emitted as the same
guarded EXISTS subquery with `= $P` rather than `= ANY(...)`. Choosing
this over a top-level `@>` containment is deliberate — keeping a
single SQL shape simplifies tests, keeps both operators on the same
query plan, and avoids `@>` matching false-positives if records ever
start carrying nested matching shapes elsewhere (the GIN containment
match isn't anchored to `contributors[*]`).

### Operator scope (final)

| Op | Supported | Notes |
|----|-----------|-------|
| `eq` | yes | EXISTS over `jsonb_array_elements`, COALESCE both shapes |
| `in` | yes | Same EXISTS pattern, `= ANY($P::text[])` |

`DIDFilterInput` already exposes exactly these two operators (see
`internal/graphql/types/filters.go:83`), so no operator-rejection
plumbing is required.

### Notes on COALESCE semantics and precedence

`->>` and `->` return `NULL` on shape mismatch. If
`contributorIdentity` is a bare string, the object access
`c->'contributorIdentity'->>'identity'` is NULL; if it's an object,
the bare-string access `c->>'contributorIdentity'` is NULL. COALESCE
picks the first non-NULL — which is always the form the record
actually used. If neither matches (some third unknown shape, or
identity field missing from an object), COALESCE yields NULL and the
ANY/= comparison yields NULL (false in WHERE) — the row is correctly
excluded.

**Precedence note (security-relevant)**: bare string wins. The object
access is only consulted when the bare access yields NULL. A producer
that writes `"contributorIdentity": "did:plc:not-the-target"` cannot
mask a match by *also* placing a target DID in a nested `.identity` —
the bare string short-circuits the COALESCE. Conversely, a producer
that writes an object with an `identity` cannot squeeze a second DID
into the same contributor entry — only `.identity` is read. The
fixture suite includes a record with one bare-string contributor and
one object-shape contributor (both must be reachable in a single
filter call).

---

## Acceptance criteria

A. `OrgHypercertsClaimActivityWhereInput` exposes a
   `contributor: DIDFilterInput` field and **only** that lexicon's
   WhereInput does. Field description states the DID-only policy
   explicitly.

B. Filtering with:

```graphql
{
  orgHypercertsClaimActivity(
    first: 50
    where: { contributor: { in: ["did:plc:abc"] } }
    orderBy: indexed_at orderDirection: DESC
  ) { edges { node { uri did } } pageInfo { hasNextPage endCursor } }
}
```

returns every activity record whose `contributors[*]` carries
`did:plc:abc` as a contributor identity — in either the bare-string
or the object shape — and no others.

C. Filtering with a handle returns a clean validation error
   (`"contributor filter values must be DIDs"`) at the GraphQL layer,
   not silent empty results.

D. Composition with existing filters works:

```graphql
where: {
  _or: [
    { did: { eq: "did:plc:me" } },
    { contributor: { in: ["did:plc:me"] } }
  ]
}
```

Returns the "authored OR contributed" union as a single query.

E. Pagination is correct under filter — `endCursor` advances,
   `hasNextPage` is honest, and subsequent pages match the expected
   SQL semantics.

F. **Notifications fix verified**: a freshly-ingested activity record
   with the object-shape `contributorIdentity` produces a
   `ReasonActivityContributor` notification for the contributor DID
   (regression test). Pre-fix this notification was silently dropped.

G. **Metric verified**: `contributor_identity_total{outcome}` is
   incremented at ingest with the right outcome label for each input
   shape. Unit-test asserted.

H. All four quality gates pass:
   - `go build ./...`
   - `go vet ./...`
   - `go test -race ./...`
   - `golangci-lint run ./...`

I. Integration tests pass with `TEST_DATABASE_URL` set.

J. Single example query against a staging-deployed indexer returns the
   expected shape (operator confirms post-merge).

---

## Performance considerations

- **Hot path**: the consumer's profile-page query. Expected shape is
  `IN [did:plc:user]` with `first: 50` and the default sort
  (`indexed_at DESC`). Per-request work: scan
  `idx_record_collection_keyset` newest-first from the cursor;
  evaluate the EXISTS subquery per candidate row over a contributor
  list averaging <10 items.
- **Index that actually does the work**:
  `idx_record_collection_keyset` (`(collection, indexed_at DESC,
  uri DESC)`, defined in
  `internal/database/migrations/postgres/008_add_record_keyset_index.up.sql`).
  The existing `idx_record_json_gin` uses the `jsonb_path_ops`
  opclass and supports `@>`, `?`, `?|`, `?&`, `@?` only — **none of
  these match the chosen EXISTS-over-`jsonb_array_elements` shape**,
  so the GIN index is not consulted at all on this filter. Both
  guard predicates (`jsonb_typeof`, `jsonb_array_length`) and the
  EXISTS subquery are evaluated per row after the keyset index
  narrows the candidate set.
- **Risk**: a "popular contributor" matching many rows. Bounded by
  `first: 50` and keyset pagination — the planner short-circuits the
  index scan once 50 rows pass the EXISTS.
- **Asymmetry to expect**: recent-active contributors are fast (the
  cursor finds 50 matches in the first newest-first window).
  Ancient-but-inactive contributors are slow (the planner walks
  newer rows that don't match before reaching the older window where
  the contributor appears). This is standard cursor-pagination
  behaviour with a low-selectivity correlated subquery, not a
  regression specific to this filter.
- **Worst-case scan bound**: 200 contributors per row × 50 IN values
  × candidate rows walked. The 200-cap (see §"SQL shape") prevents a
  single pathological record from poisoning all queries. A separate
  follow-up issue will add an ingest-time hard cap.
- **Subscriptions are unaffected**. The `contributor` filter is on
  the connection query only;
  `internal/graphql/subscription/pubsub.go` filters by collection
  only and has no `where` plumbing — no change required.
- **No new index in this PR** (decision). The existing keyset index
  funnels candidate rows tightly enough that the per-row EXISTS
  cost is acceptable for the hot path. Perf measurement happens via
  staging `EXPLAIN ANALYZE` against production-shaped data, not via
  a synthetic load test. A follow-up index, **if needed**, must
  satisfy two constraints the obvious shape does not:
  - **Both contributor shapes must be covered.** A naive
    `jsonb_path_query_array(json->'contributors', '$[*].contributorIdentity.identity')`
    only enumerates the object variant — records using the
    bare-string variant would become invisible to the index path.
    Either deprecate the bare-string variant first, or build a
    union index expression that yields both
    `c->>'contributorIdentity'` (when bare) and
    `c->'contributorIdentity'->>'identity'` (when object).
  - **`IMMUTABLE` wrapper required.** Postgres rejects a GIN index
    on a non-IMMUTABLE expression; `jsonb_path_query_array` is
    `STABLE` and must be wrapped in a small SQL IMMUTABLE function.
  Both caveats will be addressed by the follow-up PR that introduces
  the index, if measurement says we need one.

---

## Security

### Input validation surface

The new filter is gated by a single shared predicate:
`internal/atproto/did/did.go:IsValid`. Contract:

- accept `did:[a-z]+:[A-Za-z0-9:._-]+` (lowercase method prefix
  required — uppercase in the prefix is **rejected**)
- length 8..256
- reject leading/trailing whitespace
- reject NUL bytes (already excluded by the charset)

This is the canonical input-validation DID predicate for the
codebase. The pre-existing `oauth.IsValidDID` is structurally
weaker (prefix-only) and is **renamed in this PR** to
`oauth.HasDIDMethodPrefix` to remove the foot-gun for future
callers. `SECURITY.md` is updated with a one-line entry naming the
canonical predicate.

Stricter than W3C DID syntax (which allows `%`-encoded segments
and other characters) — intentional, this is input sanitisation
for a SQL filter.

### SQL surface

- All filter values are parameterised via `$N` placeholders. Field
  paths in the EXISTS subquery (`'contributors'`,
  `'contributorIdentity'`, `'identity'`) are **constants**, not user
  input — no injection surface.
- `MaxInListSize = 50` cap holds. `MaxFilterConditions = 20` holds.
- The two guard predicates (`jsonb_typeof` and `jsonb_array_length`)
  are mandatory — see §"SQL shape" for the threat-model motivation.

### Threat model: oversized contributor arrays

A malicious record with a 100k-element `contributors` array would
otherwise weaponise every public query. **The 200-element SQL bound
makes such records invisible to this filter** rather than blocking
all queries that touch the collection. The ingest path does **not**
currently cap `contributors` array size for record persistence
(`MaxContributorsBeforeReject = 200` only gates notification
fan-out; the record itself is still stored). Follow-up issue:
ingest-time hard cap.

### Data disclosure

No new data is disclosed. Contributor identities are already
returned inside `record.json` content on the public GraphQL
endpoint. The new field is filter-only — it lets clients page by a
predicate they could already evaluate after fetching.

### Notification fan-out (re-enabled by the extractor fix)

The folded-in extractor fix turns previously-silent
`ReasonActivityContributor` notifications back on for object-shape
records. Per-record fan-out is bounded by
`MaxFanOutPerRecord = 100` and the ingest-time
`MaxContributorsBeforeReject = 200`. An attacker writing many
records produces ≤100N notifications — bounded only by their PDS
write rate, not by this PR. Pre-existing posture.

### Metric cardinality

`contributor_identity_total{outcome}` has three fixed-string
outcomes (`did`, `non_did`, `unrecognized_shape`). No user-
controllable label component. Conforms to `SECURITY.md`'s
"every label is bounded" rule.

### Log levels

The `non_did` and `unrecognized_shape` outcomes are data-shape
facts, not operator-actionable failures. **No WARN logging — the
metric is the signal.** Mirrors the `PDSResolveNoEndpoint`
precedent in `internal/database/repositories/actors.go`.

### Unchanged surfaces

No change to AuthN/AuthZ. No change to label filtering. No change
to PDS filter or `excludePds`. No change to the `did` filter.
No change to the GraphQL subscription path.

---

## Rollback plan

Single PR, no migrations. The filter and the notifications-extractor
fix land as **two separate commits** on `staging`:

1. `feat(graphql): contributor filter on activity records` — adds
   the schema field, the SQL path, the metric, the shared
   `did.IsValid` predicate.
2. `fix(notifications): read contributorIdentity from object shape` —
   the small extractor fix, the rename of `oauth.IsValidDID` to
   `oauth.HasDIDMethodPrefix`.

Three rollback options:

- **Revert the whole PR**: `git revert <merge-sha>`. Filter is gone;
  the notification path returns to silently dropping object-shape
  contributor identities (pre-existing broken state).
- **Revert filter only**: `git revert <commit-1>`. Notifications fix
  stays in production.
- **Revert notifications fix only**: `git revert <commit-2>`. Filter
  stays; we go back to the silent-drop state for notifications.

No data fix-up required for any of the three. No coordinated client
work — consumer just falls back to the pre-PR behaviour (no
contributor-side filter) or the pre-PR notification behaviour.

---

## Open questions for the operator — resolved 2026-05-13

1. **Field name** → `contributor` (operator override of agent's
   `contributorIdentity` proposal). Schema name:
   `OrgHypercertsClaimActivityWhereInput.contributor: DIDFilterInput`.
2. **Notifications extractor bug** → **folded into this PR**, not a
   separate issue. Same lens, same policy.
3. **Docs surface** → GraphQL field description only (no
   `docs/RUNBOOK.md` section).
4. **Non-DID identities in records** → metric outcome `non_did`
   counted at ingest; otherwise silently ignored at query time. No
   400 errors at query time over data shape, only over filter input
   shape.

## Implementation note: where `isValidDID` lives

`isValidDID` is currently in `internal/notifications/extractors/shared.go`.
The filter resolver in `internal/graphql/schema/where.go` needs the
same predicate, and importing the `extractors` package from the
schema package creates layering tension (schema → notifications is a
new dependency direction). Two viable shapes:

- **Move `isValidDID` to a new tiny package** `internal/atproto/did/`
  with one exported function `IsValid(string) bool`. Both call
  sites import that. Cleanest.
- **Inline a second copy** in `where.go`. Stays simple but allows
  drift.

Plan picks the first. Reviewer round 1 confirmed: adopt the new
package, retire `oauth.IsValidDID` → `oauth.HasDIDMethodPrefix`.

---

## Field description (pinned text)

The new GraphQL field carries the following description verbatim so
consumers see the policy at the schema-introspection boundary:

> Filter to activities where any `contributors[*].contributorIdentity`
> resolves to one of these DIDs. **DIDs only** — handle values are
> rejected at the GraphQL layer. Records whose contributor identity is
> a handle (not a DID) silently do not match — handle storage is a
> producer-side concern, not indexed as a queryable identity here.
> The strong-ref contributor variant
> (`com.atproto.repo.strongRef`) is not currently supported. To
> express "authored OR contributed" as a single query, compose with
> the `did` filter via `_or`:
> ```
> where: { _or: [
>   { did: { eq: "did:plc:me" } },
>   { contributor: { in: ["did:plc:me"] } }
> ] }
> ```

## Error message (pinned text)

When a value fails `did.IsValid`, the resolver returns:

> `contributor filter values must be DIDs (did:...); resolve handles
> to DIDs in the session layer — handle values are not indexed as a
> queryable identity`

The wrapped error includes the rejected value so the consumer's
debugging tooling shows which entry was bad.

## Loud-input / silent-data asymmetry

The DID-only policy is enforced two different ways at two
boundaries:

- **Loud at the input boundary**: a query that passes a non-DID
  value to the `contributor` filter returns a GraphQL validation
  error immediately. The consumer is asking us a question; we can
  tell them the rule.
- **Silent at the data boundary**: a stored record whose
  contributor identity is a handle (or any other non-DID string)
  simply does not match any query. We do not throw on every
  malformed record at query time — that would 5xx the public
  endpoint over upstream data quality, which is not our role.

We own the contract; producers own the data. Operators monitor
producer drift via the `contributor_identity_total{outcome="non_did"}`
counter.

---

## Reviewer roster (round 1)

Five lenses, parallel agents:

1. **GraphQL schema correctness** — field lands on the right type and
   only that type; `DIDFilterInput` composes correctly with
   `_and`/`_or` and existing args; descriptions accurate; the
   special-case branch in `where.go` doesn't leak to other lexicons.
2. **Postgres SQL correctness** — EXISTS pattern handles all edge
   cases (missing array, empty array, mixed contributor shapes,
   record missing `json->'contributors'` entirely); COALESCE
   semantics are correct under JSONB type system; parameter types
   line up (`text[]`, not `jsonb`); cursor pagination unaffected.
3. **Security** — DID validation in input rejects everything else;
   no injection surface in new SQL paths; length caps respected;
   COALESCE doesn't accidentally read into unintended JSON paths.
4. **Performance** — no-new-index decision is sound for the
   "popular contributor" worst case; query plan is acceptable;
   EXISTS subquery doesn't blow up on contributors arrays with
   hundreds of entries (defended at ingest by
   `MaxContributorsBeforeReject`).
5. **Policy / API consumer ergonomics** — DID-only policy is
   correctly enforced at the API boundary; metric outcomes capture
   what the operator wants to monitor; the asymmetry with the
   strong-ref variant is clearly documented in field description;
   the field name `contributor` is unambiguous to a consumer.

Round 2 only if round 1 surfaces ≥3 substantive findings or any
single critical finding.
