-- no-transaction
-- Step 2 of 2: create the path_ops GIN variant under a distinct
-- name (so the duplicate-index-name guard in
-- migrations_indexnames_test.go stays green). See 021's up for the
-- rationale on why DROP + CREATE had to be split across two files.
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_record_json_gin_path_ops
    ON record USING gin (json jsonb_path_ops);
