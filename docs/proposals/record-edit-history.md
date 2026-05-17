# Proposal — record edit history

**Status**: discovery, not committed work.
**Date**: 2026-05-13.
**Goal**: a complexity analysis of adding "view every prior version of a record" to Magic Indexer, with options ranked by cost.

---

## 1. What "record edit history" means concretely

In ATProto, a "record" is keyed by `at://<did>/<collection>/<rkey>` and content-addressed by its CID. When the author edits a record, the **URI stays the same** but the **CID changes** (new content hash) and the PDS publishes a `commit` event of operation `update`. The same is true for `create` (first CID) and `delete` (CID gone). Today Jetstream/Tap delivers all three to Magic Indexer's ingestion processor.

A history feature answers two query shapes:

1. **"Show me every version of this record"** — list of `(cid, json, indexed_at, operation)` keyed by URI, ordered by `indexed_at`.
2. **"What did this record look like at time T?"** — point-in-time lookup, returns the version that was current at T.

Sometimes operators also want:

3. **"Show me deletions"** — soft-tombstone preserved so a deleted record still appears in audit views.

Each shape has a different storage shape; (1) is the simplest, (2) needs a range index, (3) needs a tombstone column.

---

## 2. Current state — what's lost on update today

### Schema
```sql
CREATE TABLE record (
  uri TEXT PRIMARY KEY NOT NULL,    -- one row per record, keyed by URI
  cid TEXT NOT NULL,
  did TEXT NOT NULL,
  collection TEXT NOT NULL,
  json JSONB NOT NULL,
  indexed_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT NOW(),
  sort_at TIMESTAMP WITH TIME ZONE,           -- migration 017
  subject_did TEXT GENERATED ALWAYS AS (…),   -- migration 025
  -- ... plus a handful of indexes ...
);
```

Single row per URI. **Updates clobber the previous CID and JSON in place.** The relevant SQL is in `internal/database/repositories/records.go:152`:

```sql
INSERT INTO record (uri, cid, did, collection, json, sort_at)
VALUES (…)
ON CONFLICT(uri) DO UPDATE SET
    cid = EXCLUDED.cid,
    json = EXCLUDED.json,
    indexed_at = NOW(),
    sort_at = COALESCE(EXCLUDED.sort_at, record.sort_at)
WHERE record.cid IS DISTINCT FROM EXCLUDED.cid;
```

The `WHERE record.cid IS DISTINCT FROM EXCLUDED.cid` clause filters out same-CID re-inserts (so re-running ingest on a replay is idempotent) but real updates overwrite. There is no archive of the prior `(cid, json)`.

### What survives elsewhere

Two existing tables carry partial information:

- **`jetstream_activity`** (migration 001) — every Jetstream event records a row: `timestamp, operation, collection, did, status, error_message, event_json`. Rkey added in 005. `is_valid` added in 011.
  - **Retention**: 7 days (`internal/workers/activity_cleanup.go`). Operators can tune via env, but past 7 days the row is gone.
  - **Indexes**: timestamp DESC, rkey. Not indexed by `(did, collection, rkey)` for direct URI lookup.
  - **`event_json` content**: the full Jetstream message, which includes the prior CID and operation. Theoretically this is the source-of-truth for history, but only within retention and not joinable on URI without extra indexes.

- **GraphQL subscriptions** — `EventCreate`, `EventUpdate`, `EventDelete` are published live but not persisted (`internal/ingestion/processor.go:218–222`).

### Operation classification in ingestion

```go
switch op.Operation {
case OpCreate, OpUpdate:
    p.Records.InsertWithParams(...)  // upsert, history lost on update
case OpDelete:
    p.Records.Delete(...)            // hard delete, no tombstone
}
```

So **the system already knows** when an update is an update vs a create — `op.Operation == OpUpdate`. We're just not preserving the prior version.

### What's lost on delete

Today: hard `DELETE FROM record WHERE uri = $1`. Row gone, no tombstone. The `jetstream_activity` row survives for 7 days with `operation=delete`.

---

## 3. Approaches, ranked by cost

Five options, roughly increasing in complexity. Each option lists what changes, the storage cost, the write-path impact, and the query API surface.

### Option A — extend `jetstream_activity` retention + add an index

