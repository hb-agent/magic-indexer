# 02 — Performance Findings

Date: 2026-05-18 (overnight pass).
Scope: 10 lenses listed in the brief (SQL emission, ingestion path,
totalCount, N+1, index alignment, filter cap, depth/complexity,
hot-path logging, allocations, worker batching).
Calibration: high = measurable production impact (>10% on a load path or
known scaling cliff); medium = real waste, low daily impact; low =
micro-optimisation that's also a cleanup; nits omitted.

Cross-references to the correctness/security passes are noted where the
performance angle compounds an already-filed bug.

Per-finding format matches the brief: title, severity, location, problem,
realistic cost, proposed fix, effort, fix-risk, reversibility.

---

### P-1: `GetCollectionCount` ignores the request's filters — totalCount is wrong AND wastes a query
**Severity:** high
**Location:** `internal/graphql/schema/builder.go:763-770`, `internal/database/repositories/records.go:771-778`
**Problem:** The connection resolver's totalCount path calls
`repos.Records.GetCollectionCount(ctx, collection)` regardless of the
`where`, `authors`, `labels`, `excludeLabels`, `search`, or `excludePds`
arguments the client supplied. `GetCollectionCount` is hard-coded
`SELECT COUNT(*) FROM record WHERE collection = $1` — no filter
parameters, no filter SQL. So when a client issues a filtered page
request (e.g. `badgeAward(where: {subject: {eq: "did:plc:abc"}}, first: 20) { totalCount edges { ... } }`),
totalCount comes back as the unfiltered collection size while edges
returns the filtered page. The client believes the filtered result set
is two orders of magnitude bigger than it is. (This is both a
correctness defect and a performance defect — flagged here for the
performance angle.)

**Realistic cost:** Two-fold:
1. **Wasted work.** The COUNT(*) of an entire collection (`badge.award`
   already has millions of rows on the dev deployment) runs on every
   request that selects `totalCount` — even though the client wanted a
   tiny filtered slice. The unfiltered COUNT touches the full
   `idx_record_collection_keyset` btree, which on a multi-million-row
   collection costs hundreds of milliseconds and burns through the 5s
   per-request budget for no useful output.
2. **Misleading UI.** Clients render pagination controls / "X of Y
   results" against the wrong denominator. Combined with `first: 20`,
   they show "20 of 1.2M" when the true filtered count is 14.

**Proposed fix:** Promote totalCount to a filter-aware repo method
(`GetCollectionCountFiltered(ctx, collection, filter, filterGroup)`) that
applies the same WHERE clauses as `GetByCollectionFiltered`. Reuse the
existing filter-emission code path — move the WHERE-building helper
out of `GetByCollectionFiltered` into a private helper that both the
page query and the count query call. The count path skips the ORDER BY
+ LIMIT + cursor predicate. Same parameter binding rules; same per-DID
dedupe; same auth/PDS-exclude limits.
**Effort:** M (refactor the WHERE-clause builder, add a count
repository method, wire through the resolver).
**Risk of fix:** low (the count query is a strict subset of the page
query — if the page query is correct, the count is too).
**Reversibility:** easy (the change is additive — old `GetCollectionCount`
stays on the type, just unused from the connection resolver).

---

### P-2: Per-record `jetstream_activity` INSERT writes the full record JSON twice
**Severity:** high
**Location:** `internal/database/repositories/jetstream_activity.go:65-138`, called from `internal/ingestion/processor.go:135-151`
**Problem:** Every ingested record is written twice to Postgres on the
hot path: once to `record.json` (the canonical store) and once to
`jetstream_activity.event_json` (the audit log). Both are JSONB. The
activity table is retained for 7 days (`internal/workers/activity_cleanup.go:26`).
For a record with a 2-5 KB JSON body, this doubles every record
insert's storage write, doubles WAL volume on the ingestion path, and
doubles the JSONB parse cost (which is non-trivial in pgx 5).
Compounding with **C-1** (the activity cleanup worker never runs after
startup), the duplicate-storage cost is unbounded on the dev
deployment today — every record ever ingested is sitting in
`jetstream_activity` consuming disk and slowing the partial unique
index in migration 028.

