-- Composite indexes to support label-filtered record queries at scale.
--
-- The label-filter join in records.GetByCollectionWithLabelFilterAndKeysetCursor
-- issues an EXISTS subquery per candidate record:
--
--   EXISTS (SELECT 1 FROM label l
--           WHERE l.uri = r.uri AND l.src = ? AND l.neg = 0 AND l.val IN (...))
--
-- That lookup can hit any of (uri, src, val) as the leading column; the
-- most selective in practice is (uri, src) because a given record rarely
-- has labels from many labelers but may have many label values from one.
--
-- The negation NOT EXISTS check uses (uri, val, neg) ordering to find
-- retractions, so we add a dedicated composite there too.
CREATE INDEX IF NOT EXISTS idx_label_uri_src_neg ON label(uri, src, neg);
CREATE INDEX IF NOT EXISTS idx_label_uri_val_neg ON label(uri, val, neg);
