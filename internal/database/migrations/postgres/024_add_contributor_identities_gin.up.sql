-- no-transaction
-- Partial GIN expression index on `record_contributor_identities(json)`,
-- the IMMUTABLE wrapper function created in migration 023. The index
-- serves the `@>` (eq) and `&&` (in) operators emitted by
-- `buildContributorFilter` against the activity collection, replacing
-- the previous O(collection-size) per-row EXISTS scan that the 5s
-- /graphql budget would otherwise time out on for rare contributors
-- (P-2 in the 2026-05-13 audit).
--
-- Partial — keyed only for `org.hypercerts.claim.activity` (the only
-- collection that reaches the KindArrayContributor filter today) and
-- only for rows whose `contributors` is a well-shaped array. Both
-- predicates keep the index tight; legacy mis-shaped rows are excluded
-- from the index entirely. The function itself returns NULL on
-- non-array contributors AND on arrays longer than 200 entries, so
-- the filter SQL no longer needs a separate type/size guard.
--
-- CONCURRENTLY = no exclusive lock on the table, but must run
-- outside a transaction. The migration runner detects
-- `-- no-transaction` and skips its BEGIN/COMMIT wrapper, but pgx
-- still wraps a multi-statement Exec body in an implicit transaction
-- (SQLSTATE 25001), so this stays one statement per file (lesson
-- from CI commit cb06896 on 021).
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_record_contributor_identities
    ON record USING gin (record_contributor_identities(json))
    WHERE collection = 'org.hypercerts.claim.activity'
      AND jsonb_typeof(json->'contributors') = 'array';
