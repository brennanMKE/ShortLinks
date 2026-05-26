-- clicks: one row per redirect, capturing request metadata and any inbound
-- UTM parameters for analytics.
CREATE TABLE clicks (
    id           BIGSERIAL PRIMARY KEY,
    link_id      BIGINT REFERENCES links(id),
    clicked_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    ip_address   INET,
    user_agent   TEXT,
    referer      TEXT,
    utm_source   TEXT,
    utm_medium   TEXT,
    utm_campaign TEXT,
    utm_term     TEXT,
    utm_content  TEXT
);

-- Aggregate click counts per link.
CREATE INDEX idx_clicks_link_id ON clicks (link_id);

-- Time-range queries for analytics.
CREATE INDEX idx_clicks_clicked_at ON clicks (clicked_at);
