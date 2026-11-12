-- WARNING: running this down migration PERMANENTLY LOSES every user handle
-- captured at login. They live only in this column and on users' authenticators;
-- they cannot be regenerated server-side, and each lost handle means a passkey
-- that must be re-enrolled at the Portiko cutover (#0131). Back up first:
--   \copy (SELECT credential_id, user_handle FROM passkey_credentials
--           WHERE user_handle IS NOT NULL) TO 'user_handles.csv' CSV
ALTER TABLE webauthn_challenges DROP COLUMN user_handle;
ALTER TABLE passkey_credentials DROP COLUMN user_handle;
