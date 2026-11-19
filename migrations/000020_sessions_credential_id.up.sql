SET lock_timeout = '5s';

-- Record which passkey a session was created with (#0188), so the Account page
-- can mark "This device". Nullable: sessions that predate this migration, and
-- sessions created without a passkey, have none. If the passkey is later
-- revoked the session stays valid and simply stops being tied to a passkey.
ALTER TABLE sessions
    ADD COLUMN credential_id BIGINT REFERENCES passkey_credentials(id) ON DELETE SET NULL;

-- ON DELETE SET NULL looks up sessions by credential_id when a passkey is
-- revoked; without this index that is a full scan under RevokeCredential's lock.
CREATE INDEX sessions_credential_id_idx ON sessions (credential_id) WHERE credential_id IS NOT NULL;
