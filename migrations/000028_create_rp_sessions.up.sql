SET lock_timeout = '5s';

-- Server-side sessions for sign-in through the embedded Portiko provider
-- (#0132). This is the table Portiko's rp/session/pgstore reads and writes; the
-- columns and index names are exactly the ones its Migrate would create, so the
-- two agree.
--
-- Nothing a session holds is stored in the clear: id_hash is SHA-256 of the
-- cookie value (a copy of the table cannot be replayed as cookies), and sealed
-- holds the access, refresh and ID tokens, email and name under AES-256-GCM with
-- id_hash as associated data. subject and sid stay plain because back-channel
-- logout queries on them. subject is the Portiko subject, i.e. users.subject.
-- The table stays empty on an instance that has PORTIKO_ENABLED off.
CREATE TABLE IF NOT EXISTS rp_sessions (
    id_hash         TEXT PRIMARY KEY,
    subject         TEXT NOT NULL,
    sid             TEXT NOT NULL DEFAULT '',
    profile_version BIGINT NOT NULL DEFAULT 0,
    sealed          BYTEA NOT NULL,
    expires_at      TIMESTAMPTZ NOT NULL,
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS rp_sessions_subject ON rp_sessions (subject);
CREATE INDEX IF NOT EXISTS rp_sessions_expires ON rp_sessions (expires_at);
