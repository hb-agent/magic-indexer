DROP INDEX IF EXISTS idx_record_search_vector;
ALTER TABLE record DROP COLUMN IF EXISTS search_vector;
DROP FUNCTION IF EXISTS immutable_to_tsvector(regconfig, text);
