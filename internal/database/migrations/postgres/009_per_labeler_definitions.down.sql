-- Reverse of 009. Lossy: any per-labeler rows (src != did:web:system)
-- created by ensureDefinition or the admin UI are dropped so the old
-- val-only PK can be restored.

-- Drop rows that cannot be represented in the old PK.
DELETE FROM label_definition WHERE src <> 'did:web:system';
DELETE FROM actor_label_preference WHERE src <> 'did:web:system';

-- Restore PKs and drop src column.
ALTER TABLE actor_label_preference DROP CONSTRAINT actor_label_preference_pkey;
ALTER TABLE actor_label_preference ADD PRIMARY KEY (did, label_val);
ALTER TABLE actor_label_preference DROP COLUMN src;

ALTER TABLE label_definition DROP CONSTRAINT label_definition_pkey;
ALTER TABLE label_definition ADD PRIMARY KEY (val);
ALTER TABLE label_definition DROP COLUMN src;

-- Re-add the old label.val FK now that label_definition.val is
-- unique again.
ALTER TABLE label ADD CONSTRAINT label_val_fkey
  FOREIGN KEY (val) REFERENCES label_definition(val);
