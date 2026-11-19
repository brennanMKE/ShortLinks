-- #0180: optional fallback URLs. A link that is expired or deactivated redirects
-- to links.fallback_url (or, when that is NULL, its campaign's
-- default_fallback_url) instead of answering 404/410. NULL means no fallback.
ALTER TABLE links ADD COLUMN fallback_url TEXT;
ALTER TABLE campaigns ADD COLUMN default_fallback_url TEXT;
