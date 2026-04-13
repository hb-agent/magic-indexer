-- no-transaction
-- This migration must run outside a transaction because CREATE INDEX CONCURRENTLY
-- cannot execute inside a transaction block. The migration runner must detect the
-- "-- no-transaction" sentinel and execute this file without wrapping it in BEGIN/COMMIT.
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_record_json_gin ON record USING gin (json jsonb_path_ops);