**Idea**: every Jetstream commit event already lands in `jetstream_activity` with the full `event_json` (which contains the prior CID and the new record body). Bump retention from 7 days to "long" and add an index on `(did, collection, rkey, timestamp DESC)` so URI-based history lookups are cheap.

**What changes:**
- One migration: add the new composite index.
- One config knob: `ACTIVITY_RETENTION_HOURS` (already exists at 24×7 = 168; bump default or make it operator-configurable).
- One GraphQL resolver: `recordHistory(uri: String!): [ActivityEvent!]!` that joins activity rows by URI.
- Optional: parse `event_json` server-side so the API returns structured `{cid, json, operation, ts}` rather than raw event blobs.

**Storage cost**: a 1-month retention at the current rate is ~30× the current 7-day retention. `event_json` is the full Jetstream message (~1–5 KB depending on collection); 30 days × ingest-rate is the steady-state.

**Pros**:
- Zero changes to the hot `record` write path.
- Schema cost is one index.
- Already-coded ingestion captures the data.

**Cons**:
- `event_json` shape is the Jetstream wire format, not the canonical record JSON — needs server-side translation for the API.
- Retention is a hard cap; activity older than the window is permanently gone.
- The activity table grows fast (every event, including creates, deletes, identity events) — a generalist log, not a record-history index.

**Effort estimate**: ~1 day. **Cheapest path; least powerful.**

### Option B — soft-delete column on `record`

**Idea**: keep one row per URI but add a `deleted_at TIMESTAMPTZ` column. On delete, set `deleted_at = NOW()` instead of `DELETE`. Updates still clobber.

**What changes:**
- One migration: `ALTER TABLE record ADD COLUMN deleted_at TIMESTAMPTZ`.
- `Records.Delete` becomes a soft-update.
- Every read path adds `WHERE deleted_at IS NULL` (or surfaces deleted-records via an option).
- GraphQL gains an `includeDeleted` argument on record queries.

**Storage cost**: one nullable timestamp per row. Effectively zero.

**Pros**:
- Recoverable deletes.
- Tombstones survive forever.

**Cons**:
- **Only solves the delete half of history.** Updates still clobber the prior CID + JSON. The "show me every version" query shape is not served.
- Every read path needs the filter — easy to forget on a future new query and silently surface deleted rows.

**Effort estimate**: ~0.5 day. **Useful but narrow.**

### Option C — `record_history` append-only sidecar (Recommended)

**Idea**: keep `record` as the current-state table (no schema change to it). Add a new `record_history` table that gets one append per change. Updates and deletes write a history row; the current-state row in `record` is also updated as today.

See § 3.C-detail below for the full mechanics — schema, exact write-path change, race semantics, query SQL, retention, and corner cases.

**Storage cost**: the `record` table size + one history row per change. For a record that gets edited 10× in its lifetime, that's 11 rows total. For a deleted record, 1 history row remains after the `record` row is gone.

**Effort estimate**: ~2 days. **Best fit for the stated goal.**

---

## 3.C-detail — Option C end-to-end

Most of what made Option C "feel like the right pick" was glossed over in the summary. This section is the real shape.

### 3.C.1 — Schema

```sql
CREATE TABLE record_history (
    -- BIGSERIAL because the URI×timestamp space has no useful natural key;
    -- two events for the same URI in the same millisecond are possible
    -- under replay, and a synthetic id is the cleanest way to order them.
    id BIGSERIAL PRIMARY KEY,

    -- The same five identity columns as `record` (uri/cid/did/collection
    -- are denormalised here so a history-only query never needs to join).
    uri TEXT NOT NULL,
    cid TEXT,                  -- NULL for delete tombstone; otherwise the CID this version produced
    did TEXT NOT NULL,
    collection TEXT NOT NULL,

    -- The operation that produced this history row.
    operation TEXT NOT NULL CHECK (operation IN ('create', 'update', 'delete')),

    -- Record body. NULL for delete (we keep the row as a tombstone but
    -- the content is gone — the prior version's row still carries the
    -- last-known body).
    json JSONB,

    -- sort_at (record's reported createdAt clamped) is carried over so
    -- history rows can be ordered by the same semantics the read path
    -- already uses. NULL is fine — same nullability rule as record.sort_at.
    sort_at TIMESTAMP WITH TIME ZONE,

    -- When we ingested this version. Server clock; this is what the
    -- "version at time T" query keys on, not sort_at.
    indexed_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT NOW()
);

-- Most important index: per-URI history walk.
CREATE INDEX idx_record_history_uri_indexed_at
    ON record_history (uri, indexed_at DESC);

-- Per-actor and per-collection feeds (mirror the existing record indexes
-- so the same access patterns work on history).
CREATE INDEX idx_record_history_did_indexed_at
    ON record_history (did, indexed_at DESC);
CREATE INDEX idx_record_history_collection_indexed_at
    ON record_history (collection, indexed_at DESC);

-- Optional later: a partial index for "tombstones only" if a delete-feed
-- becomes a hot query.
-- CREATE INDEX idx_record_history_deletes
--     ON record_history (uri, indexed_at DESC)
--     WHERE operation = 'delete';
```

