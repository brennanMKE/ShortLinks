-- Reverses 000025. Dropping the column also drops its CHECK constraint and
-- the partial unique index; the explicit DROP INDEX keeps the intent visible.
DROP INDEX IF EXISTS idx_links_user_external_id;
ALTER TABLE links DROP COLUMN IF EXISTS external_id;
