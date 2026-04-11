-- Composite indexes to support label-filtered record queries at scale.
-- See sqlite/006_add_label_composite_indexes.up.sql for rationale.
CREATE INDEX IF NOT EXISTS idx_label_uri_src_neg ON label(uri, src, neg);
CREATE INDEX IF NOT EXISTS idx_label_uri_val_neg ON label(uri, val, neg);