Notes on the schema choices:

- **No FK from `record_history.uri` to `record.uri`.** A delete clears the `record` row but the history rows must survive. FK with `ON DELETE` of anything is wrong here.
- **No FK from `record_history.did` to `actor.did`.** Same reason — `purgeActor` clears the `actor` row but the history audit trail must outlive the actor row (subject to the retention / GDPR policy decision in § 5).
- **`cid` is nullable** so a delete tombstone can be stored as one row with `operation='delete', cid=NULL, json=NULL`. The previous version's row still carries the last-known content.
- **No `pds` column.** The current `record_table()` LEFT JOINs `actor` for `pds`; history rows do the same join at read time. (Or we denormalise `pds` here as the next thing if the join is hot.)
- **No `subject_did` generated column** (yet) — see § 5 open question 5. Probably wanted for symmetric query power; add later if the operator says yes.

### 3.C.2 — The exact write-path change

Where it goes: `internal/ingestion/processor.go`, inside the `switch op.Operation` block at line ~182.

```go
// Today's code (verbatim):
switch op.Operation {
case OpCreate, OpUpdate:
    if err := p.Actors.UpsertWithPDS(ctx, op.DID, "", pds); err != nil { ... }
    sortAt := ComputeSortAt(ExtractCreatedAt(op.Record), processedAt)
    result, err := p.Records.InsertWithParams(ctx, repositories.InsertParams{
        URI: op.URI, CID: op.CID, DID: op.DID, Collection: op.Collection,
        JSONData: string(op.Record), SortAt: &sortAt,
    })
    if err != nil { ... }
    if result == repositories.Inserted {
        metrics.RecordInserted(op.Collection)
    }
    // NEW — append history when the row actually changed.
    if result == repositories.Inserted {
        if err := p.Records.AppendHistory(ctx, repositories.RecordHistoryEntry{
            URI: op.URI, CID: op.CID, DID: op.DID, Collection: op.Collection,
            Operation: string(op.Operation), // "create" or "update"
            JSON:      string(op.Record),
            SortAt:    &sortAt,
        }); err != nil {
            // Log but don't fail the ingest — current-state was already
            // committed and is the contract. History is an audit
            // sidecar, not the source of truth.
            slog.Warn("record history append failed",
                "uri", op.URI, "operation", op.Operation, "err", err)
        }
    }
    p.PubSub.PublishRecord(...) // unchanged

case OpDelete:
    // NEW — write the tombstone FIRST so a crash between the two writes
    // leaves history slightly ahead of state (recoverable) rather than
    // the inverse (state cleared, no record of the delete).
    if err := p.Records.AppendHistory(ctx, repositories.RecordHistoryEntry{
        URI: op.URI, CID: "", DID: op.DID, Collection: op.Collection,
        Operation: "delete", JSON: "",
    }); err != nil {
        slog.Warn("record history (delete) append failed",
            "uri", op.URI, "err", err)
    }
    if err := p.Records.Delete(ctx, op.URI); err != nil { ... }
    p.PubSub.PublishRecord(subscription.EventDelete, ...)
}
```

Three things to notice:

