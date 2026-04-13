-- Full-text search on record JSON content.
--
-- Creates an immutable wrapper for to_tsvector (which is STABLE, not
-- IMMUTABLE) so it can be used in a GENERATED ALWAYS AS expression.
-- This is a well-known Postgres pattern; the "lie" about immutability
-- is safe because the 'english' text search configuration is fixed.
--
-- The generated column concatenates searchable string fields from the
-- record's JSONB payload with weights:
--   A = title (highest relevance)
--   B = shortDescription
--   C = description
--   D = workScope (only when it is a plain string, not a strongRef object)
--
-- Records for collections that lack these fields get an empty tsvector,
-- which is correct — search simply returns no results for them.

CREATE OR REPLACE FUNCTION immutable_to_tsvector(config regconfig, input text)
RETURNS tsvector LANGUAGE sql IMMUTABLE PARALLEL SAFE AS $$
  SELECT to_tsvector(config, input);
$$;

ALTER TABLE record ADD COLUMN search_vector tsvector
  GENERATED ALWAYS AS (
    setweight(immutable_to_tsvector('english'::regconfig, COALESCE(json->>'title', '')), 'A') ||
    setweight(immutable_to_tsvector('english'::regconfig, COALESCE(json->>'shortDescription', '')), 'B') ||
    setweight(immutable_to_tsvector('english'::regconfig, COALESCE(json->>'description', '')), 'C') ||
    setweight(immutable_to_tsvector('english'::regconfig,
      CASE WHEN jsonb_typeof(json->'workScope') = 'string'
           THEN COALESCE(json->>'workScope', '')
           ELSE ''
      END
    ), 'D')
  ) STORED;

CREATE INDEX IF NOT EXISTS idx_record_search_vector
  ON record USING GIN(search_vector);
