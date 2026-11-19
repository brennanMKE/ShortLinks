CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_clicks_link_time ON clicks (link_id, clicked_at);