1. **The `result == repositories.Inserted` gate is doing real work.** Today `InsertWithParams` returns `Inserted` only when `RowsAffected != 0`, which the `ON CONFLICT … WHERE record.cid IS DISTINCT FROM EXCLUDED.cid` clause turns into "the CID actually changed." A backfill replay that re-ingests the same record with the same CID returns `Skipped` and **we don't write a history row** — exactly the right behaviour. This is the part that "the existing same-CID-skip semantics already give us idempotency" was leaning on.

2. **The processor knows whether it's `create` or `update`** (`op.Operation`), even though the repository's `InsertWithParams` doesn't distinguish. So the operation string written to history comes from the processor's op, not from any derived state.

3. **Two writes, no transaction.** I am deliberately not putting the upsert + history-append inside a single `BeginTx`. Reasons:
   - The hot ingest path is sensitive to write latency; one round-trip is cheaper than two-in-a-tx.
   - The history table is an audit sidecar; if it falls behind by one row on a crash, the current-state contract is unaffected.
   - The processor already has crash-recovery via the Jetstream cursor — on restart, the un-acked event will re-ingest, and the second pass either (a) writes the history row this time (good) or (b) skips because the CID didn't change (also fine — history is at-least-once, idempotent).
   
   The cost of the two-writes-no-tx choice is documented in § 3.C.6.

### 3.C.3 — The three query SQL shapes

All driven by `(uri, indexed_at DESC)` or `(did, indexed_at DESC)` indexes — these are the only access patterns the schema is designed for.

**Q1 — "Show me every version of this record":**

```sql
SELECT id, cid, operation, json, sort_at, indexed_at
FROM record_history
WHERE uri = $1
ORDER BY indexed_at DESC, id DESC
LIMIT $2;
```

The secondary `id DESC` tie-breaker handles same-millisecond replays.

**Q2 — "What did this record look like at time T?":**

```sql
SELECT cid, operation, json, sort_at, indexed_at
FROM record_history
WHERE uri = $1
  AND indexed_at <= $2
  AND operation IN ('create', 'update')   -- skip tombstones
ORDER BY indexed_at DESC, id DESC
LIMIT 1;
```

If the result is empty, the record didn't exist at T. If `operation = 'delete'` for the most recent row before T, the record was deleted at T — return null with a `deletedAt` field in the GraphQL response.

**Q3 — "Recent edits for a collection":**

```sql
SELECT uri, cid, operation, sort_at, indexed_at
FROM record_history
WHERE collection = $1
  AND ($2::timestamptz IS NULL OR indexed_at <= $2)
ORDER BY indexed_at DESC, id DESC
LIMIT $3;
```

Used by a `collectionRecentEdits` resolver if exposed. Cheap on the partial index.

### 3.C.4 — GraphQL surface

Two new fields on the public schema:

```graphql
type RecordHistoryEntry {
  cid: String           # null for delete tombstones
  operation: String!    # "create" | "update" | "delete"
  json: JSON            # null for delete tombstones
  sortAt: DateTime
  indexedAt: DateTime!
}

type RecordHistoryConnection {
  edges: [RecordHistoryEdge!]!
  pageInfo: PageInfo!
}

extend type Query {
  recordHistory(uri: String!, first: Int = 50, after: String): RecordHistoryConnection!
  recordVersionAt(uri: String!, at: DateTime!): RecordHistoryEntry
}
```

Cursor encoding mirrors the existing keyset cursors in `internal/database/repositories/records.go:1042-1068`. Pagination cap = 200 like the rest of the read path.

### 3.C.5 — Retention worker

New worker mirroring `internal/workers/activity_cleanup.go`:

```go
// RecordHistoryCleanupWorker drops history rows older than retentionHours.
// Default retentionHours=0 means "keep forever"; operators opt into a
// finite retention via RECORD_HISTORY_RETENTION_HOURS.
type RecordHistoryCleanupWorker struct {
    history      *repositories.RecordHistoryRepository
    interval     time.Duration
    retentionHrs int
}
```

SQL: `DELETE FROM record_history WHERE indexed_at < NOW() - $1 * INTERVAL '1 hour'`. Batched (`LIMIT 10000`) to avoid long locks under a big sweep — same pattern as `JetstreamActivityRepository.CleanupOldActivity`.

### 3.C.6 — Race semantics and crash recovery (what the two-writes-no-tx costs us)

