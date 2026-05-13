-- no-transaction
-- Replaces the legacy idx_record_json_gin (created in migration 001 with the
-- default jsonb_ops operator class) with idx_record_json_gin_path_ops (using
-- jsonb_path_ops).
--
-- Why:
--   * Migration 013 tried to switch operator classes but reused the same index
--     name with `CREATE INDEX … IF NOT EXISTS`, so it was a silent no-op on
--     every environment that ran 001 first. Today's prod GIN is therefore the
--     larger, slower jsonb_ops variant; the smaller / faster jsonb_path_ops
--     variant the codebase claims is in use does not actually exist.
--   * Worse, 013's down migration drops the 001-created index — a rollback
--     would degrade query performance permanently.
--
-- Recovery semantics:
--   * Up:   drop the legacy index, create the path_ops variant under a new
--           unique name. New filters added today (BadgeAward subject, the
--           contributor EXISTS subquery) bypass this GIN anyway; the index
--           is paying its insert cost for residual `@>` containment workloads.
--   * Down: drop the new index. Does NOT recreate the legacy — its only role
--           in production is to support residual containment lookups that we
--           are explicitly retaining only via the path_ops variant going
--           forward. If you need the legacy back, run 001's CREATE statement
--           manually.
--
-- CONCURRENTLY is required for both the DROP and the CREATE so we don't lock
-- writers; the `-- no-transaction` sentinel tells the migration runner to
-- execute this file outside a BEGIN/COMMIT.

DROP INDEX CONCURRENTLY IF EXISTS idx_record_json_gin;

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_record_json_gin_path_ops
    ON record USING gin (json jsonb_path_ops);
