-- no-transaction
-- HISTORICAL — neutralised. See 013_*.up.sql.
-- The original down dropped `idx_record_json_gin` (which 001 created),
-- permanently degrading rollback environments. That dependency now lives
-- in migration 021's down — this file is a no-op.
SELECT 1;
