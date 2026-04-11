-- Reverse of 009: restore the pre-per-labeler schema. This is a
-- lossy rollback — only the rows attributed to the system sentinel
-- can be kept with their original keys; any per-labeler definitions
-- created by ensureDefinition or the admin UI will be dropped.

CREATE TABLE label_definition_old (
  val TEXT PRIMARY KEY NOT NULL,
  description TEXT NOT NULL,
  severity TEXT NOT NULL CHECK (severity IN ('inform', 'alert', 'takedown')),
  default_visibility TEXT NOT NULL DEFAULT 'warn',
  created_at TEXT NOT NULL DEFAULT (datetime('now'))
);

INSERT INTO label_definition_old (val, description, severity, default_visibility, created_at)
  SELECT val, description, severity, default_visibility, created_at
  FROM label_definition
  WHERE src = 'did:web:system';

DROP TABLE label_definition;
ALTER TABLE label_definition_old RENAME TO label_definition;

-- Rebuild label with the old val FK back.
CREATE TABLE label_old (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  src TEXT NOT NULL,
  uri TEXT NOT NULL,
  cid TEXT,
  val TEXT NOT NULL,
  neg INTEGER NOT NULL DEFAULT 0,
  cts TEXT NOT NULL DEFAULT (datetime('now')),
  exp TEXT,
  FOREIGN KEY (val) REFERENCES label_definition(val)
);

INSERT INTO label_old (id, src, uri, cid, val, neg, cts, exp)
  SELECT id, src, uri, cid, val, neg, cts, exp FROM label
  WHERE val IN (SELECT val FROM label_definition);

DROP TABLE label;
ALTER TABLE label_old RENAME TO label;

CREATE INDEX IF NOT EXISTS idx_label_uri ON label(uri);
CREATE INDEX IF NOT EXISTS idx_label_val ON label(val);
CREATE INDEX IF NOT EXISTS idx_label_src ON label(src);
CREATE INDEX IF NOT EXISTS idx_label_cts ON label(cts DESC);
CREATE INDEX IF NOT EXISTS idx_label_takedown ON label(uri, val, neg);
CREATE INDEX IF NOT EXISTS idx_label_uri_src_neg ON label(uri, src, neg);
CREATE INDEX IF NOT EXISTS idx_label_uri_val_neg ON label(uri, val, neg);
CREATE UNIQUE INDEX IF NOT EXISTS idx_label_unique_assertion
  ON label(src, uri, val, IFNULL(cid, ''))
  WHERE neg = 0;
CREATE UNIQUE INDEX IF NOT EXISTS idx_label_unique_negation
  ON label(src, uri, val)
  WHERE neg = 1;

-- Roll back actor_label_preference similarly.
CREATE TABLE actor_label_preference_old (
  did TEXT NOT NULL,
  label_val TEXT NOT NULL,
  visibility TEXT NOT NULL,
  created_at TEXT NOT NULL DEFAULT (datetime('now')),
  PRIMARY KEY (did, label_val)
);

INSERT INTO actor_label_preference_old (did, label_val, visibility, created_at)
  SELECT did, label_val, visibility, created_at
  FROM actor_label_preference
  WHERE src = 'did:web:system';

DROP TABLE actor_label_preference;
ALTER TABLE actor_label_preference_old RENAME TO actor_label_preference;

CREATE INDEX IF NOT EXISTS idx_actor_label_preference_did ON actor_label_preference(did);
