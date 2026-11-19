-- Narrowing back to 12 fails if any key longer than 12 characters exists; delete
-- or re-key those links first.
ALTER TABLE links ALTER COLUMN key TYPE VARCHAR(12);
