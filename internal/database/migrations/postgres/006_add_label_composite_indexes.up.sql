-- Composite indexes to support label-filtered record queries at scale.
-- See sqlite/006_add_label_composite_indexes.up.sql for rationale.
--
-- Uses plain CREATE INDEX (not CONCURRENTLY) because the migration
-- framework wraps each file in a transaction, and CONCURRENTLY is not
-- allowed inside a transaction. Operators running this against a large
-- table in production should apply the indexes out-of-band with
-- CONCURRENTLY first; the IF NOT EXISTS makes the migration a no-op
-- after that.
CREATE INDEX IF NOT EXISTS idx_label_uri_src_neg ON label(uri, src, neg);
CREATE INDEX IF NOT EXISTS idx_label_uri_val_neg ON label(uri, val, neg);

ANALYZE label;