| Crash point | State after restart | Net effect |
|---|---|---|
| Before the upsert | Neither table touched. Jetstream cursor still at the un-acked event; replay re-ingests and writes both. | Correct. |
| Between upsert and history append (create/update path) | `record` has the new version; `record_history` does NOT. Jetstream cursor still at the un-acked event. Replay re-ingests; upsert sees `cid` unchanged → `Skipped` → **history row is NOT written**. | **Cost:** the first transition that crashes is lost from history. The current-state row is correct. The audit trail has a one-row gap. Frequency = crash rate × ingest rate × bad-luck-window (microseconds between two SQL calls). |
| Between history append and delete | `record_history` has the tombstone; `record` still has the row. Replay re-fires the delete; tombstone path is idempotent (re-inserts the same tombstone — see § 3.C.7); `record` row gets cleared. | Correct after replay; one duplicate tombstone row in history (harmless for forward queries, slightly noisy). |
| Between two records in a batch | Each record is processed independently. The cursor only advances on successful processor return. | Correct. |

The single failure mode is the create/update gap. **If that's unacceptable** — i.e. the audit trail has compliance value that exceeds the latency cost — we move both writes into one `BeginTx`. Two-line code change; we lose 0.1–0.5ms of ingest throughput per event. Make this decision explicitly in the plan.

### 3.C.7 — Corner cases I'd otherwise hand-wave

