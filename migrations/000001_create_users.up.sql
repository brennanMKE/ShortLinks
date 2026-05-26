-- users: account records. The first user to register on a fresh install is
-- promoted to admin. Email is used for identity/recovery only, never to log in.
CREATE TABLE users (
    id            BIGSERIAL PRIMARY KEY,
    email         TEXT UNIQUE NOT NULL,
    is_admin      BOOLEAN NOT NULL DEFAULT FALSE,
    active        BOOLEAN NOT NULL DEFAULT TRUE,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_login_at TIMESTAMPTZ
);

-- Note: the UNIQUE constraint on email creates its own index; no separate
-- index is required for email lookups.
