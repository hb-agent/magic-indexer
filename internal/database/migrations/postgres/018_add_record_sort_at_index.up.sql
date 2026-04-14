-- no-transaction
-- Companion index for migration 017 (issue #26). Split into its own file
-- because CREATE INDEX CONCURRENTLY cannot run inside a transaction block
-- and pgx wraps multi-statement ExecContext calls in an implicit one. The
-- "-- no-transaction" sentinel tells the migration runner to exec this
-- file without BEGIN/COMMIT.
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_record_collection_sort_at_uri
  ON record(collection, sort_at DESC NULLS LAST, uri DESC);
