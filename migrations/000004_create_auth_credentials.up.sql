-- pending_registrations: short-lived rows tracking an email mid-registration.
-- Created before webauthn_challenges because the latter references
-- pending_registrations(token).
CREATE TABLE pending_registrations (
    id         BIGSERIAL PRIMARY KEY,
    email      TEXT NOT NULL,
    token      TEXT UNIQUE NOT NULL,
    expires_at TIMESTAMPTZ
);

-- Background sweep of expired rows.
CREATE INDEX idx_pending_registrations_expires_at ON pending_registrations (expires_at);

-- passkey_credentials: one row per registered WebAuthn credential.
CREATE TABLE passkey_credentials (
    id            BIGSERIAL PRIMARY KEY,
    user_id       BIGINT REFERENCES users(id),
    credential_id BYTEA UNIQUE NOT NULL,
    public_key    BYTEA NOT NULL,
    aaguid        UUID,
    sign_count    BIGINT NOT NULL DEFAULT 0,
    device_name   TEXT,
    created_at    TIMESTAMPTZ,
    last_used_at  TIMESTAMPTZ
);

-- credential_id UNIQUE constraint already provides the WebAuthn assertion
-- lookup index; no separate index required.

-- webauthn_challenges: ephemeral challenges issued during registration and
-- authentication ceremonies. Deleted after use or expiry.
CREATE TABLE webauthn_challenges (
    id                         BIGSERIAL PRIMARY KEY,
    challenge                  BYTEA UNIQUE NOT NULL,
    user_id                    BIGINT REFERENCES users(id),
    pending_registration_token TEXT REFERENCES pending_registrations(token),
    purpose                    TEXT,
    expires_at                 TIMESTAMPTZ
);

-- Background sweep of expired rows.
CREATE INDEX idx_webauthn_challenges_expires_at ON webauthn_challenges (expires_at);
