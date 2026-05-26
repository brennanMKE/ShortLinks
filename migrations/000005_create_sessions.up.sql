-- sessions: active authenticated sessions. The token is stored as an
-- HttpOnly; Secure; SameSite=Strict cookie and looked up on every request.
CREATE TABLE sessions (
    id           BIGSERIAL PRIMARY KEY,
    user_id      BIGINT NOT NULL REFERENCES users(id),
    token        TEXT UNIQUE NOT NULL,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at   TIMESTAMPTZ NOT NULL,
    last_seen_at TIMESTAMPTZ NOT NULL
);

-- token UNIQUE constraint already provides the per-request lookup index;
-- no separate index required.
