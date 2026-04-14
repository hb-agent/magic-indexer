ALTER TABLE oauth_refresh_token
  DROP COLUMN IF EXISTS dpop_jkt,
  DROP COLUMN IF EXISTS original_issued_at;
