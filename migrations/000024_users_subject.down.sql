-- Dropping this column forgets which Portiko account each user was imported as.
-- A later import would adopt the account by its verified email, so no account is
-- duplicated, but record the mapping first if you need it:
--   \copy (SELECT id, email, subject FROM users WHERE subject IS NOT NULL) TO 'subjects.csv' CSV
ALTER TABLE users DROP COLUMN subject;
