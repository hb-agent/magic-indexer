-- Partial unique indexes to make label ingestion idempotent.
--
-- The label table stores both assertions (neg=0) and negations (neg=1).
-- A given (src, uri, val) can have at most one active assertion and at
-- most one active negation at a time; retransmissions from a labeler
-- (e.g. after a backfill + stream overlap) should not create duplicates.
--
-- We split into two partial indexes because the uniqueness columns
-- differ: assertions have an optional cid that MUST be part of the key
-- (so a labeler can label distinct versions), while negations have no
-- cid and dedup on (src, uri, val) alone.
--
-- These indexes also give the Postgres INSERT ... ON CONFLICT DO NOTHING
-- clause a concrete target to fire on, so the clause is no longer dead
-- code on either dialect.
--
-- Dedup existing rows first: CREATE UNIQUE INDEX would fail on pre-
-- existing duplicates that predate this migration. Keep the most-recent
-- row (highest id) in each duplicate group.

DELETE FROM label
WHERE neg = 0
  AND id NOT IN (
    SELECT MAX(id) FROM label
    WHERE neg = 0
    GROUP BY src, uri, val, IFNULL(cid, '')
  );

DELETE FROM label
WHERE neg = 1
  AND id NOT IN (
    SELECT MAX(id) FROM label
    WHERE neg = 1
    GROUP BY src, uri, val
  );

CREATE UNIQUE INDEX IF NOT EXISTS idx_label_unique_assertion
  ON label(src, uri, val, IFNULL(cid, ''))
  WHERE neg = 0;

CREATE UNIQUE INDEX IF NOT EXISTS idx_label_unique_negation
  ON label(src, uri, val)
  WHERE neg = 1;
