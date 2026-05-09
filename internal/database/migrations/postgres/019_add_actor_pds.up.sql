-- Add a `pds` column to the actor table so the indexer can record each
-- author's PDS service endpoint and expose it via GraphQL. Filtering by
-- PDS (e.g. excluding test PDSes from a public feed) joins record→actor
-- on did, so the column lives on actor rather than denormalised onto
-- every record row. Nullable while backfill catches up.
ALTER TABLE actor ADD COLUMN pds TEXT;
