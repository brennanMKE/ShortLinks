-- #0182: an optional client-supplied external id on links, so a client such as
-- CommunityPlatform can find its own link again (an invitation's link, say)
-- without storing the key or matching titles.
--
-- Additive, and short on locks:
--   * ADD COLUMN with no default is a catalog change; existing rows keep NULL
--     and nothing is rewritten. The CHECK only constrains non-NULL values, and
--     every existing row is NULL, so adding it needs no table scan. The ALTER
--     does take a brief ACCESS EXCLUSIVE lock on links.
--   * The unique index is partial (WHERE external_id IS NOT NULL), so it holds
--     no rows when created. A plain CREATE INDEX still scans links once to
--     evaluate the predicate and blocks writes (not reads) while it does. It
--     is not CONCURRENTLY because golang-migrate runs a multi-statement file
--     in one transaction, where CREATE INDEX CONCURRENTLY is not allowed.
--   * lock_timeout makes the migration fail fast instead of queueing behind a
--     long transaction and blocking every other query on links; re-run it.
--
-- external_id is unique per user, not globally: two users may reuse an id.
-- 1 to 128 printable ASCII characters (0x20 to 0x7E).
SET lock_timeout = '5s';

ALTER TABLE links
    ADD COLUMN external_id TEXT
        CONSTRAINT links_external_id_format CHECK (external_id ~ '^[ -~]{1,128}$');

CREATE UNIQUE INDEX idx_links_user_external_id
    ON links (user_id, external_id)
    WHERE external_id IS NOT NULL;

RESET lock_timeout;
