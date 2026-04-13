DROP INDEX IF EXISTS idx_jetstream_activity_invalid;
-- SQLite does not support DROP COLUMN before 3.35.0; recreate if needed.
