# Implementation Plan: Trusted-Evaluator Feed Filter

Companion to [ADR 0001](./0001-trusted-evaluator-feed-filter.md). This
document is the actionable plan: what to build, in what order, and how to
verify. A fresh contributor (human or AI) should be able to execute every
step from top to bottom with no external context except this document and
the codebase.

## Scope

Two repositories, two phases.

- **Magic Indexer (Go, `hb-agent/magic-indexer`)**: generic `authors`
  filter primitive, unified `RecordFilter` refactor, supporting index,
  resolver wiring, tests, deploy, metrics.
- **certs.social (Next.js, `hypercerts-org/certs-social`)**: evaluator
  DID list, toggle UI, endorsement-resolution hook, feed hook
  integration, attribution UI, empty states, revocation propagation.

Phase 1 is a strict prerequisite for phase 2. Client scaffolding against a
mocked backend can proceed in parallel but integration testing is blocked
on Phase 1 deploy.

## Pre-flight check

Before starting, verify the following state on the live development
indexer. These should already be true; if any are not, resolve before
proceeding.

- `https://magic-indexer-dev.up.railway.app/stats` reports
  `lexicons >= 24` and includes `app.certified.temp.graph.endorsement`
  (introspect the GraphQL schema for
  `AppCertifiedTempGraphEndorsement` type).
- `https://magic-indexer-dev.up.railway.app` serves the
  `orgHypercertsClaimActivity` query with a non-zero `totalCount` or
  visible edges.
- The CORS allowlist on the indexer includes
  `https://certs-social-*.vercel.app` so the certs.social preview
  deployment can reach the indexer from the browser.
- A working endorsement record exists for end-to-end verification, or is
  easy to create via `com.atproto.repo.createRecord` against the
  evaluator DID's PDS.

## Phase 1: Magic Indexer backend

### 1.1 Migration: supporting compound index

The new filter predicate is `WHERE collection = ? AND did IN (...)
ORDER BY indexed_at DESC, uri DESC`. Without a supporting index, Postgres
falls back to BitmapOr over `idx_record_did_collection` followed by a sort,
which defeats the keyset cursor's stop-at-`LIMIT` property. SQLite picks
one index and sorts, which is worse.

Add a composite index matching the query shape.

**File**: `internal/database/migrations/postgres/010_add_record_collection_did_keyset.up.sql`

```sql
CREATE INDEX IF NOT EXISTS idx_record_collection_did_keyset
  ON record (collection, did, indexed_at DESC, uri DESC);
ANALYZE record;
```

**File**: `internal/database/migrations/postgres/010_add_record_collection_did_keyset.down.sql`

```sql
DROP INDEX IF EXISTS idx_record_collection_did_keyset;
```

**File**: `internal/database/migrations/sqlite/010_add_record_collection_did_keyset.up.sql`

```sql
CREATE INDEX IF NOT EXISTS idx_record_collection_did_keyset
  ON record (collection, did, indexed_at DESC, uri DESC);
ANALYZE;
```

**File**: `internal/database/migrations/sqlite/010_add_record_collection_did_keyset.down.sql`

```sql
DROP INDEX IF EXISTS idx_record_collection_did_keyset;
```

