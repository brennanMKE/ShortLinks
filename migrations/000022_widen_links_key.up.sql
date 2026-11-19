-- #0179: allow generated and custom keys up to 32 characters (was 12). Widening
-- a VARCHAR is a metadata-only change in Postgres; existing keys are untouched.
ALTER TABLE links ALTER COLUMN key TYPE VARCHAR(32);
