-- Partial unique indexes to make label ingestion idempotent.
-- See sqlite/007_add_label_unique_indexes.up.sql for rationale.
--
-- Note: Postgres defines label.neg as BOOLEAN (migration 003), so we
-- compare against false/true here. The SQLite variant uses 0/1 because
-- SQLite stores neg as INTEGER.
--
-- Dedup existing rows first so CREATE UNIQUE INDEX does not fail on
-- pre-existing duplicates from earlier deployments that ran migrations
-- 003-006 without unique constraints. Keep the most-recent row (highest
-- id) in each duplicate group.

DELETE FROM label
WHERE neg = false
  AND id NOT IN (
    SELECT MAX(id) FROM label
    WHERE neg = false
    GROUP BY src, uri, val, COALESCE(cid, '')
  );

DELETE FROM label
WHERE neg = true
  AND id NOT IN (
    SELECT MAX(id) FROM label
    WHERE neg = true
    GROUP BY src, uri, val
  );

CREATE UNIQUE INDEX IF NOT EXISTS idx_label_unique_assertion
  ON label(src, uri, val, COALESCE(cid, ''))
  WHERE neg = false;

CREATE UNIQUE INDEX IF NOT EXISTS idx_label_unique_negation
  ON label(src, uri, val)
  WHERE neg = true;