Run the existing migration round-trip test harness (from issue #17) to
verify up and down both work against an existing record table.

### 1.2 Unified `RecordFilter` struct

The current repository has two sibling methods:
`GetByCollectionWithKeysetCursor` and
`GetByCollectionWithLabelFilterAndKeysetCursor`. Adding a third sibling
`GetByCollectionWithAuthorsAndKeysetCursor` would force a fourth
(`...WithAuthorsAndLabelFilterAndKeysetCursor`) as soon as the resolver
wants to compose the two, and so on — 2^N methods per filter axis.

Refactor into one method taking a filter struct.

**File**: `internal/database/repositories/records.go`

Add near the top of the file, with the other types:

```go
// RecordFilter composes the filter axes applied to record collection
// queries. Zero-value means "no filter." Each field has its own
// distinct "empty" semantic — see field comments.
type RecordFilter struct {
    // Authors is the "any of" author-DID filter. Semantics:
    //   nil         → no author filter applied
    //   []string{}  → return zero results without querying the DB
    //                 (load-bearing: a client bug producing an empty
    //                 slice must NOT silently degrade to "no filter"
    //                 and show the full firehose)
    //   [a, b, ...] → return records with did IN (a, b, ...)
    //
    // Callers must distinguish nil from empty explicitly. The resolver
    // layer is responsible for preserving that distinction when parsing
    // the GraphQL argument.
    Authors []string

    // Labels are the existing label include/exclude sets. Unchanged.
    Labels LabelFilter
}

// IsEmpty reports whether the filter has no active constraints.
func (f RecordFilter) IsEmpty() bool {
    return f.Authors == nil && f.Labels.IsEmpty()
}

// MaxAuthorsFilterSize is the server-enforced cap on the number of DIDs
// passed in the Authors filter. Chosen to stay comfortably under
// SQLite's default 999-parameter limit and to keep Postgres planner
// estimates honest. Exceeding it returns ErrAuthorsFilterTooLarge from
// the repository.
const MaxAuthorsFilterSize = 500

// ErrAuthorsFilterTooLarge is returned when RecordFilter.Authors has
// more than MaxAuthorsFilterSize entries.
var ErrAuthorsFilterTooLarge = errors.New("authors filter exceeds maximum size")
```

Add the unified method:

```go
// GetByCollectionFiltered returns a page of records for the given
// collection, applying the supplied filter and keyset cursor. This is
// the canonical query method — the older sibling methods
// (GetByCollectionWithKeysetCursor,
// GetByCollectionWithLabelFilterAndKeysetCursor) are now thin wrappers
// and will be removed in a follow-up cleanup.
func (r *RecordsRepository) GetByCollectionFiltered(
    ctx context.Context,
    collection string,
    limit int,
    afterIndexedAt, afterURI string,
    filter RecordFilter,
) ([]*Record, error) {
    // Empty-not-nil authors = explicit "no results" short circuit.
    // Load-bearing: see struct doc.
    if filter.Authors != nil && len(filter.Authors) == 0 {
        slog.Info("records filter short-circuited on empty authors",
            "collection", collection,
        )
        return nil, nil
    }

    // Enforce the DID-list size cap.
    if len(filter.Authors) > MaxAuthorsFilterSize {
        return nil, ErrAuthorsFilterTooLarge
    }

    // Dedup + sort authors for stable query plan and predictable binding.
    // Sort is required by slices.Compact, which only removes consecutive
    // duplicates.
    authors := filter.Authors
    if len(authors) > 1 {
        authors = slices.Clone(authors)
        sort.Strings(authors)
        authors = slices.Compact(authors)
    }

    // Build per-dialect SQL. The shared helper returns the WHERE
    // clause fragments and args, and the caller composes them with
    // the keyset cursor and LIMIT.
    whereClauses, args, err := r.buildRecordFilterClauses(collection, afterIndexedAt, afterURI, authors, filter.Labels)
    if err != nil {
        return nil, err
    }

    // ... existing query assembly and execution, identical to
    // GetByCollectionWithLabelFilterAndKeysetCursor except using the
    // shared builder
}
```

Extract the SQL-building logic that currently lives inline in
`GetByCollectionWithLabelFilterAndKeysetCursor` into a private helper:

```go
// buildRecordFilterClauses constructs the per-dialect WHERE clause
// fragments and args for the given filter inputs. It handles the
// dialect-aware placeholder generator, the authors-IN clause when
// authors is non-empty, and the label-include/exclude subqueries when
// labels is non-empty.
func (r *RecordsRepository) buildRecordFilterClauses(
    collection, afterIndexedAt, afterURI string,
    authors []string,
    labels LabelFilter,
) (whereClauses []string, args []any, err error) {
    // ... dialect-aware assembly, identical placeholder-generation
    // pattern to the existing code in records.go:369-453
}
```

The `authors` predicate appends to `whereClauses`:

- SQLite: `r.did IN (?, ?, ..., ?)` with one placeholder per DID.
- Postgres: `r.did IN ($N, $N+1, ..., $N+len-1)`.

Existing sibling methods become thin wrappers:

```go
func (r *RecordsRepository) GetByCollectionWithKeysetCursor(
    ctx context.Context,
    collection string,
    limit int,
    afterIndexedAt, afterURI string,
) ([]*Record, error) {
    return r.GetByCollectionFiltered(ctx, collection, limit, afterIndexedAt, afterURI, RecordFilter{})
}

func (r *RecordsRepository) GetByCollectionWithLabelFilterAndKeysetCursor(
    ctx context.Context,
    collection string,
    limit int,
    afterIndexedAt, afterURI string,
    labels LabelFilter,
) ([]*Record, error) {
    return r.GetByCollectionFiltered(ctx, collection, limit, afterIndexedAt, afterURI, RecordFilter{Labels: labels})
}
```

Mark both wrappers as `// Deprecated: use GetByCollectionFiltered` but
do not delete yet. Deletion is a follow-up PR after all call sites
migrate.

### 1.3 GraphQL schema: `authors` argument on `ConnectionArgs`

**File**: `internal/graphql/query/connection.go`

Add to `ConnectionArgs`:

```go
"authors": &graphql.ArgumentConfig{
    Type: graphql.NewList(graphql.NewNonNull(graphql.String)),
    Description: `Filter to records authored by any of these DIDs.

Semantics:
- Omitted or null: no filter applied.
- Empty list []: returns zero results (explicit "match nothing" signal).
- Non-empty list: returns records whose publishing DID is in the list.

Maximum 500 DIDs per query. Duplicates are deduplicated server-side;
order is not significant. DIDs are case-sensitive per the ATProto spec.`,
},
```

Add a parser helper alongside the existing `parseLabelFilter`:

```go
// parseAuthorsFilter extracts the "authors" argument from GraphQL args.
// Returns:
//   (nil,  nil) — argument omitted or explicitly null (no filter)
//   (&[],  nil) — empty list (explicit "match nothing" signal)
//   (&[..],nil) — non-empty list
//   (nil,  err) — malformed (e.g. non-string elements, or exceeds cap)
//
// The pointer-to-slice return type is load-bearing: distinguishing
// "omitted" from "empty list" is the primary semantic distinction and
// cannot be represented with a plain slice return.
func parseAuthorsFilter(args map[string]interface{}) (*[]string, error) {
    raw, present := args["authors"]
    if !present || raw == nil {
        return nil, nil
    }
    list, ok := raw.([]interface{})
    if !ok {
        return nil, fmt.Errorf("authors argument must be a list")
    }
    dids := make([]string, 0, len(list))
    for _, e := range list {
        s, ok := e.(string)
        if !ok {
            return nil, fmt.Errorf("authors elements must be strings")
        }
        dids = append(dids, s)
    }
    if len(dids) > MaxAuthorsFilterSize {
        return nil, fmt.Errorf("authors filter exceeds maximum of %d", MaxAuthorsFilterSize)
    }
    return &dids, nil
}
```

Export `MaxAuthorsFilterSize` from the repositories package and re-export
from the query package, or duplicate the constant with a comment — pick
one for consistency with the existing codebase style.

### 1.4 Resolver wiring

**File**: `internal/graphql/schema/builder.go`

In `resolveRecordConnection` (and `createCollectionResolver`):

```go
authorsFilter, err := query.ParseAuthorsFilter(p.Args)
if err != nil {
    return nil, err
}

labelFilter, err := query.ParseLabelFilter(p.Args) // existing
if err != nil {
    return nil, err
}

filter := repositories.RecordFilter{
    Labels: labelFilter,
}
if authorsFilter != nil {
    filter.Authors = *authorsFilter // may be empty slice = "no results"
}

// Instrument before the call so we count both hits and short-circuits.
metrics.RecordsAuthorsFilterApplied.WithLabelValues(collection).Inc()
if authorsFilter != nil {
    metrics.RecordsAuthorsFilterSize.Observe(float64(len(*authorsFilter)))
    if len(*authorsFilter) == 0 {
        metrics.RecordsAuthorsFilterEmptyBlocked.Inc()
    }
}

records, err := repo.GetByCollectionFiltered(
    ctx, collection, limit, afterIndexedAt, afterURI, filter,
)
```

Update `createSingleRecordResolver` similarly where applicable. Note that
single-record queries (`*ByUri`) don't typically need an `authors` filter
since they're URI-addressed; skip unless there's a concrete use case.

### 1.5 Observability

**File**: wherever Prometheus metrics are registered (from issue #56)

Add the following counters and histograms:

```go
var (
    RecordsAuthorsFilterApplied = prometheus.NewCounterVec(
        prometheus.CounterOpts{
            Name: "records_authors_filter_applied_total",
            Help: "Total authors-filter-applied record queries, by collection",
        },
        []string{"collection"},
    )

    RecordsAuthorsFilterSize = prometheus.NewHistogram(
        prometheus.HistogramOpts{
            Name:    "records_authors_filter_size",
            Help:    "Histogram of authors-filter list sizes",
            Buckets: []float64{1, 5, 10, 25, 50, 100, 250, 500},
        },
    )

    RecordsAuthorsFilterEmptyBlocked = prometheus.NewCounter(
        prometheus.CounterOpts{
            Name: "records_authors_filter_empty_blocked_total",
            Help: "Total queries short-circuited due to empty authors filter",
        },
    )

    RecordsAuthorsFilterTooLarge = prometheus.NewCounter(
        prometheus.CounterOpts{
            Name: "records_authors_filter_too_large_total",
            Help: "Total queries rejected for exceeding MaxAuthorsFilterSize",
        },
    )
)
```

Register with the existing metrics registry. Alert on
`rate(records_authors_filter_empty_blocked_total[5m]) > X` (pick X based on
expected baseline after ship) so a client bug producing chronic empty
filters is visible quickly.

### 1.6 Tests

**File**: `internal/database/repositories/records_filter_test.go`
(new file, mirrors the existing `records_labels_test.go` pattern)

Table-driven tests, dual-dialect via existing `testutil.SetupTestDB`:

```go
func TestGetByCollectionFiltered_Authors(t *testing.T) {
    cases := []struct {
        name        string
        setup       func(t *testing.T, db Database)
        filter      RecordFilter
        wantErr     error
        wantLen     int
        wantURIs    []string  // ordered, if set
    }{
        {
            name: "no filter matches all",
            // ...
        },
        {
            name: "authors nil matches all",
            filter: RecordFilter{Authors: nil},
            // ...
        },
        {
            name: "authors empty slice matches nothing",
            filter: RecordFilter{Authors: []string{}},
            wantLen: 0,
            // and: assert no SQL was executed against a tracing wrapper
        },
        {
            name: "authors single DID matches records by that DID only",
            // ...
        },
        {
            name: "authors multiple DIDs matches union",
            // ...
        },
        {
            name: "authors deduplicates input",
            filter: RecordFilter{Authors: []string{"did:plc:a", "did:plc:a"}},
            // ...
        },
        {
            name: "authors at cap (500) succeeds",
            filter: RecordFilter{Authors: make501UniqueDIDs(t, 500)},
            // ...
        },
        {
            name: "authors exceeds cap returns ErrAuthorsFilterTooLarge",
            filter: RecordFilter{Authors: make501UniqueDIDs(t, 501)},
            wantErr: ErrAuthorsFilterTooLarge,
        },
        {
            name: "authors does not leak across collections",
            // seed: records for DID X in collection A and collection B
            // query: collection A with Authors=[X]
            // assert: only collection A results returned
        },
        {
            name: "authors composes with label include",
            // seed: records with various labels from various authors
            // filter: Authors = [X, Y], Labels.Include = [high-quality]
            // assert: intersection
        },
        {
            name: "authors composes with label exclude",
            // ...
        },
        {
            name: "authors + label include + label exclude three-way composition",
            // ...
        },
        {
            name: "keyset pagination stability under authors filter",
            // seed: 3N records across M authors with colliding indexed_at
            //       to force the URI tiebreaker
            // query: Authors = [X, Y], paginate with limit = N
            // assert: round-trip equals single-shot, no duplicates, no skips
        },
        {
            name: "DIDs are case-sensitive",
            // seed: records for did:plc:ABC
            // filter: Authors = [did:plc:abc]
            // assert: zero results
        },
    }
    // run with both SQLite and Postgres via existing harness
}
```

Add an EXPLAIN-plan assertion test (Postgres only, gated on CI-with-
Postgres from issue #49):

```go
func TestGetByCollectionFiltered_UsesAuthorsKeysetIndex(t *testing.T) {
    if !hasPostgres(t) {
        t.Skip("requires Postgres")
    }
    // seed representative data
    // EXPLAIN (ANALYZE, FORMAT TEXT) SELECT ... with authors + keyset cursor
    // assert plan contains "idx_record_collection_did_keyset"
    // assert plan does NOT contain "Sort" at the top level
}
```

Without this assertion the new index can silently rot if schema changes
elsewhere shift the planner's choice.

### 1.7 Deploy and smoke

1. Commit in four commits off `per-labeler-definitions`:
   - `feat(records): migration adds collection+did+keyset compound index`
   - `refactor(records): unified RecordFilter replaces sibling methods`
   - `feat(graphql): generic authors filter on record connections`
   - `feat(metrics): authors filter observability`

2. Push and verify CI green (build + dual-dialect tests + round-trip
   migration test).

3. Deploy to `magic-indexer-dev` on Railway.
   `railway redeploy` uses the last-built image rather than pulling fresh
   source, so either rebuild from local via `railway up --service
   magic-indexer --environment dev` or push a new commit to the branch
   Railway tracks and let auto-deploy handle it.

4. Verify `/stats` is live and the version matches the new commit
   (check deploy logs for the new migration line).

5. Run a smoke GraphQL query via the admin GraphiQL at
   `/graphiql/admin`:

   ```graphql
   {
     orgHypercertsClaimActivity(
       first: 2
       authors: ["did:plc:cpoagodpqrgs4t7thi5z37uf"]
     ) {
       edges { node { uri did } }
     }
   }
   ```

   Expect records for that DID. Run with `authors: []` and verify empty
   result. Run with `authors: [...] labels: ["standard"]` and verify
   intersection.

6. Check `/metrics` for `records_authors_filter_*` counters. Trigger each
   code path at least once and verify the counter increments.

## Phase 2: certs.social client

Phase 2 is unblocked once Phase 1 is deployed.

### 2.1 Trusted-evaluator list

**File**: `src/config/trusted-evaluators.ts` (new)

```ts
/**
 * Trusted evaluator DIDs whose endorsement records feed the certs.social
 * "Trusted" feed filter. Committed to this repository as the authoritative
 * source of truth. Changes go through normal PR review.
 *
 * Curation policy:
 *   - Each entry is a DID that certs.social considers a credible
 *     endorser of hypercert work. See
 *     docs/architecture/0001-trusted-evaluator-feed-filter.md for the
 *     design context.
 *   - Entries are additive only by default. Removal of an existing entry
 *     is a user-visible action (existing users who had that evaluator
 *     toggled on will silently lose it on next page load) and should be
 *     discussed before merging.
 *   - New entries are ON by default for new users and OFF by default for
 *     existing users (preserving their explicit opt-in set).
 *
 * The list is a flat array of DID strings — no names, avatars, or
 * metadata. The app resolves display information for each evaluator at
 * runtime from their `app.certified.actor.profile` record via the
 * indexer.
 */
export const TRUSTED_EVALUATORS: ReadonlyArray<string> = [
  "did:plc:nfd56jdcukm76ogr43ckjvr7",
  // Add entries here via PR.
];
```

**File**: `src/config/__tests__/trusted-evaluators.test.ts` (new)

Unit test asserting every entry matches the ATProto DID format:
`/^did:(plc|web):[a-z0-9\-.:]+$/i`. Trivial but prevents accidentally
committing a garbage string.

**CODEOWNERS**: add an entry covering `src/config/trusted-evaluators.ts`
pointing at the product owner. Changes to this file are a product
decision, not an engineering one.

### 2.2 Active-evaluators hook

**File**: `src/hooks/use-active-evaluators.ts` (new)

```ts
"use client"

import { useCallback, useMemo, useState } from "react"
import { TRUSTED_EVALUATORS } from "@/config/trusted-evaluators"

const STORAGE_KEY = "activeEvaluators.v1"

/**
 * Tracks which trusted evaluators the current user has toggled on.
 * Persisted to localStorage. Default: all evaluators from
 * TRUSTED_EVALUATORS enabled.
 *
 * Returns a `stableKey` derived from the active set (sorted, joined).
 * Downstream hooks should include this key in their react-query cache
 * keys so toggling an evaluator produces a deterministic refetch.
 */
export function useActiveEvaluators() {
  const [active, setActive] = useState<Set<string>>(() => initialActive())

  const persist = useCallback((next: Set<string>) => {
    if (typeof window === "undefined") return
    window.localStorage.setItem(STORAGE_KEY, JSON.stringify(Array.from(next)))
  }, [])

  const toggle = useCallback(
    (did: string) => {
      setActive(prev => {
        const next = new Set(prev)
        if (next.has(did)) next.delete(did)
        else if (TRUSTED_EVALUATORS.includes(did)) next.add(did)
        persist(next)
        return next
      })
    },
    [persist],
  )

  const selectAll = useCallback(() => {
    const next = new Set<string>(TRUSTED_EVALUATORS)
    persist(next)
    setActive(next)
  }, [persist])

  const deselectAll = useCallback(() => {
    const next = new Set<string>()
    persist(next)
    setActive(next)
  }, [persist])

  const activeList = useMemo(
    () => Array.from(active).sort(),
    [active],
  )

  const stableKey = useMemo(() => activeList.join(","), [activeList])

  return { active, activeList, toggle, selectAll, deselectAll, stableKey }
}

function initialActive(): Set<string> {
  if (typeof window === "undefined") {
    return new Set(TRUSTED_EVALUATORS)
  }
  const stored = window.localStorage.getItem(STORAGE_KEY)
  if (!stored) return new Set(TRUSTED_EVALUATORS)
  try {
    const parsed = JSON.parse(stored) as string[]
    const valid = new Set(TRUSTED_EVALUATORS)
    // Prune entries no longer in the registry (evaluator removed from
    // TRUSTED_EVALUATORS). New entries are NOT silently added —
    // existing users keep their explicit opt-in set.
    return new Set(parsed.filter(d => valid.has(d)))
  } catch {
    return new Set(TRUSTED_EVALUATORS)
  }
}
```

### 2.3 Indexer client: `fetchEndorsements`

**File**: `src/lib/atproto/indexer.ts` (extend existing)

Add alongside the existing `ACTIVITIES_QUERY` and `fetchIndexerActivities`:

```ts
const ENDORSEMENTS_QUERY = `
query Endorsements($authors: [String!]!, $first: Int!, $after: String) {
  appCertifiedTempGraphEndorsement(
    first: $first
    after: $after
    authors: $authors
  ) {
    edges {
      cursor
      node {
        uri
        did
        subject
        createdAt
      }
    }
    pageInfo { hasNextPage endCursor }
  }
}
`

export interface EndorsementRecord {
  uri: string
  author: string
  subject: string
  createdAt: string
}

export interface FetchEndorsementsOptions {
  authors: string[]
  signal?: AbortSignal
}

/**
 * Fetch all endorsement records authored by the given evaluator DIDs,
 * paginating through the indexer until the connection is exhausted or
 * the safety cap is hit.
 *
 * Returns [] if authors is empty (short-circuits without a network call).
 */
export async function fetchEndorsements(
  options: FetchEndorsementsOptions,
): Promise<EndorsementRecord[]> {
  if (options.authors.length === 0) return []

  const PAGE_SIZE = 100
  const SAFETY_CAP = 10_000 // never paginate beyond this

  const all: EndorsementRecord[] = []
  let cursor: string | null = null

  while (all.length < SAFETY_CAP) {
    const res = await fetch(INDEXER_URL, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({
        query: ENDORSEMENTS_QUERY,
        variables: {
          authors: options.authors,
          first: PAGE_SIZE,
          after: cursor,
        },
      }),
      signal: options.signal,
    })
    if (!res.ok) {
      throw new Error(`Indexer request failed: ${res.status}`)
    }
    const json = (await res.json()) as {
      data: {
        appCertifiedTempGraphEndorsement: {
          edges: { node: { uri: string; did: string; subject: string; createdAt: string } }[]
          pageInfo: { hasNextPage: boolean; endCursor: string | null }
        }
      }
      errors?: { message: string }[]
    }
    if (json.errors?.length) {
      throw new Error(`Indexer GraphQL error: ${json.errors[0].message}`)
    }
    const connection = json.data.appCertifiedTempGraphEndorsement
    for (const edge of connection.edges) {
      all.push({
        uri: edge.node.uri,
        author: edge.node.did,
        subject: edge.node.subject,
        createdAt: edge.node.createdAt,
      })
    }
    if (!connection.pageInfo.hasNextPage) break
    cursor = connection.pageInfo.endCursor
    if (!cursor) break
  }

  return all
}
```

Extend `FetchIndexerOptions` and `ACTIVITIES_QUERY` to accept `authors`:

```ts
const ACTIVITIES_QUERY = `
query Activities(
  $first: Int!
  $after: String
  $labels: [String!]
  $excludeLabels: [String!]
  $authors: [String!]
) {
  orgHypercertsClaimActivity(
    first: $first
    after: $after
    labels: $labels
    excludeLabels: $excludeLabels
    authors: $authors
  ) {
    # ... existing selection
  }
}
`

export interface FetchIndexerOptions {
  first?: number
  after?: string
  labels?: LabelValue[]
  excludeLabels?: LabelValue[] | string[]
  authors?: string[]          // NEW
  signal?: AbortSignal
}

// In fetchIndexerActivities, add to variables:
//   authors: authors && authors.length > 0 ? authors : (authors?.length === 0 ? [] : null)
// Preserve the nil-vs-empty distinction by passing null for "no filter"
// and [] for "explicit empty."
```

### 2.4 Trusted endorsed-DIDs hook

**File**: `src/hooks/use-trusted-endorsed-dids.ts` (new)

```ts
"use client"

import { useQuery } from "@tanstack/react-query"
import { fetchEndorsements, type EndorsementRecord } from "@/lib/atproto/indexer"

export interface EvaluatorAttribution {
  evaluatorDid: string
  createdAt: string
}

export interface TrustedEndorsedDids {
  endorsedDids: Set<string>
  /** Map from endorsed subject DID → list of evaluators who endorsed them. */
  attribution: Map<string, EvaluatorAttribution[]>
  totalEndorsements: number
}

const EMPTY: TrustedEndorsedDids = {
  endorsedDids: new Set(),
  attribution: new Map(),
  totalEndorsements: 0,
}

/**
 * Fetches all endorsement records authored by the given active
 * evaluator list and derives both the endorsed-DID set (for filtering
 * the feed) and an attribution map (for the "Endorsed by @alice" chip
 * on each activity card).
 *
 * Empty activeEvaluators → returns EMPTY without a network call.
 *
 * Cache key includes stableKey so toggling an evaluator refetches.
 */
export function useTrustedEndorsedDids(
  activeEvaluators: string[],
  stableKey: string,
) {
  return useQuery<TrustedEndorsedDids>({
    queryKey: ["trusted-endorsed-dids", stableKey],
    queryFn: async ({ signal }) => {
      if (activeEvaluators.length === 0) return EMPTY
      const records = await fetchEndorsements({
        authors: activeEvaluators,
        signal,
      })
      return buildTrustedEndorsedDids(records)
    },
    staleTime: 5 * 60 * 1000,
    refetchOnWindowFocus: true, // catch revocations on tab return
    placeholderData: prev => prev ?? EMPTY,
  })
}

function buildTrustedEndorsedDids(
  records: EndorsementRecord[],
): TrustedEndorsedDids {
  // Dedup by (author, subject) — multiple endorsements from the same
  // evaluator for the same subject collapse into one. Keep the most
  // recent.
  const byPair = new Map<string, EndorsementRecord>()
  for (const r of records) {
    const key = `${r.author}\0${r.subject}`
    const existing = byPair.get(key)
    if (!existing || r.createdAt > existing.createdAt) {
      byPair.set(key, r)
    }
  }
  const endorsedDids = new Set<string>()
  const attribution = new Map<string, EvaluatorAttribution[]>()
  for (const r of byPair.values()) {
    endorsedDids.add(r.subject)
    const existing = attribution.get(r.subject) ?? []
    existing.push({ evaluatorDid: r.author, createdAt: r.createdAt })
    attribution.set(r.subject, existing)
  }
  return { endorsedDids, attribution, totalEndorsements: byPair.size }
}
```

### 2.5 Feed hook composition

**File**: `src/hooks/use-global-feed.ts` (modify existing)

1. Accept a new optional prop: `endorsedDids?: Set<string>`.
2. Extend the stable-key computation to include a hash of the
   endorsedDids set:

   ```ts
   const serverAuthorsFilterKey = useMemo<string>(() => {
     if (!endorsedDids) return ""
     // Sort for stability across renders.
     return Array.from(endorsedDids).sort().join(",")
   }, [endorsedDids])
   ```

   Then combine with the existing label key:

   ```ts
   const combinedServerFilterKey = `${serverLabelFilterKey}|${serverAuthorsFilterKey}`
   ```

   Use `combinedServerFilterKey` as the effect dependency instead of just
   `serverLabelFilterKey`.

3. When calling `fetchIndexerActivities`, pass `authors`:

   ```ts
   const data = await fetchIndexerActivities({
     first: PAGE_SIZE,
     labels: labelsArg,
     authors: endorsedDids ? Array.from(endorsedDids) : undefined,
     signal,
   })
   ```

4. Preserve the load-bearing empty-set semantic. If
   `endorsedDids !== undefined && endorsedDids.size === 0`, the hook
   should pass `authors: []` to the backend and the backend returns
   empty. Do NOT collapse "empty set" to "no filter."

### 2.6 Feed page integration

In the feed page (wherever `useGlobalFeed` is called — likely
`src/app/page.tsx` or equivalent):

```tsx
const { active, activeList, toggle, selectAll, deselectAll, stableKey } =
  useActiveEvaluators()

const { data: trusted, isLoading: trustedLoading } =
  useTrustedEndorsedDids(activeList, stableKey)

const [trustedMode, setTrustedMode] = useLocalStorage<boolean>(
  "feed.trustedMode",
  true,
)

const {
  activities,
  isLoading,
  isLoadingMore,
  hasMore,
  loadMore,
  error,
  selectedLabels,
  setSelectedLabels,
} = useGlobalFeed({
  endorsedDids: trustedMode ? trusted?.endorsedDids : undefined,
})
```

### 2.7 UI surfaces

#### 2.7.1 Feed header toggle

A two-state toggle in the feed header: "Trusted" / "All". Default:
"Trusted". Persists via `useLocalStorage`. Rendered prominently.

#### 2.7.2 Evaluator picker

When `trustedMode === true`, a chip next to the toggle shows
"N of M evaluators" and opens a modal/drawer on click. The picker lists
every entry in `TRUSTED_EVALUATORS` with:

- Avatar (fetched via `appCertifiedActorProfile(authors: [did])`, cached;
  fall back to a DID-initial placeholder if no profile exists).
- Display name (from the profile, fall back to truncated DID).
- Handle (from the profile, if present).
- Toggle switch wired to `useActiveEvaluators.toggle`.
- Count of current active endorsements from that evaluator (derived
  from `trusted.attribution`).
- "Select all" / "Deselect all" footer buttons.

Evaluator profile data should be fetched once on picker open (or
pre-fetched on page load) via:

```graphql
{
  appCertifiedActorProfile(authors: [...TRUSTED_EVALUATORS]) {
    edges { node { uri did value { displayName avatar } } }
  }
}
```

Wrap in a dedicated `useEvaluatorProfiles()` hook for reusability.

#### 2.7.3 Attribution chip on activity cards

When `trustedMode === true`, each activity card shows, below the author
line:

```
Endorsed by @alice, @bob and 2 more
```

Truncation rule:

- 1 endorser → "Endorsed by @alice"
- 2 endorsers → "Endorsed by @alice, @bob"
- 3 endorsers → "Endorsed by @alice, @bob, @charlie"
- 4+ endorsers → "Endorsed by @alice, @bob, @charlie and N more"

Clicking the chip opens a popover listing all endorsers with their
full display names and endorsement dates.

Handles for the attribution are resolved via the same
`useEvaluatorProfiles()` hook — these are the evaluators, a fixed set,
so profile data is cached aggressively.

#### 2.7.4 Empty states

Three distinct empty states:

1. **No evaluators active** (user toggled everyone off):

   > You have no trusted evaluators selected.
   > Select at least one evaluator to see endorsed activity.
   > [Open evaluator picker] [Switch to All]

2. **Active evaluators, zero matching activities** (trust graph is too
   sparse):

   > No activities from people endorsed by your selected evaluators yet.
   > [Adjust evaluators] [Switch to All]

3. **Backend error**: reuse existing feed error UI.

### 2.8 Revocation propagation

For v1, use polling:

- `useTrustedEndorsedDids` sets `refetchOnWindowFocus: true` and
  `staleTime: 5 min`. Revocations propagate within 5 min in the
  background, instantly on tab return.

For v2 (deferred, separate ticket): subscribe to Magic Indexer's
`/graphql/ws` subscription endpoint and invalidate the trusted-endorsed-
dids query on delete events for `app.certified.temp.graph.endorsement`.
This gives near-instant propagation without the polling overhead.

### 2.9 Testing

- **Unit tests** for `useActiveEvaluators`:
  - Default state is all evaluators on.
  - localStorage restore works.
  - Toggle adds/removes cleanly.
  - stableKey updates deterministically.
  - Pruning removes entries no longer in TRUSTED_EVALUATORS.
- **Unit tests** for `buildTrustedEndorsedDids`:
  - Dedup by `(author, subject)` keeps most recent.
  - Attribution map is correctly populated.
- **Integration test** via MSW or similar:
  - Mock the indexer's endorsements and activities endpoints.
  - Render the feed page with the hooks wired.
  - Toggle an evaluator off and assert the feed refetches with a
    different `authors` set.
  - Toggle into "All" mode and assert the feed refetches without
    `authors`.
- **Manual end-to-end verification** via `ab` against the staging
  deployment after Phase 1 ships.

### 2.10 Deploy

Standard certs.social deploy via Vercel from `staging`. Verify in a real
browser via `ab` after deploy.

## Phase 3: End-to-end verification

After both phases are deployed, run the following sequence on the live
staging deployment. Use `ab` or the operator's own browser.

1. Confirm the feed loads in "Trusted" mode by default.
2. Open the evaluator picker; confirm all registered evaluators appear
   with names and endorsement counts.
3. Issue a new endorsement record from an evaluator DID to a known
   author via `com.atproto.repo.createRecord`.
4. Wait ~5 seconds for Jetstream ingestion (visible in indexer logs).
5. Force a refresh of the trusted-endorsed-dids hook (focus the tab or
   wait for stale time).
6. Confirm the newly-endorsed author's activities now appear in the feed
   with an attribution chip.
7. Toggle the evaluator off. Confirm the feed refetches and those
   activities disappear.
8. Toggle the evaluator back on. Confirm the activities return.
9. Delete the endorsement record from the evaluator's PDS.
10. Wait for propagation (up to 5 min on v1 polling, or force tab focus).
11. Confirm the formerly-endorsed author's activities disappear.
12. Deselect all evaluators. Confirm the empty state appears with the
    correct copy and CTAs.
13. Switch to "All" mode. Confirm the full feed returns.

Every step should pass without manual intervention beyond the
toggle/delete actions called out explicitly.

## Phase 4: Cleanup (deferred)

- **Delete deprecated sibling methods** on the records repository
  (`GetByCollectionWithKeysetCursor`,
  `GetByCollectionWithLabelFilterAndKeysetCursor`) once all callers
  have migrated to `GetByCollectionFiltered`. Follow-up PR after bake
  time.
- **WebSocket-based revocation propagation** replacing the polling
  fallback. Separate ticket, not blocking ship.
- **Feed generator wrap** for cross-Bluesky-ecosystem exposure. Covered
  in the ADR's "Future direction" section; not in scope here.

## Rollback

- **Backend rollback**: revert the four commits and redeploy. The
  migration's `.down.sql` file removes the new index cleanly. Existing
  queries continue to work because the old sibling methods are preserved
  as wrappers throughout Phase 4.
- **Client rollback**: disable the "Trusted" toggle (render "All" as the
  only mode). All trust-related code paths become dead code until the
  toggle is re-enabled. No data migration, no cache flush.
- **Partial rollout**: if backend ships and client isn't ready, the
  backend change is silently idle — no existing query uses `authors`, so
  no behavioral change visible to users.

## Risk summary

| Risk | Likelihood | Impact | Mitigation |
|------|------------|--------|------------|
| Empty-authors semantic misread as "no filter" at any layer | Medium | High (full firehose leak) | Explicit tests, resolver log + metric on every short-circuit, alert threshold |
| Postgres planner ignores new index | Low | Medium (slow queries) | EXPLAIN assertion test in CI |
| SQLite 999-param ceiling hit despite cap | Low | Low (error, not corruption) | 500-DID cap + typed error |
| Method-variant explosion if future filter axes added | Medium | Medium (tech debt) | Unified RecordFilter struct shipped in same PR |
| Trust graph extremely sparse at launch → empty feed by default | High | Medium (bad first impression) | Default "Trusted" mode still works (empty state copy guides user to "All") |
| Client cache staleness on revocation | Medium | Low (5-min lag) | `refetchOnWindowFocus: true` + polling; WebSocket upgrade in v2 |
| Large trust sets (>500 DIDs) rejected | Low (v1) | Medium (feature breaks for that user) | Cap documented; v2 raises cap or adds server-side join |

## Open questions for the implementer

Both of these are answerable without blocking Phase 1 start. Flag if the
answer meaningfully changes the shape.

1. Does certs.social already have a `useLocalStorage` hook? If not, add
   a trivial one (useState + effect to persist) rather than pulling a
   library.
2. Is there an existing `useEvaluatorProfiles` / similar pattern for
   resolving DID → profile data? If so, reuse. If not, the evaluator
   picker will need a small new hook wrapping
   `appCertifiedActorProfile(authors: ...)`.
