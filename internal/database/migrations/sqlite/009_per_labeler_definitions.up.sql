-- Per-labeler label_definition semantics (fixes issue #2).
--
-- Before this migration, label_definition used `val` as the primary
-- key. ATProto allows each labeler to define the same label value with
-- its own semantics (description, severity, default visibility), so a
-- global key meant "whoever got there first wins" — and in our ingest
-- path, ensureDefinition silently discarded every subsequent labeler's
-- semantics.
--
-- This migration restructures label_definition around a composite
-- (src, val) primary key, and gives actor_label_preference the same
-- treatment so users can scope their visibility preferences per
-- labeler. The pre-seeded Bluesky default rows are attributed to a
-- sentinel src ('did:web:system') so the admin UI still has a place
-- to hang takedown/warn defaults that don't belong to any specific
-- external labeler.
--
-- SQLite cannot DROP CONSTRAINT, so we rebuild each affected table
-- with the new schema, copy the data, and swap.

-- =============================================================================
-- Step 1: label_definition with composite (src, val) PK
-- =============================================================================

CREATE TABLE label_definition_new (
  src TEXT NOT NULL,
  val TEXT NOT NULL,
  description TEXT NOT NULL,
  severity TEXT NOT NULL CHECK (severity IN ('inform', 'alert', 'takedown')),
  default_visibility TEXT NOT NULL DEFAULT 'warn',
  created_at TEXT NOT NULL DEFAULT (datetime('now')),
  PRIMARY KEY (src, val)
);

-- Attribute every pre-existing seeded row to the system sentinel.
INSERT INTO label_definition_new (src, val, description, severity, default_visibility, created_at)
  SELECT 'did:web:system', val, description, severity, default_visibility, created_at
  FROM label_definition;

-- Backfill a definition row for every (src, val) currently present in
-- the label table so the new key space is consistent with the data
-- that's already been ingested. Uses the same shape as ensureDefinition:
-- empty description, severity=inform, default_visibility=warn.
INSERT OR IGNORE INTO label_definition_new (src, val, description, severity, default_visibility)
  SELECT DISTINCT l.src, l.val, '', 'inform', 'warn'
  FROM label l;

DROP TABLE label_definition;
ALTER TABLE label_definition_new RENAME TO label_definition;

-- =============================================================================
-- Step 2: label table, rebuilt without the old val FK
-- =============================================================================
--
-- The previous schema had FOREIGN KEY (val) REFERENCES
-- label_definition(val), which is no longer valid now that `val` on
-- the parent table is not unique on its own. We drop the FK entirely
-- and rely on application-layer ensureDefinition — which already
-- handled consistency before this migration.

CREATE TABLE label_new (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  src TEXT NOT NULL,
  uri TEXT NOT NULL,
  cid TEXT,
  val TEXT NOT NULL,
  neg INTEGER NOT NULL DEFAULT 0,
  cts TEXT NOT NULL DEFAULT (datetime('now')),
  exp TEXT
);

INSERT INTO label_new (id, src, uri, cid, val, neg, cts, exp)
  SELECT id, src, uri, cid, val, neg, cts, exp FROM label;

DROP TABLE label;
ALTER TABLE label_new RENAME TO label;

-- Recreate every index that lived on the old label table. These
-- mirror migrations 003, 006, and 007.
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

-- =============================================================================
-- Step 3: actor_label_preference with composite (did, src, label_val) PK
-- =============================================================================

CREATE TABLE actor_label_preference_new (
  did TEXT NOT NULL,
  src TEXT NOT NULL,
  label_val TEXT NOT NULL,
  visibility TEXT NOT NULL,
  created_at TEXT NOT NULL DEFAULT (datetime('now')),
  PRIMARY KEY (did, src, label_val)
);

-- Existing preferences are attributed to the system sentinel to match
-- the label_definition migration above.
INSERT INTO actor_label_preference_new (did, src, label_val, visibility, created_at)
  SELECT did, 'did:web:system', label_val, visibility, created_at
  FROM actor_label_preference;

DROP TABLE actor_label_preference;
ALTER TABLE actor_label_preference_new RENAME TO actor_label_preference;

CREATE INDEX IF NOT EXISTS idx_actor_label_preference_did ON actor_label_preference(did);