- **Initial state for already-indexed records.** History starts when the migration ships. A record edited five times before deployment shows up in `record_history` only on its sixth edit (or never if it's never edited again). **Mitigation option**: a one-time seed migration that inserts one history row per existing `record` row with `operation='initial', indexed_at=record.indexed_at`. Costs as much as the current `record` table; useful for "show history" never returning empty for existing records. Operator call.

- **Replay during backfill.** The PDS-backfill CLI (`cmd/backfill_pds`) re-ingests records that already exist. Same `cid IS DISTINCT FROM` skip applies — no spurious history rows.

- **Multiple identical deletes.** If the operator manually triggers a delete and Jetstream redelivers it, two `operation='delete'` rows land. Harmless but visible. The retention worker prunes both at the same age. If this matters, add a uniqueness guard: `CREATE UNIQUE INDEX … ON record_history (uri, operation, indexed_at) WHERE operation = 'delete'` — but a millisecond gap defeats this. Better fix: dedupe at query time (`SELECT DISTINCT ON (uri, operation)` for tombstones). Defer the decision until we see the data.

- **Same-millisecond timestamps under replay.** `id BIGSERIAL` is the tie-breaker; `(indexed_at DESC, id DESC)` ordering is total and deterministic across replicas (the BIGSERIAL is local to the writer, but with one indexer-replica we're fine; if we ever scale to multiple replicas writing this table, switch to a UUIDv7 or pgcrypto-driven id).

- **Generated columns.** Today's `record.subject_did` (migration 025, STORED, generated from `json`) does NOT exist on `record_history` in this proposal. If the operator wants `recordHistory` filterable by subject DID, add the same `STORED` generated column to `record_history`. Cost: same as 025 — table rewrite, schedule a maintenance window. Open question 5 in § 5.

- **Interaction with `purgeActor` and `resetAll`.** The Track 3 audit-driven mutations delete from `record` and `actor` (plus the hard-listed table set in `resetAll`). Should they ALSO clear history for the purged DID? GDPR-style takedowns argue yes (the whole point is "remove this person's data"). Audit-trail framing argues no (you want to know who was purged and when). **Strong recommendation**: when `purgeActor(did)` clears `record` and `actor`, it should also clear `record_history` for that DID, with the audit-log line itself recording the purge fact (count of history rows deleted). The audit trail of the *purge operation* survives in slog; the audit trail of the *purged DID's activity* does not — that's the GDPR contract.

### 3.C.8 — File ownership

| File | Change |
|---|---|
| `internal/database/migrations/postgres/0NN_add_record_history.up.sql` + `.down.sql` (new) | New table + three indexes |
| `internal/database/repositories/record_history.go` + `_test.go` (new) | `RecordHistoryRepository` — `AppendHistory`, `GetByURI`, `GetVersionAt`, `CleanupOlderThan`, `DeleteByDID` |
| `internal/database/repositories/records.go` | Add `AppendHistory` shim that calls the history repo, so the processor still sees one repository surface |
| `internal/ingestion/processor.go` | The two write-path changes from § 3.C.2 |
| `internal/workers/record_history_cleanup.go` (new) | Mirror of `activity_cleanup.go` |
| `internal/graphql/admin/purge.go` | Extend `PurgeActor` SQL to also delete from `record_history WHERE did=$1` |
| `internal/graphql/admin/resolvers.go` | Extend `ResetAll`'s hard-listed table set with `record_history` |
| `internal/graphql/schema/builder.go` + new resolvers | The `recordHistory` + `recordVersionAt` GraphQL fields |
| `internal/config/config.go` | `RECORD_HISTORY_RETENTION_HOURS` env var (default 0 = forever) |
| `cmd/hypergoat/main.go` | Start the new cleanup worker |
| `SECURITY.md` | New "Record history" section under "Admin surface" — retention contract, GDPR interaction with purge |
| `RUNBOOK.md` | One paragraph: what the new table is, how to tune retention, how to query it |
| `CHANGELOG.md` | One Unreleased entry |

### Option D — full bitemporal `record` with `valid_from`/`valid_to`

**Idea**: replace `uri PRIMARY KEY` with `(uri, valid_from)`. Every change appends a new row; the previous row gets `valid_to = NOW()`. Reading "current" filters `WHERE valid_to IS NULL`.

**What changes:**
- Schema: composite primary key, two new TIMESTAMPTZ columns.
- Every read query gains `WHERE valid_to IS NULL`.
- Every existing index needs a partial-index variant (`WHERE valid_to IS NULL`) to keep current-state queries fast.
- Every write does an UPDATE-then-INSERT inside a transaction (close-out previous row, insert new one).

**Storage cost**: same as Option C.

**Pros**:
- One table, no sidecar.
- Bitemporal semantics expressible directly in SQL.

**Cons**:
- **Every read path changes.** The existing filter / sort / cursor / index machinery (which is substantial — see `internal/database/repositories/filter.go` and the GraphQL schema builder) gets the `valid_to IS NULL` predicate added everywhere or risks returning historical rows.
- Existing indexes need partial-index siblings or they index history too.
- The migration is hostile to large existing tables (`ALTER TABLE record DROP CONSTRAINT … ADD CONSTRAINT` on the primary key with row movement).
- The generated column `subject_did` (just landed today in migration 025) needs to also flow into history rows correctly — needs careful expression.

**Effort estimate**: ~5–6 days. **Powerful but disruptive.**

### Option E — content-addressed CID table + version pointers

**Idea**: separate content (`record_content` keyed by CID) from current-state pointer (`record_current` keyed by URI → CID). History is `record_versions(uri, cid, indexed_at)`.

**Pros**:
- Deduplicates JSON across CIDs (if the same content is ever recreated, it's stored once).
- Matches the underlying ATProto model (records are content-addressed).

**Cons**:
- Three tables instead of one. Every read needs a CID→content join.
- CID is already in the `record` table; the dedup is mostly theoretical (same content with the same CID is rare across distinct URIs).
- Substantially more code change than Option C.

**Effort estimate**: ~4 days. **Probably overkill.**

---

## 4. Recommended: Option C (sidecar) with the obvious caveats

Reasoning:

1. **Read path is untouched.** All existing GraphQL filters, sort orders, cursors, and indexes continue to work on `record`. No risk of accidentally surfacing historical rows in a forgotten predicate.

2. **Write path is small and additive.** One new repository method (`AppendHistory`), one new table, one new index. The processor changes are ~10 lines.

3. **The existing same-CID-skip semantics already give us idempotency.** We only write a history row when `InsertWithParams` reports `Inserted` (real change), not on replay. This is built in.

4. **Deletes get tombstones for free.** A delete writes a history row with `json = NULL` before clearing the `record` row.

5. **The new GraphQL surface is small.** Two resolvers:
   - `recordHistory(uri: String!, first: Int, after: String): RecordHistoryConnection!`
   - `recordVersionAt(uri: String!, at: DateTime!): RecordHistoryEntry`

6. **Operator control**: a `RECORD_HISTORY_RETENTION_DAYS` env knob with default `0 = forever`. A cleanup worker (mirror `internal/workers/activity_cleanup.go`) prunes per retention.

### Complexity inventory

| Piece | Effort |
|---|---|
| Migration: `record_history` table + 3 indexes | 0.25 d |
| `Records.AppendHistory(ctx, entry)` + tests | 0.25 d |
| Processor wire-up (insert path + delete path) | 0.25 d |
| GraphQL: `RecordHistoryEntry` type, `recordHistory` connection, `recordVersionAt` resolver | 0.5 d |
| Retention worker + env var | 0.25 d |
| Operator-facing docs (`SECURITY.md` / `RUNBOOK.md` / CHANGELOG) | 0.25 d |
| Tests (Postgres-backed): create→update→update→delete sequence; point-in-time lookup; retention | 0.5 d |
| **Total** | **~2 days** |

### Storage estimate

Per-record overhead: ~1-5 KB per version (JSON body of the record, plus per-row tuple overhead). With **N edits per record over time**:

- 10k records × avg 3 edits = 30k history rows. At 2 KB avg = **60 MB**.
- 100k records × avg 3 edits = 300k history rows = **600 MB**.
- 1M records × avg 3 edits = **6 GB**.

The biggest cost driver is the ingest pattern of the collections actually being indexed. `app.bsky.feed.post` rarely gets edited; `org.hypercerts.claim.activity` might be edited heavily. Operator can tune retention per collection if needed (a later v2).

---

## 5. Open questions for the operator

1. **Goal scope.** Do we want (1) full history of every version, (2) just tombstones for deletes, or (3) both? Option C gives both; Option B gives only tombstones.

2. **Retention.** Forever, N days, N days per collection? Default? An immediate decision: forever is safest for legal/audit; N days needs an opinion.

3. **Backfill semantics.** History starts when the migration ships. Should we also seed history with the *current* state of every record (one entry per URI marked `operation=initial`)? Slightly more work but lets `recordHistory(uri)` return at least one row even for never-edited records.

4. **GraphQL exposure**. Should `recordHistory` be a public field or admin-only? Public reveals editing patterns; admin-only matches the audit-trail framing.

5. **`subject_did` and other generated columns.** The history table's `json` is the record body; do we replicate the generated `subject_did` column there for symmetric query power? If yes, the `STORED` generated-column cost from migration 025 doubles (~table-rewrite class operation). If no, the history table can't be filtered by subject DID.

6. **Per-collection opt-out.** Some collections (test data, throwaway lexicons) probably don't deserve history. Operator-controlled allow-list?

7. **Interaction with `purgeActor` / `resetAll`.** Today's destructive mutations (Track E + Track 3) delete from `record` and `actor`. Should they also clear `record_history` for the purged DID? The audit-trail framing argues yes, but the GDPR framing argues "yes definitely — that's the whole point of purge."

---

## 6. What I'd **not** do

- **Don't** add per-CID history to the `record` table itself (Option D). The existing read-path machinery is the project's hottest code and changing its semantics around `record` is high-risk relative to the size of the gain.

- **Don't** rely on `jetstream_activity` as the history store (Option A) unless the retention bump is explicitly the goal. The shape is wrong (wire-format, not canonical-record), the indexing is wrong (timestamp-first, not URI-first), and the implicit "history = activity log" coupling fights every future audit-log addition.

- **Don't** build Option E. The content-addressed model is theoretically clean but the dedup gain is small and the three-table join is permanently in the read path.

---

## 7. Recommended next step

**Write a plan doc** (`docs/proposals/record-edit-history/plan.md`) under the project's deep-flow process — capture larger goal, scope + file ownership, alternatives (this doc summarises them), acceptance criteria, retention defaults, GraphQL surface, and the seven open questions in § 5 above. Run plan review with reviewers in three lenses (DB / read-path-correctness; GraphQL surface ergonomics; ops / retention contract). Then implement on a feature branch with atomic commits per layer (migration → repo helper → processor wire-up → GraphQL → worker → docs).

Estimated end-to-end (plan + review + implement + impl-review + PR): **~3 working days**, of which **~2 days is implementation** as broken out in § 4 above.
