DROP INDEX IF EXISTS idx_jetstream_activity_invalid;
ALTER TABLE jetstream_activity DROP COLUMN IF EXISTS is_valid;
