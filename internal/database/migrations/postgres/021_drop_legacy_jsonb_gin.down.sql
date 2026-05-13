-- no-transaction
-- Reverts the drop from 021's up. Note: this does NOT recreate the
-- index with the modern jsonb_path_ops operator class — that lives
-- in migration 022, which has its own down that drops it. If the
-- operator runs `down 022` then `down 021` they will be left with
-- the legacy jsonb_ops variant, matching the pre-migration state.
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_record_json_gin
    ON record USING GIN(json);
