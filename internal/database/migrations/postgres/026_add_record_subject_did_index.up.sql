-- no-transaction
-- Partial btree on the `subject_did` generated column (materialized
-- by migration 024). Scoped to the badge.award collection where the
-- KindUnionSubject filter is reachable so the index stays tight on
-- collections that don't use the subject filter today.
--
-- Pairs with the filter SQL rewrite in
-- internal/database/repositories/filter.go that replaces the per-row
-- `LIKE 'at://' || $1 || '/%'` (un-indexable, parameter-driven LIKE
-- pattern) with `r.subject_did = $1` / `r.subject_did = ANY($1::text[])`
-- which the planner picks up via this index.
--
-- CONCURRENTLY so production deploys don't block writers; one
-- statement per file because pgx wraps multi-statement Exec bodies in
-- an implicit transaction that Postgres rejects for CONCURRENTLY
-- operations (SQLSTATE 25001 — lesson from CI failure on 021,
-- commit cb06896).
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_record_subject_did
    ON record (subject_did)
    WHERE collection = 'app.certified.badge.award' AND subject_did IS NOT NULL;
