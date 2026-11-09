-- The signed-out home page's primary URL (#0125). Empty = unset (bounce to /#/login as before).
INSERT INTO settings (key, value, updated_at) VALUES ('home_redirect_url', '', now()) ON CONFLICT DO NOTHING;
