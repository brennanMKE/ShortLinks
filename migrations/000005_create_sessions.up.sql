-- sessions: active authenticated sessions. The token is stored as an
-- HttpOnly; Secure; SameSite=Strict cookie and looked up on every request.
CREATE TABLE sessions (
    id           BIGSERIAL PRIMARY KEY,
    user_id      BIGINT REFERENCES users(id),
    token        TEXT UNIQUE NOT NULL,
    created_at   TIMESTAMPTZ,
    expires_at   TIMESTAMPTZ,
    last_seen_at TIMESTAMPTZ
);

-- token UNIQUE constraint already provides the per-request lookup index;
-- no separate index required.
