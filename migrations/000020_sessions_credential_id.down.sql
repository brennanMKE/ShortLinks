DROP INDEX IF EXISTS sessions_credential_id_idx;
ALTER TABLE sessions DROP COLUMN IF EXISTS credential_id;
