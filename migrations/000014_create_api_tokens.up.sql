-- api_tokens: named, long-lived bearer credentials a user mints in the web UI
-- and hands to a script or agent. Unlike sessions, the plaintext token is
-- NEVER stored — only its SHA-256 digest — so a database leak (or a backup in
-- the wrong hands) does not leak usable credentials. The plaintext is shown to
-- the user exactly once, at creation.
--
-- token_prefix is the first few characters of the plaintext ("slk_AbCdEfGh"),
-- kept only so the UI can tell tokens apart; it is far too short to be useful
-- to an attacker.
--
-- expires_at NULL means the token never expires. revoked_at is a soft delete:
-- the row stays so audit entries that reference it keep their meaning.
CREATE TABLE api_tokens (
    id           BIGSERIAL PRIMARY KEY,
    user_id      BIGINT NOT NULL REFERENCES users(id),
    name         TEXT NOT NULL,
    token_prefix TEXT NOT NULL,
    token_hash   BYTEA NOT NULL UNIQUE,
    read_only    BOOLEAN NOT NULL DEFAULT FALSE,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at   TIMESTAMPTZ,
    last_used_at TIMESTAMPTZ,
    last_used_ip INET,
    revoked_at   TIMESTAMPTZ
);

-- token_hash UNIQUE provides the per-request lookup index.

-- List a user's tokens; also enforces one live token per name per user so the
-- UI (and audit metadata) can refer to a token by its name unambiguously.
CREATE UNIQUE INDEX idx_api_tokens_user_name_live
    ON api_tokens (user_id, name) WHERE revoked_at IS NULL;
CREATE INDEX idx_api_tokens_user_id ON api_tokens (user_id);
