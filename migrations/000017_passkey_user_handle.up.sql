SET lock_timeout = '5s';

-- Capture the WebAuthn user handle per passkey credential (#0127) so existing
-- passkeys can be imported into Portiko, which requires it. Nullable: rows
-- created before this migration learn their handle at the next successful login.
ALTER TABLE passkey_credentials ADD COLUMN user_handle BYTEA;

-- The handle issued at the registration/recovery verify step is held with the
-- challenge so the finish step can replay it and store it on the credential.
ALTER TABLE webauthn_challenges ADD COLUMN user_handle BYTEA;
