-- IMMUTABLE wrapper function that materialises the per-row
-- contributor identities array used by the KindArrayContributor
-- filter shape in internal/database/repositories/filter.go. The
-- partial GIN index on `record_contributor_identities(json)` lives
-- in migration 024.
--
-- WHY A FUNCTION? Postgres forbids subqueries in index expressions
-- (`cannot use subquery in index expression`, SQLSTATE 0A000), so
-- we can't put the `ARRAY(SELECT ... FROM jsonb_array_elements(...))`
-- expression directly into a CREATE INDEX. Wrapping the subquery in
-- an IMMUTABLE SQL function and indexing the function call is the
-- documented workaround. The function MUST stay IMMUTABLE — the
-- index would be corrupt if the function ever changed its return
-- for the same input. PARALLEL SAFE matches what the planner needs
-- to choose parallel scans against this index.
--
-- This file is split from the CONCURRENTLY index in 024 because:
--   1. pgx wraps multi-statement Exec bodies in an implicit
--      transaction (SQLSTATE 25001 for CONCURRENTLY operations —
--      lesson from CI commit cb06896 on 021).
--   2. CREATE FUNCTION runs fine inside a transaction; CREATE INDEX
--      CONCURRENTLY does not. Splitting lets each migration use the
--      best execution mode.
--
-- The function body MUST match `buildContributorFilter`'s inline
-- ARRAY-subquery in filter.go byte-for-byte (modulo the `rec_json`
-- parameter name vs. the `r.json` column reference). If they ever
-- diverge, the planner stops using the index and the filter
-- silently degrades to O(collection-size) per query.
CREATE OR REPLACE FUNCTION record_contributor_identities(rec_json jsonb)
RETURNS text[]
LANGUAGE sql
IMMUTABLE
PARALLEL SAFE
AS $$
    -- Returns NULL on non-array contributors AND on arrays longer
    -- than 200 entries (mirrors notifications.MaxContributorsBeforeReject
    -- so a record the notifications layer rejects also fails to match
    -- the filter). Callers don't need a separate type-or-size guard —
    -- `NULL @> ARRAY[...]` is NULL, treated as FALSE in WHERE.
    --
    -- The 200 cap MUST match repositories.MaxArrayContributorScan
    -- in filter.go. The function is IMMUTABLE so the value is
    -- baked into the index; changing the cap requires
    -- REINDEX CONCURRENTLY idx_record_contributor_identities.
    SELECT CASE
        WHEN jsonb_typeof(rec_json->'contributors') = 'array'
         AND jsonb_array_length(rec_json->'contributors') <= 200 THEN
            ARRAY(
                SELECT CASE jsonb_typeof(c->'contributorIdentity')
                    WHEN 'string' THEN c->>'contributorIdentity'
                    WHEN 'object' THEN c->'contributorIdentity'->>'identity'
                END
                FROM jsonb_array_elements(rec_json->'contributors') c
                WHERE c->'contributorIdentity' IS NOT NULL
            )
        ELSE NULL
    END;
$$;
