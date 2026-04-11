-- Partial unique indexes to make label ingestion idempotent.
-- See sqlite/007_add_label_unique_indexes.up.sql for rationale.
CREATE UNIQUE INDEX IF NOT EXISTS idx_label_unique_assertion
  ON label(src, uri, val, COALESCE(cid, ''))
  WHERE neg = 0;

CREATE UNIQUE INDEX IF NOT EXISTS idx_label_unique_negation
  ON label(src, uri, val)
  WHERE neg = 1;
