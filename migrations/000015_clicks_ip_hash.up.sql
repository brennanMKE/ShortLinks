-- #0121: stop storing visitor IPs. clicks.ip_address (INET, raw) is replaced by
-- clicks.ip_hash (BYTEA, 32 bytes): a keyed HMAC-SHA256 computed in Go with a
-- daily-rotating salt (internal/clicks/iphash.go).
--
-- Existing rows get NULL, not a transformed value. SQL cannot reproduce the
-- Go-side keyed daily hash, and an unsalted sha256 of an IPv4 address is
-- reversible by brute force (2^32 inputs), so dropping the raw value is the
-- only honest one-way transform.
ALTER TABLE clicks
    DROP COLUMN ip_address,
    ADD COLUMN ip_hash BYTEA;