**Realistic cost:** At even 100 records/sec (a quiet Jetstream
subscription), this is 200 INSERT statements/sec instead of 100, with
2x WAL bytes. On Railway's Postgres-18, that translates to a measurable
write-throughput ceiling: the ingestion path is bottlenecked on the
activity insert, not the record insert, because the activity row is
written FIRST (line 137 — the `pending`-then-`UpdateStatus` pattern).
At 500 records/sec (the kind of burst a single popular collection can
produce in the Bluesky firehose), the activity write IS the bottleneck.
Audit-log value: low — the data is duplicated from the `record` table
plus a small status field.

**Proposed fix:** Stop storing the full record body in
`jetstream_activity.event_json`. Either:
(a) keep an `event_meta` JSONB that captures only the operation /
collection / DID / rkey / status / error_message (the fields the
admin UI actually reads — see `GetRecentActivity` consumers); or
(b) drop `event_json` entirely on the create/update path and let
`record.json` serve as the source of truth for retrieval, joining on
URI when the admin UI needs a body.
The audit table becomes a thin operational ledger instead of a duplicate
record store. Migration writes `event_json` to NULL going forward;
existing rows continue to drain via the (fixed) cleanup worker.
**Effort:** M (migration to relax NOT NULL, edit `LogActivityWithStatus`,
audit admin UI consumers).
**Risk of fix:** med (one admin-UI query may grep through
`event_json` for diagnostics — verify against the admin
client before shipping). Coordinate with the C-1 fix.
**Reversibility:** hard (data thrown away by the producer cannot be
re-derived from the canonical record once the canonical record is
modified or deleted).

---

### P-3: Per-record `ProcessRecord` issues 4 DB round-trips even on the happy path
**Severity:** medium
**Location:** `internal/ingestion/processor.go:103-275`
**Problem:** Each Jetstream commit drives, in sequence:
1. `Activity.LogActivity` — INSERT into `jetstream_activity` (1 RT).
2. `Actors.UpsertWithPDS` — INSERT … ON CONFLICT into `actor` (1 RT).
3. `Records.InsertWithParams` — INSERT … ON CONFLICT into `record` (1 RT).
4. `Activity.UpdateStatus` — UPDATE `jetstream_activity` (1 RT).
That's 4 round-trips per record, all sequential, all on the same
connection pool. The first and fourth are book-keeping (status
'pending' → 'success'), so even after P-2 trims the JSON they are
still two writes for one logical operation.

**Realistic cost:** With Railway's pooler measured ~3-5ms per simple
INSERT round-trip, the ingestion path needs ~15-20ms wall per record
before doing anything useful. Throughput ceiling per consumer
goroutine is ~50-70 records/sec on a single-threaded firehose. The
Jetstream consumer is single-threaded (one event at a time —
`processEvents` at `internal/jetstream/consumer.go:285-350`), so this
ceiling is the per-collection ingestion ceiling. For a collection
under sustained ~100 events/sec, the consumer falls behind cursor and
the gap grows.

