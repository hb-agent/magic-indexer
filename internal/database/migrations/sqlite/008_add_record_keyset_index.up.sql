-- Composite index for keyset pagination on record queries. Covers the
-- canonical query shape:
--
--   WHERE collection = ?
--   ORDER BY indexed_at DESC, uri DESC
--   LIMIT ?
--
-- Without this index, a collection with many records requires a filter
-- on `collection` followed by an in-memory sort of every matching row.
-- With it, the planner walks a single B-tree in the exact page order
-- and stops at LIMIT rows.
--
-- Adopted from hypercerts-org/hyperindex#34 by way of its upstream
-- migration 006_add_composite_index; we add it as 008 here to avoid
-- renumbering our own 006/007 label indexes.
CREATE INDEX IF NOT EXISTS idx_record_collection_keyset
  ON record(collection, indexed_at DESC, uri DESC);
