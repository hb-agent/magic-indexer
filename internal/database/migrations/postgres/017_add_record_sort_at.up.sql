-- no-transaction
-- Deploy 1 of the 2-step rollout for issue #26 (Bluesky-style sortAt ordering).
--
-- sort_at is a clock-skew-clamped timestamp used for deterministic feed
-- ordering. It is nullable in this migration so the column can be added
-- without touching existing rows; the processor writes a value on insert
-- going forward. Deploy 2 (a later migration) will backfill and flip the
-- column to NOT NULL once every row has a value.
--
-- CREATE INDEX CONCURRENTLY cannot run inside a transaction block, so this
-- file carries the "-- no-transaction" sentinel; the migration runner
-- executes it without wrapping in BEGIN/COMMIT.
ALTER TABLE record ADD COLUMN IF NOT EXISTS sort_at TIMESTAMP WITH TIME ZONE;

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_record_collection_sort_at_uri
  ON record(collection, sort_at DESC NULLS LAST, uri DESC);
