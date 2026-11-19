SET lock_timeout = '5s';

-- The opaque Portiko subject for this user (#0131). NULL until `shortlinks
-- portiko-import` has copied the account into the embedded provider; it is the
-- value Portiko issues as the token `sub`. Nullable and UNIQUE: Postgres allows
-- many NULLs under a unique constraint.
ALTER TABLE users ADD COLUMN subject TEXT UNIQUE;
