-- Add backup-eligible and backup-state flag columns to passkey_credentials.
-- go-webauthn's ValidateLogin treats the Backup Eligible (BE) flag as immutable:
-- the value recorded at registration must equal the value presented on every
-- assertion. These columns were missing, so credentials were rehydrated with
-- BE=false even when registered as synced/backup-eligible passkeys, causing
-- every Apple/iCloud Keychain assertion to fail with a flag inconsistency.

ALTER TABLE passkey_credentials
    ADD COLUMN backup_eligible BOOLEAN NOT NULL DEFAULT FALSE,
    ADD COLUMN backup_state    BOOLEAN NOT NULL DEFAULT FALSE;

-- Backfill existing rows. Every credential currently enrolled in this
-- deployment is an Apple iCloud Keychain / synced passkey, which always sets
-- BE=true. Setting backup_eligible=TRUE unblocks the already-enrolled admin
-- credential without requiring a new registration ceremony.
UPDATE passkey_credentials SET backup_eligible = TRUE;
