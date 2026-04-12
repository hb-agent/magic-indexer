-- Composite index for keyset pagination on record queries that also
-- filter by author DID. Covers the canonical query shape:
--
--   WHERE collection = ? AND did IN (...)
--   ORDER BY indexed_at DESC, uri DESC
--   LIMIT ?
--
-- Without this index, a query with an IN-list on did requires either a
-- BitmapOr across idx_record_did_collection followed by an in-memory
-- sort, or picking one single-column index and then sorting. Both lose
-- the keyset cursor's stop-at-LIMIT property and scale poorly as the
-- trust-set size grows.
--
-- With this index, the planner walks a single B-tree in
-- (collection, did, indexed_at DESC, uri DESC) order and stops at
-- LIMIT rows regardless of how many DIDs are in the IN-list.
--
-- See docs/architecture/0001-trusted-evaluator-feed-filter.md for the
-- feature that motivated this index.
CREATE INDEX IF NOT EXISTS idx_record_collection_did_keyset
  ON record(collection, did, indexed_at DESC, uri DESC);

-- Refresh planner stats so the new index is picked up immediately.
ANALYZE;
