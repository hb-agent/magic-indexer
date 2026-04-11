-- Per-labeler label_definition semantics (fixes issue #2).
-- See sqlite/009_per_labeler_definitions.up.sql for rationale and
-- design. This Postgres variant takes advantage of ALTER TABLE DROP
-- CONSTRAINT, which SQLite lacks, so we can avoid the full
-- table-rebuild dance.

-- =============================================================================
-- Step 1: drop the old label.val FK (constraint name assigned by
-- Postgres when FOREIGN KEY (val) REFERENCES label_definition(val) was
-- declared without a name in migration 003).
-- =============================================================================
ALTER TABLE label DROP CONSTRAINT IF EXISTS label_val_fkey;

-- =============================================================================
-- Step 2: restructure label_definition with a composite (src, val) PK.
-- =============================================================================
ALTER TABLE label_definition ADD COLUMN IF NOT EXISTS src TEXT NOT NULL DEFAULT 'did:web:system';

ALTER TABLE label_definition DROP CONSTRAINT label_definition_pkey;
ALTER TABLE label_definition ADD PRIMARY KEY (src, val);

-- Drop the default now that the column is populated; new rows must
-- specify the src explicitly.
ALTER TABLE label_definition ALTER COLUMN src DROP DEFAULT;

-- Backfill one definition row per distinct (src, val) currently in
-- the label table so the new key space is consistent with ingested
-- data. Matches the shape ensureDefinition would produce.
INSERT INTO label_definition (src, val, description, severity, default_visibility)
  SELECT DISTINCT l.src, l.val, '', 'inform', 'warn'
  FROM label l
  ON CONFLICT (src, val) DO NOTHING;

-- =============================================================================
-- Step 3: restructure actor_label_preference similarly.
-- =============================================================================
ALTER TABLE actor_label_preference ADD COLUMN IF NOT EXISTS src TEXT NOT NULL DEFAULT 'did:web:system';

ALTER TABLE actor_label_preference DROP CONSTRAINT actor_label_preference_pkey;
ALTER TABLE actor_label_preference ADD PRIMARY KEY (did, src, label_val);

ALTER TABLE actor_label_preference ALTER COLUMN src DROP DEFAULT;
