-- no-transaction
-- review-2026-05-17 Track 4: partial unique index on
-- jetstream_activity.source_event_id. Backs the ON CONFLICT DO
-- NOTHING dedup pattern in LogActivity that closes the Tap/
-- Jetstream redelivery window.
--
-- Partial — keyed only on non-NULL source_event_id so historical
-- rows (with NULL source_event_id) and any future consumer that
-- doesn't supply one stay valid and don't collide on each
-- other.
--
-- CONCURRENTLY = no exclusive lock on the table, but must run
-- outside a transaction. The migration runner detects
-- `-- no-transaction` and skips its BEGIN/COMMIT wrapper.
-- pgx still wraps a multi-statement Exec body in an implicit
-- transaction (SQLSTATE 25001), so this stays one statement per
-- file (lesson from CI commit cb06896 on migration 021).
CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS idx_jetstream_activity_source_event_id
    ON jetstream_activity (source_event_id)
    WHERE source_event_id IS NOT NULL;
