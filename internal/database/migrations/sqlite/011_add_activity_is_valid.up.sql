ALTER TABLE jetstream_activity ADD COLUMN is_valid BOOLEAN;
CREATE INDEX IF NOT EXISTS idx_jetstream_activity_invalid
    ON jetstream_activity (collection, timestamp)
    WHERE is_valid = 0;
