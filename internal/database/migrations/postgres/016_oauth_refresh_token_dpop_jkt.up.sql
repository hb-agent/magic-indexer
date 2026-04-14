ALTER TABLE oauth_refresh_token
  ADD COLUMN IF NOT EXISTS dpop_jkt text,
  ADD COLUMN IF NOT EXISTS original_issued_at bigint;

-- Backfill: existing tokens inherit their current created_at as original_issued_at.
UPDATE oauth_refresh_token
SET original_issued_at = created_at
WHERE original_issued_at IS NULL;
