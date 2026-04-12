-- Composite index for keyset pagination on record queries that also
-- filter by author DID. See sqlite/010_add_record_collection_did_keyset_index.up.sql
-- for the full rationale.
--
-- On Postgres, without this index, `did IN (...)` against a large
-- collection falls back to a BitmapOr scan across the existing
-- idx_record_did_collection followed by a sort, which defeats the
-- keyset cursor's stop-at-LIMIT property and spikes memory use for
-- large IN-lists.
CREATE INDEX IF NOT EXISTS idx_record_collection_did_keyset
  ON record(collection, did, indexed_at DESC, uri DESC);

-- Refresh planner statistics so the new index is picked up immediately.
-- Without ANALYZE the planner may underestimate the selectivity of the
-- collection+did prefix until autovacuum catches up.
ANALYZE record;
