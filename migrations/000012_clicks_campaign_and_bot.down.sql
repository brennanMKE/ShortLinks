DROP INDEX idx_clicks_campaign_time;
DROP INDEX idx_clicks_campaign_id;

ALTER TABLE clicks
    DROP COLUMN campaign_id,
    DROP COLUMN is_bot;
