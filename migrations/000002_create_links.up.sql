-- links: short codes that map a key to a destination URL, owned by a user.
-- active + denied_reason together encode the effective link state (see PRD).
CREATE TABLE links (
    id              BIGSERIAL PRIMARY KEY,
    user_id         BIGINT NOT NULL REFERENCES users(id),
    key             VARCHAR(12) UNIQUE NOT NULL,
    destination_url TEXT NOT NULL,
    title           TEXT,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at      TIMESTAMPTZ,
    active          BOOLEAN DEFAULT TRUE,
    denied_reason   SMALLINT NOT NULL DEFAULT 0
);

-- key UNIQUE constraint already provides the primary redirect-lookup index;
-- no separate index on key is required.

-- List all links owned by a user.
CREATE INDEX idx_links_user_id ON links (user_id);

-- Per-user deduplication lookup. Only non-denied links participate, so a
-- re-submitted blocked URL is re-evaluated rather than silently reactivated.
CREATE INDEX idx_links_user_destination ON links (user_id, destination_url)
    WHERE denied_reason = 0;

-- Admin queries for all denied links.
CREATE INDEX idx_links_denied_reason ON links (denied_reason)
    WHERE denied_reason > 0;
