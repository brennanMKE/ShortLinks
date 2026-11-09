-- #0121: restores the SHAPE of the old schema only. The raw IPs were destroyed
-- by the up migration (that is its purpose), so ip_address comes back empty
-- and the hashes are discarded.
ALTER TABLE clicks
    DROP COLUMN ip_hash,
    ADD COLUMN ip_address INET;
