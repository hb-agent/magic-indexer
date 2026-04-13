ALTER TABLE jetstream_activity DROP COLUMN is_valid;
DROP INDEX IF EXISTS idx_jetstream_activity_invalid;
