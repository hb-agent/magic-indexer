# Issue #65 — BadgeAward subject filter (+ nullable badge join, deferred)

**Issue**: [#65](https://github.com/hb-agent/magic-indexer/issues/65) — "AppCertifiedBadgeAward needs subject filter + nullable definition join."

> **PR scope**: this PR ships the **subject filter** only. The
> nullable badge-join half is deferred — see the
> "Nullable badge join — deferred" section below for why.

**Filed by**: certified-app consumer building the endorsement flow on `app.certified.badge.{definition,award,response}`. Without a subject filter the profile "Endorsements received" view has to do a network-wide PDS scan (~390 round-trips per cold profile view); with the filter it collapses to a single GraphQL query.

## Larger goal

Unblock the certified-app endorsement-Phase-2 read path. The
client-side `useReceivedEndorsements` hook currently enumerates every
known certified user via `appCertifiedActorProfile`, lists each one's
awards, and filters client-side to those targeting the profile DID.
That works because totalCount is small today (14) but doesn't scale.
The query the consumer wants:

```graphql
{
  appCertifiedBadgeAward(
    where: { subject: { eq: "did:plc:profile" } }
    first: 50
  ) { edges { node { uri did subject createdAt note } } }
}
```

A second, smaller blocker for the same consumer: `badge { ... }`
joins crash today because some awards reference definitions the
indexer doesn't hold and the join fields are marked non-null. The
read path doesn't actually need the join — it just needs the
filter — but fixing both keeps the schema honest for future
joiners.

## Two sub-features, one PR

### 1. `subject` filter on `AppCertifiedBadgeAwardWhereInput`

Single-collection special case in `where.go` + new SQL shape in
`repositories/filter.go`, modelled exactly on the contributor
filter shipped in #69 for `OrgHypercertsClaimActivityWhereInput`.

The badge.award `subject` is a union of `app.certified.defs#did`
(a string DID) and `com.atproto.repo.strongRef` (an object with
`uri` + `cid`). For "endorsements received" we want to filter on
DID regardless of which representation the issuer used. So a
single-value filter against a DID must match either shape:

```sql
value->>'subject' = $did
OR value->'subject'->>'uri' LIKE 'at://' || $did || '/%'
```

The strongRef branch needs LIKE because `subject.uri` is an AT-URI
that starts with the DID followed by `/<collection>/<rkey>`.

### 2. Nullable `AppCertifiedBadgeAward.badge` field — deferred

**Status**: deferred to a follow-up PR. Not shipped here.

**Why deferred**: probing the live indexer reveals it serves
`badge: AppCertifiedBadgeDefinition (NON_NULL)` — i.e. the
strongRef-to-record join is happening server-side. Building locally
from `main` produces `badge: ComAtprotoRepoStrongRef!` because no
auto-join code exists in `resolveRefType`
(`internal/graphql/types/object.go`): a ref to a `RecordDef`
resolves to that record's object type but no resolver is wired to
dereference the strongRef and load the target record from the DB.

So the precondition for "make `badge` nullable" is "build an
auto-join from strongRef-with-record-collection to the resolved
record type." That's a new resolver shape, not a one-line schema
tweak, and outside the surface area of the subject-filter work
shipped here. Filed as follow-up — see PR body for tracking.

## Alternatives considered

### For the subject filter

| Shape | Pros | Cons | Decision |
|---|---|---|---|
| **A. `subject: DIDFilterInput`** on the WhereInput (matches the #69 contributor pattern) | Composable with `_and`/`_or`/`did`; on-issue-author shape; reuses DIDFilterInput | Single-collection special case in `where.go` (same departure as #69) | **Chosen** — closest precedent already in the codebase |
| B. Top-level connection arg: `appCertifiedBadgeAward(subjectDids: [...])` (matches `authors` shortcut on `appCertifiedTempGraphEndorsement`) | Smallest API surface | Doesn't compose with `_or`/other filters; the issue asks for the WhereInput shape | Rejected |
| C. Generic nested filter via union introspection (`subject: { eq: ... }` works because subject is a DID-string union) | Solves a class of problems | Big schema-builder change; over-engineering today; risk of subtle bugs | Rejected — premature generalization. Per AGENTS.md §"deep flow" we don't run with the first proposal but we also don't generalize until we have ≥2 callers. |

### For the strongRef variant

| Approach | Pros | Cons | Decision |
|---|---|---|---|
| **A. Match both subject-as-DID-string AND subject-as-strongRef-uri (LIKE prefix)** | Filter works regardless of which representation an issuer wrote | LIKE precludes GIN-index `@>` short-circuit on the strongRef branch | **Chosen** — the consumer doesn't care which shape was written and shouldn't have to send two filters |
| B. Match string-only; let consumer query the strongRef variant separately | Cheaper SQL | Burden on every caller; defeats the convenience the filter exists for | Rejected |
| C. Normalise `subject` at write-time into a canonical field on the row | Fastest reads | Major migration; needs Jetstream reprocessing | Rejected (scope) |

### For the nullable-join fix

| Approach | Pros | Cons | Decision |
|---|---|---|---|
| **A. Make `badge` field nullable on the resolver; return nil when ref is unresolvable** | Honest; consumer can decide to skip | Slight API change (existing non-null clients get nullable now — but no callers currently query `badge` successfully so the change is moot) | **Chosen** |
| B. Filter out badge.award records whose `badge` ref doesn't resolve | Always-resolvable schema | Hides data; can flip on/off depending on indexer state (e.g. a definition arrives 5 min later) | Rejected — data should stay visible even when joins fail |
| C. Synthesise a placeholder definition record for unresolvable refs | Schema stays non-null | Lies about what's in the index | Rejected |

## Scope and file ownership

### Modified

- `internal/graphql/schema/where.go` — add `wantsBadgeAwardSubjectFilter(lexID)` predicate; add `subject` field to WhereInput when true; add `extractBadgeAwardSubjectFilter` special-case in the filter extraction.
- `internal/database/repositories/filter.go` — new `IsBadgeAwardSubject bool` field on `FieldFilter`; new `buildBadgeAwardSubjectFilter` SQL builder.

### New tests

- `internal/graphql/schema/builder_test.go`:
  - `TestBadgeAwardWhereInput_HasSubjectFilter` — positive (subject present) + negative (subject NOT leaked to activity WhereInput).
- `internal/database/repositories/filter_unit_test.go`:
  - `TestBuildBadgeAwardSubjectFilter` — `eq` DID matches both string and strongRef shapes; `in` with N DIDs; rejects non-DIDs; rejects empty lists.

### Out of scope

- `badge.uriIn` filter / nested `badge.badgeType` filter (the "optional but useful" items in #65). Defer to a follow-up — the subject filter alone unblocks the consumer.
- Indexing of currently-unhandled badge.award subject shapes (e.g. a future bare-record-URI form). The lexicon's union has exactly two shapes today.

## Acceptance criteria

1. `appCertifiedBadgeAward(where: { subject: { eq: "did:plc:X" } })` returns awards whose subject is the string DID `"did:plc:X"` AND awards whose subject is a strongRef `{uri: "at://did:plc:X/...", cid: ...}`.
2. `appCertifiedBadgeAward(where: { subject: { in: ["did:plc:A", "did:plc:B"] } })` matches the OR of single-value matches.
3. The filter rejects non-DID values with `"subject filter values must be DIDs"` at the GraphQL layer.
4. _(Deferred — see "Nullable badge join — deferred" above.)_
5. `go vet ./...`, `go build ./...`, `go test ./internal/graphql/schema/... ./internal/database/repositories/...` all pass.

## Rollback plan

Single revert restores prior schema. New filter is additive — existing
queries without `subject` see no change. Nullable-join change is
schema-compatible for any non-asserting client; an asserting client
(non-null expectation in their generated types) would need a regen
but no behavioural break.
