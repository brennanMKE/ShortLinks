DROP INDEX IF EXISTS idx_links_purge_after_expiry;
DROP TRIGGER IF EXISTS links_reject_purged_key ON links;
DROP FUNCTION IF EXISTS links_reject_purged_key();
DROP TABLE IF EXISTS purged_keys;
ALTER TABLE links DROP COLUMN IF EXISTS purge_after_expiry_days;
