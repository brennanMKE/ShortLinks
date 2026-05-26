-- ShortLinks — development teardown
--
-- Drops the application database and login role so a developer can reset to a
-- clean slate. Both statements are idempotent (IF EXISTS), so this is safe to
-- run whether or not the objects are present.
--
-- Usage:
--   psql -U postgres -f scripts/db/drop.sql
--
-- WARNING: This permanently destroys all data in the `shortlinks` database.
-- Intended for development resets only — never run against production.

-- Drop the database first; the role cannot be dropped while it owns objects.
DROP DATABASE IF EXISTS shortlinks;

-- Drop the application login role.
DROP ROLE IF EXISTS shortlinks;
