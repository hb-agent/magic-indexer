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
CREATE UNIQUE INDEX IF NOT EXISTS idx_label_unique_assertion
  ON label(src, uri, val, IFNULL(cid, ''))
  WHERE neg = 0;

CREATE UNIQUE INDEX IF NOT EXISTS idx_label_unique_negation
  ON label(src, uri, val)
  WHERE neg = 1;
