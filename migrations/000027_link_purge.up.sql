SET lock_timeout = '5s';

-- #0183: real deletion (purge) of a link so a secret-bearing destination URL
-- does not stay in the database.

-- Optional retention: the sweeper purges a link this many days after its
-- expires_at. NULL (the default) never auto-purges.
ALTER TABLE links
    ADD COLUMN purge_after_expiry_days INTEGER
    CHECK (purge_after_expiry_days IS NULL OR purge_after_expiry_days > 0);

-- Tombstones for purged keys. A key is not secret, but reusing it would send
-- every printed or shared copy of the old short URL to a different destination,
-- so a purged key is never issued again. Only the key and a timestamp are kept:
-- no destination, title, owner or campaign.
CREATE TABLE purged_keys (
    key       VARCHAR(32) PRIMARY KEY,
    purged_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Rejects an INSERT into links for a tombstoned key as a unique violation, so
-- every insert path (custom alias or generated, single or batch) reports the
-- same "key already taken" it would for a live link.
CREATE FUNCTION links_reject_purged_key() RETURNS trigger AS $$
BEGIN
    IF EXISTS (SELECT 1 FROM purged_keys WHERE key = NEW.key) THEN
        RAISE EXCEPTION 'link key % was purged and cannot be reused', NEW.key
            USING ERRCODE = 'unique_violation';
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER links_reject_purged_key
    BEFORE INSERT ON links
    FOR EACH ROW EXECUTE FUNCTION links_reject_purged_key();

-- Supports the sweeper's "expired long enough ago" lookup; partial so the vast
-- majority of links (no auto-purge) never enter the index.
CREATE INDEX idx_links_purge_after_expiry ON links (expires_at) WHERE purge_after_expiry_days IS NOT NULL;
