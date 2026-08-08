-- Adds campaign membership and discrete UTM columns to links (#0099),
-- reversing the #0048 storage decision to bake UTM params into
-- destination_url ONLY. destination_url keeps being the composed URL (the
-- destination site's own analytics depends on it arriving); these columns
-- are additive, recording what the builder was given so the app can read
-- its own data back without re-parsing a URL.
--
-- placement is deliberately separate from utm_content: a physical poster
-- location ("18th & Texas board") is operational metadata, not what gets
-- sent to the destination site's analytics. They frequently differ.
--
-- Existing rows keep NULL in all seven columns. Do NOT backfill by parsing
-- destination_url — ambiguous wherever a link already carried its own
-- ?utm_source= from an external source; a wrong guess is worse than a known
-- gap.
ALTER TABLE links
    ADD COLUMN campaign_id  BIGINT REFERENCES campaigns(id) ON DELETE SET NULL,
    ADD COLUMN utm_source   TEXT,
    ADD COLUMN utm_medium   TEXT,
    ADD COLUMN utm_campaign TEXT,
    ADD COLUMN utm_term     TEXT,
    ADD COLUMN utm_content  TEXT,
    ADD COLUMN placement    TEXT;

-- Campaign-scoped link lookups (GET /api/campaigns/{slug}/links). Partial so
-- the vast majority of links (no campaign) never enter the index.
CREATE INDEX idx_links_campaign_id ON links (campaign_id) WHERE campaign_id IS NOT NULL;