**Proposed fix:** Collapse the activity-status flow into a single
INSERT … RETURNING that writes the FINAL status (after the record
write), not 'pending' then UPDATE. Today the LogActivity-then-
UpdateStatus split exists because the record insert can fail
in the middle — but with the (independent) record validation done
*before* the activity insert, the only thing between activity insert
and UpdateStatus that can fail is the record INSERT itself, and the
existing C-1 orphan janitor was the safety net. Restructure:
- compute validation result first;
- compute sortAt first;
- attempt `record.Insert`;
- ONLY THEN write `jetstream_activity` with the final status.
This drops a round-trip per record (~25% throughput improvement on the
ingestion path) and removes the orphan window that C-16 calls out.
**Effort:** M (reorder the processor, update the orphan-janitor
guarantees in the doc, update tests).
**Risk of fix:** med (re-ordering means a crash between record insert
and activity insert leaves the record with no audit trail — but that's
strictly safer than today's "pending audit, no record" failure mode).
**Reversibility:** easy (re-order back if needed).

---

### P-4: Per-record `Actors.UpsertWithPDS` issues an INSERT even when the actor row already exists and the handle/PDS haven't changed
**Severity:** medium
**Location:** `internal/ingestion/processor.go:201`, `internal/database/repositories/actors.go:47-59`
**Problem:** Every ingested record triggers an `INSERT … ON CONFLICT
DO UPDATE` against `actor` regardless of whether anything has changed.
A single popular DID can produce hundreds of records per minute; each
one bumps `indexed_at = NOW()` even when handle and pds are
unchanged. The DB does the work; we never read `actor.indexed_at` on
the hot path.

**Realistic cost:** ~1 DB round-trip per record purely for "I just
saw this DID again," plus the row-level lock on the actor row. For a
ten-DID feed, two consumers ingesting at 50 records/sec each is
~100 actor upserts/sec on the same handful of rows — Postgres handles
this fine, but it's pure-waste write traffic competing with reads on
the same actor table. Also bloats WAL.

**Proposed fix:** Cache last-seen DID timestamps in the
RecordProcessor (bounded LRU, ~10k entries). When a DID was upserted
within the last ~60s, skip the upsert entirely. Cache the resolved
PDS too so a fresh DID misses the cache, resolves PDS, upserts, then
shadows further upserts for the cooldown window. Trade-off: handle
changes can be invisible for up to 60s — acceptable because handles
are already cached by other layers and rarely change.
**Effort:** S (one bounded LRU in the processor).
**Risk of fix:** low (skip on cache hit is strictly weaker than the
current behaviour; cache miss path is unchanged).
**Reversibility:** easy (delete the cache, restore unconditional upsert).

---

### P-5: `awardCount` derived field is N+1 by design — verified the cost claim and the description overstates "sub-millisecond"
**Severity:** medium
**Location:** `internal/graphql/schema/derived_fields.go:62-84`, `internal/database/repositories/records.go:780-809`
**Problem:** The `awardCount` resolver issues one `SELECT COUNT(*)`
per row in the parent badge.definition connection page. The pinned
description (`awardCountDescription`) claims "per-row cost is
sub-millisecond" because the query hits a partial expression index.
Two issues:
1. **N+1 is real.** A `first: 50` page on `appCertifiedBadgeDefinition
   { awardCount }` issues 51 SQL statements (1 page + 50 counts), all
   sequential because graphql-go has no DataLoader-equivalent and the
   indexer doesn't ship its own.
2. **"Sub-millisecond" is conditional.** It's true for definitions
   with low award counts (the planner does an index range scan and
   stops after counting), but a popular definition with 100k+ awards
   pays the full index-only scan — ~5-20ms on the dev deployment.
   At `first: 50` on a page of popular definitions, that's ~250-1000ms
   added wall time to the query.

**Realistic cost:** For a page of 20 popular definitions, ~100-400ms
added latency from awardCount alone. For a page of 50 mixed-popularity
definitions, ~500ms added. Acceptable for the certified-app's Lists
view because that view is paginated and clients don't refetch on every
keystroke — but the doc claim is misleading for future consumers
sizing whether to opt in.

**Proposed fix:** Two tracks.
(a) **Doc fix (must-do).** Replace "sub-millisecond" with "tens of
milliseconds per high-count definition (low-count definitions are
sub-millisecond)" so consumers don't assume it's free.
(b) **Aggregation fix (optional).** Add a batch-count repo method
`CountAwardsByBadgeURIs(ctx, uris []string) map[string]int64` that
runs a single `SELECT badge_uri, COUNT(*) FROM record WHERE collection
= 'award' AND json->'badge'->>'uri' IN (...) GROUP BY badge_uri`. The
resolver collects URIs across the parent page first, then resolves
each row from the prefetched map. Cuts 51 statements → 2. graphql-go
doesn't expose the right hook for true DataLoader, but the connection
resolver CAN prefetch counts for the page before assembling edges
(synchronously, since all edges share `repos`).
**Effort:** S (doc) / M (batch).
**Risk of fix:** low (doc) / low (batch — additive method).
**Reversibility:** easy.

---

### P-6: Repeated full-record `string([]byte)` allocations per ingested record
**Severity:** medium
**Location:** `internal/ingestion/processor.go:120, 144, 165, 216`
**Problem:** Each ingested record triggers:
- `string(op.Record)` at line 120 (inside `strings.TrimSpace(string(...))`) — full copy.
- `string(op.Record)` at line 144 (LogActivity) — full copy.
- `string(op.Record)` at line 216 (InsertWithParams.JSONData) — full copy.
- `op.Record` passed to `Validator.Validate` which does
  `json.Unmarshal(op.Record, &data)` (parse + allocate the map tree).
- `ExtractCreatedAt(op.Record)` (another `json.Unmarshal` into a
  one-field envelope).
That's 3 full record-bytes copies plus 2 JSON parses per record.

**Realistic cost:** For a 5 KB record, ~15 KB of throwaway string
allocations + 2 full JSON parses per record. At 500 records/sec, ~7.5
MB/s of GC pressure + ~1000 JSON parses/sec just for the duplicate
walks. Measurable in `pprof heap` and `pprof alloc_objects` profiles.

**Proposed fix:** Three small wins:
(a) Replace `strings.TrimSpace(string(op.Record))` with
`bytes.TrimSpace(op.Record)` and trim/inspect-byte-prefix without
allocating (the only later use of the trimmed value is to test the
first byte and log a truncation — both work on `[]byte`).
(b) Have `InsertWithParams` accept `JSONData []byte` and have the
`database.Text` constructor accept `[]byte` directly. pgx already
copies the parameter into its wire buffer; the intermediate Go string
is pure waste.
(c) Have `Validator.Validate` cache the parsed map so
`ExtractCreatedAt` can reuse the same parse — or, simpler, have
`ExtractCreatedAt` scan for `"createdAt"` using
`bytes.Index` and parse only the value, skipping the full
unmarshal. The createdAt field is always top-level so a `bytes.Index`
match is reliable enough (with a length cap to handle pathological
inputs).
**Effort:** S each.
**Risk of fix:** low.
**Reversibility:** easy.

---

### P-7: `extractInValues` allocates a fresh `[]string` per filter, including for `[]string` inputs that could be reused
**Severity:** low
**Location:** `internal/database/repositories/filter.go:1067-1084`
**Problem:** When the IN value is a `[]interface{}` (the common
GraphQL parse output), the function `fmt.Sprintf("%v", item)` per
element — this allocates twice per element (a reflection step and a
string conversion). The result slice itself is grown without an
initial capacity hint. Per-query cost is small (typically ≤50
elements per IN list), but the function is on the hot SQL-emission
path for every filter query that uses `in`/`ini`.

**Realistic cost:** Small but cumulative — for a query with 5 IN
filters of 50 elements each, ~500 reflection+allocation steps. Not a
ceiling-mover, but a clean win for free.

**Proposed fix:** Pre-allocate the result slice with `len(list)`
capacity. For string conversion, use a type switch that fast-paths
`string`, `int`/`int64`, and `float64` (the only types the GraphQL
parser produces) before falling back to `fmt.Sprintf`. The fast paths
avoid the reflection overhead.
**Effort:** S.
**Risk of fix:** low.
**Reversibility:** easy.

---

### P-8: `slog.Info` per filter-short-circuit fires on every empty-authors query
**Severity:** low
**Location:** `internal/database/repositories/records.go:459-463`
**Problem:** When a client sends `authors: []` (empty list), the
short-circuit logs at Info level. A buggy or probing client can send
this every request; the log volume scales linearly with request rate.
The metric `RecordAuthorsFilterEmptyBlocked` is already incremented
(at `internal/graphql/schema/builder.go:657`), so the log adds nothing
the metric doesn't.

**Realistic cost:** Cheap individually, but Info-level logs on a
per-request path go to a centralised log sink and rate-quota the
sink. With the metric already covering the case, the log is
duplicative.

**Proposed fix:** Demote to Debug, or drop entirely (the metric is
the operator-facing signal).
**Effort:** S.
**Risk of fix:** low.
**Reversibility:** easy.

---

### P-9: Labeler `upsertLabels` issues one INSERT per label even when frames contain many
**Severity:** low
**Location:** `internal/labeler/consumer.go:539-610`, `internal/database/repositories/labels.go:71-180`
**Problem:** The labeler consumer iterates each label in a frame and
calls `labels.Insert` (or `InsertNegation`) per label. `ensureDefinition`
is cached, so it's a no-op most of the time, but the label insert
itself is one round-trip per label. Labeler frames are usually small
(1-10 labels) but Bluesky's bulk-labeling tools can emit 100+ labels
per frame.

**Realistic cost:** For typical 1-5 labels/frame, this is invisible
(<10ms added per frame). For backfill from a large labeler
(`internal/labeler/backfill.go`), bulk frames of 100+ labels each
become 100+ sequential round-trips per frame — backfill on a busy
labeler with 1M labels would do 1M round-trips at ~5ms each = ~83
minutes minimum just on insert latency. Today this is rare; if the
labeler ecosystem grows it becomes load-bearing.

**Proposed fix:** Add `Labels.BatchInsert(ctx, []LabelToInsert)` using
the same multi-VALUES pattern as `ActorsRepository.batchUpsertChunk`.
Have `upsertLabels` collect non-negation labels and submit them as
a single batch; negations stay individual (different SQL shape via
`InsertNegation`, but they're rare in normal labeling flows).
**Effort:** M.
**Risk of fix:** low (additive method).
**Reversibility:** easy.

---

### P-10: GraphQL surface has depth guard but no field-count / alias-multiplier guard
**Severity:** low
**Location:** `internal/graphql/handler.go:142-152`, `internal/graphql/depth/depth.go`
**Problem:** The depth guard caps nesting at 15, which is fine. But a
query like
```graphql
{
  a1: appCertifiedBadgeDefinition(first: 50) { edges { node { awardCount } } }
  a2: appCertifiedBadgeDefinition(first: 50) { edges { node { awardCount } } }
  ... 100 aliases ...
}
```
has depth=4 (well under the cap) but triggers `100 page queries + 100
× 50 awardCount counts = 5100 SQL statements` in a single request.
The 5s per-request timeout will catch the slowest variants, but it
won't catch the cumulative ~3s ones, and meanwhile the request holds
multiple DB connections + memory. The body cap (1 MiB) prevents
truly absurd cases but still allows ~500 aliases of a 1KB query
fragment.

**Realistic cost:** A single un-authenticated request can occupy
multiple seconds of CPU and DB time. Rate-limiting upstream may catch
it; the indexer has no built-in defence beyond depth+body-cap+timeout.

**Proposed fix:** Add a sibling `complexity.Check` pre-execution pass
that walks the AST counting field selections (including alias
multiplication) against a cap (~500 selections). Implementation
mirrors `depth.Check` — single AST walk, returns error before
`graphql.Do`. Tune the cap to comfortably fit realistic client
queries (the certified-app frontend's largest query is ~30
selections).
**Effort:** M (write the new package; matches depth package's shape).
**Risk of fix:** low (additive guard; if the cap is too tight,
loosen).
**Reversibility:** easy.

---

### P-11: Lens "filter cap third hole" — `Children` recursion IS counted; no third hole exists
**No finding.** `FilterGroup.CountConditions` at
`internal/database/repositories/filter.go:294-306` walks `Filters`,
`Children`, `Joined.Inner`, and `Arrays.Inner` recursively. The cap
(`MaxFilterConditions = 20`) is enforced against the recursive total
at `BuildFilterGroupClause:325-329`. I traced `extractFieldFiltersRecursive`
(`internal/graphql/schema/where.go:378`) — every code path that
appends to `Joined`, `Arrays`, or `Children` flows through the same
`buildFilterGroupRecursive` that uses the recursive count. No bypass.

---

### P-12: Lens "index alignment with runtime SQL" — KindUnionSubject and the keyset / search indexes are aligned
**No finding.** Verified:
- **KindUnionSubject** (migration 026) — the runtime SQL emits
  `r.subject_did = $1` / `= ANY($1::text[])`. `subject_did` is a real
  column (STORED generated, migration 025) and the partial btree
  (migration 026) is on the column directly, not on an expression.
  Column references match indexes byte-for-byte trivially; no drift
  test needed.
- **Search vector** (migration 012) — the runtime SQL emits
  `r.search_vector @@ plainto_tsquery('english', $N)`. `search_vector`
  is a STORED generated column (also migration 012) indexed by GIN.
  Same trivial column-reference match.
- **Keyset indexes** (migrations 008 + 010) — runtime SQL emits
  `r.collection = $1 AND (r.indexed_at < $2 OR (r.indexed_at = $3 AND
  r.uri < $4))` plus `ORDER BY r.indexed_at DESC, r.uri DESC LIMIT N`.
  Index 008 is `(collection, indexed_at DESC, uri DESC)`. Index 010 is
  `(collection, did, indexed_at DESC, uri DESC)`. Both are column
  references; the planner picks them based on column equality, not
  expression equality.
The byte-equality drift tests are only needed where the SQL emits a
JSON-path expression that must exactly match the partial-index
expression (KindArrayContributor, KindStringSubject, KindUnionSubject's
backfill, CountAwardsByBadgeURI). The lens's hypothesis "the planner
could silently miss the index" doesn't apply to column references.

---

### P-13: Lens "logging on hot paths" — beyond P-8, the ingestion path is clean
**No finding.** Audited every `slog.Info` call in
`internal/jetstream/`, `internal/labeler/`, `internal/tap/`,
`internal/ingestion/`, `internal/notifications/`, `internal/backfill/`,
and `internal/database/repositories/`. Every entry is one of:
- startup / shutdown / reconnect (one-shot, fine);
- backfill progress (one per batch of 100s of records, fine);
- explicit operator-visible event ("Updating Jetstream collections",
  "Resuming from cursor" — one-shot per consumer lifecycle).
The ingestion processor's per-record paths (`slog.Debug "Stored
record"`, `"Deleted record"`) are correctly at Debug — at production
log level they don't run. The only Info-level per-request log is the
empty-authors one flagged in P-8.

---

### P-14: Lens "regexp.MustCompile inside hot-path functions" — clean
**No finding.** Two `regexp.MustCompile` calls in non-test code,
both at package scope:
- `internal/database/repositories/filter.go:478` —
  `fieldSegmentRegex` (used in `ValidateFieldName`).
- `internal/database/postgres/executor.go:48` —
  `statementTimeoutRegex` (used at startup only).
No per-call recompilation. The `extractFieldFilters` recursion uses
`ValidateFieldName` (which uses the package-level regex) — but
`ValidateFieldName` is called per-filter, not per-character, and the
regex is shared.

---

### P-15: Lens "worker / consumer batching" — Tap and Jetstream cannot batch by protocol; Notifications cannot batch by use-case
**No finding worth filing.** The lens prompts "is there a batching
opportunity that was missed."
- **Jetstream consumer**: events arrive via websocket, one at a time.
  Batching would require a buffer-and-flush window that introduces
  latency (clients of the indexer expect freshly-ingested records to
  appear in queries within hundreds of ms). The right batching unit is
  "events between cursor advances," and the cursor advances per
  event today. A buffer-and-flush optimisation is real, but it's a
  larger architectural change with a latency tradeoff — out of scope
  for "small change, big win." Note for future work.
- **Tap consumer**: each event MUST be ACK'd individually over the
  websocket (protocol contract). One handler per event is the
  protocol's expected shape.
- **Notifications dispatch**: there is no outbound dispatch.
  Notifications are persisted to the local `notification` table; the
  certified-app polls via the public `/notifications/graphql` XRPC
  bridge. The per-envelope INSERT cost flagged in **P-3** covers the
  ingestion-side work.

---

## Notes on what's deliberately omitted

- **`buildFilterGroupRecursive` allocation profile.** The function uses
  `append` on `clauses` / `params` without pre-allocating capacity.
  At realistic filter sizes (≤20 conditions), this is 2-3 reallocations
  per call — invisible cost compared to the SQL round-trip itself.
  Bias-toward-concrete: not worth flagging.
- **`buildSingleFilter` fmt.Sprintf calls.** Each filter emits 1-3
  `fmt.Sprintf("$%d", paramIdx)` calls plus one for the clause
  template. ~5 Sprintfs per filter, ~100 per query at the cap. At
  ~200ns per Sprintf, ~20μs per query — well under the network
  round-trip baseline. A `strconv.Itoa` + manual concatenation could
  shave this, but the saving is meaningless against a query that
  spends 10ms in Postgres.
- **`json.Marshal(containment)` in `buildSingleFilter`** for OpEq on
  JSON. One allocation per `eq` filter on a JSON field. Per-query cost
  is fixed by the filter cap (≤20). Not a hot loop.
- **`encodeCursorV2` per edge.** Allocates a small JSON array + base64
  encoding per edge in the response. At `first: 100`, 100 cursor
  allocations. Real but small; the dominating cost is the JSON
  serialisation of the edge data itself.
