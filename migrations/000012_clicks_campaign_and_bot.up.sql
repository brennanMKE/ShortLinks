-- Denormalizes campaign_id onto clicks (#0100) and adds the is_bot column
-- bot classification (#0101) will populate.
--
-- campaign_id is resolved from links.campaign_id AT RECORD TIME and stored on
-- the click row itself, rather than joined at query time. A link can be
-- reassigned to a different campaign or unassigned entirely; without this
-- denormalization, a query joining clicks -> links -> campaigns would
-- silently rewrite history — a click that happened while the link was in
-- Campaign A would start reporting as Campaign B (or no campaign) the moment
-- the link moved. ON DELETE SET NULL mirrors links.campaign_id (migration
-- 000011) and clicks.link_id (migration 000003): deleting a campaign
-- unassigns its historical clicks' campaign_id, it never deletes click rows.
--
-- Existing rows get campaign_id = NULL, which is correct — they predate
-- campaigns entirely. Do NOT backfill by guessing from utm_campaign strings;
-- a wrong guess is worse than a known gap (same rationale as migration
-- 000011's UTM columns).
--
-- is_bot defaults FALSE and nothing populates it yet — the classification
-- logic is #0101. It is added here so there is one clicks migration rather
-- than two.
ALTER TABLE clicks
    ADD COLUMN campaign_id BIGINT REFERENCES campaigns(id) ON DELETE SET NULL,
    ADD COLUMN is_bot      BOOLEAN NOT NULL DEFAULT FALSE;

-- Aggregate/lookup clicks by campaign. Partial so the vast majority of
-- clicks (no campaign, same as most links) never enter the index.
CREATE INDEX idx_clicks_campaign_id ON clicks (campaign_id) WHERE campaign_id IS NOT NULL;

-- Campaign-scoped time-window queries (#0102's campaign timeseries). Partial
-- for the same reason as idx_clicks_campaign_id.
CREATE INDEX idx_clicks_campaign_time ON clicks (campaign_id, clicked_at) WHERE campaign_id IS NOT NULL;
