-- ShortLinks — one-time database initialisation
--
-- Run once by a PostgreSQL superuser (e.g. the `postgres` role) BEFORE running
-- migrations. This creates the application login role and the database it owns.
-- It does NOT create any tables — schema is managed by golang-migrate in
-- migrations/ (run with: migrate -path migrations -database $DATABASE_URL up).
--
-- Usage:
--   psql -U postgres -f scripts/db/create.sql
--
-- IMPORTANT: Replace the placeholder password below with a real, strong secret
-- before running this in any non-throwaway environment, and keep that secret out
-- of source control. The DATABASE_URL in your .env must use the same role name
-- (shortlinks), database name (shortlinks), and the password you set here.

-- Create the application login role (idempotent: skip if it already exists).
DO
$$
BEGIN
    IF NOT EXISTS (SELECT FROM pg_catalog.pg_roles WHERE rolname = 'shortlinks') THEN
        CREATE ROLE shortlinks LOGIN PASSWORD 'CHANGE_ME_IN_PRODUCTION';
    END IF;
END
$$;

-- Create the application database owned by the role.
-- NOTE: PostgreSQL does not support CREATE DATABASE IF NOT EXISTS, and
-- CREATE DATABASE cannot run inside a transaction/DO block. Re-running this
-- statement against an existing database will raise "database already exists";
-- that error is safe to ignore on a re-run.
CREATE DATABASE shortlinks OWNER shortlinks;

-- Ensure the role has full privileges on the database.
GRANT ALL PRIVILEGES ON DATABASE shortlinks TO shortlinks;
