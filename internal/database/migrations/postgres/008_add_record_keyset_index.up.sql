-- Composite index for keyset pagination on record queries.
-- See sqlite/008_add_record_keyset_index.up.sql for rationale.
CREATE INDEX IF NOT EXISTS idx_record_collection_keyset
  ON record(collection, indexed_at DESC, uri DESC);
