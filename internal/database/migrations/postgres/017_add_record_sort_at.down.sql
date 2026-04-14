-- no-transaction
DROP INDEX CONCURRENTLY IF EXISTS idx_record_collection_sort_at_uri;
ALTER TABLE record DROP COLUMN IF EXISTS sort_at;
