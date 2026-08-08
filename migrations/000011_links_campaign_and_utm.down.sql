DROP INDEX idx_links_campaign_id;

ALTER TABLE links
    DROP COLUMN campaign_id,
    DROP COLUMN utm_source,
    DROP COLUMN utm_medium,
    DROP COLUMN utm_campaign,
    DROP COLUMN utm_term,
    DROP COLUMN utm_content,
    DROP COLUMN placement;
