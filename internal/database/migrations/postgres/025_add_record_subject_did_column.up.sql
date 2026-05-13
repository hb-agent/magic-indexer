-- Materialize the BadgeAward `subject` DID into a STORED generated
-- column on `record` so the KindUnionSubject filter can be served by a
-- partial btree (migration 025) instead of a per-row LIKE pattern
-- against the JSONB extraction. Addresses P-3 in the 2026-05-13 audit:
-- the previous SQL parameterised the LIKE pattern, which the planner
-- cannot match against an expression index, so a rare DID on a busy
-- collection would trip the 5s /graphql budget.
--
-- The expression handles all three subject shapes the
-- KindUnionSubject filter recognises (see filter.go's
-- `buildBadgeAwardSubjectFilter` doc):
--
--   - Bare string `at://did:plc:.../...`         → split out the DID
--                                                  (defensive; no
--                                                  production records
--                                                  use this shape).
--   - strongRef object `{uri: "at://did:.../..."}` → split out the DID
--                                                    from the URI.
--   - defs#did object `{did: "did:plc:..."}`      → use directly.
--                                                   This matches the
--                                                   `app.certified.defs#did`
--                                                   lexicon shape
--                                                   (key `did`, not
--                                                   `identity`).
--
-- All functions in the expression (`jsonb_typeof`, `->`, `->>`,
-- `split_part`, `substring`, `CASE`, `COALESCE`) are IMMUTABLE in
-- Postgres, which is the precondition for using the expression in a
-- STORED generated column. The text `LIKE 'at://%'` predicate is also
-- IMMUTABLE since it has no locale dependency.
--
-- OPERATOR NOTE: ALTER TABLE … ADD COLUMN … GENERATED ALWAYS AS (…)
-- STORED rewrites the table on Postgres < 18. For the production
-- `record` table this takes a brief but real ACCESS EXCLUSIVE lock —
-- schedule a maintenance window for deployments with >10M rows.
-- Postgres 18+ allows VIRTUAL generated columns that don't rewrite;
-- we are not yet on 18, so STORED it is.
ALTER TABLE record ADD COLUMN subject_did TEXT GENERATED ALWAYS AS (
    CASE jsonb_typeof(json->'subject')
        WHEN 'string' THEN
            CASE WHEN json->>'subject' LIKE 'at://%' THEN
                split_part(substring(json->>'subject' from 6), '/', 1)
            ELSE NULL END
        WHEN 'object' THEN
            COALESCE(
                json->'subject'->>'did',
                CASE WHEN (json->'subject'->>'uri') LIKE 'at://%' THEN
                    split_part(substring(json->'subject'->>'uri' from 6), '/', 1)
                ELSE NULL END
            )
        ELSE NULL
    END
) STORED;
