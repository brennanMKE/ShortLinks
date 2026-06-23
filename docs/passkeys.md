# Passkeys / WebAuthn

ShortLinks uses passkeys (WebAuthn) as its only authentication mechanism. There
are no passwords. Three ceremonies are supported: **registration** (new
account), **login** (assertion), and **recovery** (enrolling a new passkey onto
an existing account that has lost access to its passkeys). All three are
orchestrated by `internal/auth/` and served by `internal/handlers/auth.go`.

---

## Relying Party configuration

The relying party (RP) is configured at startup from two environment variables
read in `internal/config/config.go`:

| Variable | Example | Meaning |
|---|---|---|
| `WEBAUTHN_RP_ID` | `go.sstools.co` | Bare domain, no scheme or port. Browsers scope passkeys to this domain. |
| `WEBAUTHN_RP_ORIGIN` | `https://go.sstools.co` | Full origin used to verify `clientDataJSON.origin` on every assertion. |

Both are passed to `webauthn.New` in `internal/auth/webauthn.go`:

```go
wa, err := webauthn.New(&webauthn.Config{
    RPID:          cfg.WebAuthnRPID,
    RPDisplayName: "ShortLinks",
    RPOrigins:     []string{cfg.WebAuthnRPOrigin},
})
```

**Why exact origin and HTTPS matter.** The WebAuthn spec binds a passkey to its
RP ID at creation time. At assertion time `go-webauthn` checks that
`clientDataJSON.origin` equals one of the configured `RPOrigins`. A scheme
mismatch (`http://` vs `https://`), a port suffix, a trailing slash, or a
subdomain difference all cause assertion failure. WebAuthn also requires a
[Secure Context](https://developer.mozilla.org/en-US/docs/Web/Security/Secure_Contexts):
`navigator.credentials.create()` and `.get()` are only available over HTTPS (or
`localhost`). If the address bar shows anything other than exactly
`https://go.sstools.co`, passkey prompts will either fail silently or reject
the assertion.

To diagnose a mis-configuration, check the running service's environment:

```bash
sudo systemctl show shortlinks -p Environment
# or
grep WEBAUTHN /etc/shortlinks/config.env
```

---

## Ceremony 1 — Registration

Registration is a three-step flow: start (send email), verify (get WebAuthn
options), finish (submit attestation).

### Server side

All logic lives in `internal/auth/registration.go` (`RegistrationService`).

| Step | Endpoint | Handler | Service method |
|---|---|---|---|
| 1 | `POST /auth/register/start` | `RegisterStart` | `StartRegistration` |
| 2 | `GET /auth/register/verify?token=…` | `RegisterVerify` | `VerifyRegistration` |
| 3 | `POST /auth/register/finish?token=…[&device_name=…]` | `RegisterFinish` | `FinishRegistration` |

**StartRegistration** (`registration.go`) normalises the email, checks the
`registrations_enabled` setting (read fresh from DB each call — admin toggles
take effect immediately), rejects already-registered emails, creates a
`pending_registrations` row with a **5-minute TTL**, and emails the magic link.
The response is always the same generic `"Check your email"` — a registered
email gets no response either, so account existence is never revealed.

**VerifyRegistration** validates the magic-link token (existence + TTL), calls
`wa.BeginRegistration` with the shared `registrationOptions()` policy, persists
the challenge bytes to `webauthn_challenges` (purpose `'registration'`, linked
to the token), and returns the `PublicKeyCredentialCreationOptions` JSON.

Registration options policy (from `registrationOptions()` in
`internal/auth/webauthn.go`):
- `residentKey: required` + `userVerification: required` — true discoverable
  passkey.
- `authenticatorAttachment` left unset — platform (e.g. iCloud Keychain) or
  roaming authenticator, the platform chooses.
- `pubKeyCredParams`: ES256 (`-7`) preferred, RS256 (`-257`) accepted.

**FinishRegistration** runs inside a single database transaction:
1. Consumes (deletes) the challenge row — replay prevention.
2. Calls `wa.FinishRegistration` to verify the attestation.
3. Checks user count; promotes the registrant to admin if they are the first
   user or their email matches `ADMIN_EMAIL`.
4. Inserts the `users` row (`CreateUser`).
5. Inserts the `passkey_credentials` row via `InsertCredential`, **including
   `BackupEligible` and `BackupState` from `credential.Flags`** (see the
   credential model section).
6. Deletes the `pending_registrations` row (magic link cannot be reused).
7. Creates a `sessions` row (30-day sliding TTL) and returns the session token.

On commit the handler sets the `shortlinks_session` cookie (`HttpOnly`,
`Secure`, `SameSite=Strict`) via `auth.SetSessionCookie`.

### Client side (`web/src/views/RegisterVerify.svelte`)

The `RegisterVerify` view auto-runs on mount:

1. Calls `GET /auth/register/verify?token=…` via `registerVerify(token)`.
2. Decodes the server options with `toPublicKeyCreationOptions` from
   `web/src/lib/webauthn.ts` (converts base64url binary fields to
   `ArrayBuffer`s).
3. Calls `navigator.credentials.create({ publicKey })`.
4. Serialises the `PublicKeyCredential` with `serializeAttestation` (encodes
   all binary fields back to unpadded base64url).
5. POSTs to `POST /auth/register/finish?token=…` via `registerFinish`.
6. Calls `GET /api/me` to confirm the session, sets `currentUser`, navigates
   to the dashboard.

---

## Ceremony 2 — Login

Login is a two-step flow: start (issue challenge), finish (verify assertion).

### Server side

All logic lives in `internal/auth/login.go` (`LoginService`).

| Step | Endpoint | Handler | Service method |
|---|---|---|---|
| 1 | `GET /auth/login/start[?email=…]` | `LoginStart` | `StartLogin` |
| 2 | `POST /auth/login/finish` | `LoginFinish` | `FinishLogin` |

**StartLogin** accepts an optional email (via query parameter or JSON body).
When the email maps to an existing account with credentials, it calls
`wa.BeginLogin(loginUser)` to produce a scoped `allowCredentials` list that
narrows the platform prompt. When the email is absent, unknown, or the account
has no credentials, it falls back to `wa.BeginDiscoverableLogin()` — the
response is structurally identical in either case, so account existence is never
revealed. The challenge bytes are stored in `webauthn_challenges` (purpose
`'authentication'`, 5-minute TTL; user and token columns are NULL).

**FinishLogin** (`login.go`):
1. Parses the assertion with `protocol.ParseCredentialRequestResponse`.
2. Looks up the credential by raw `credential_id` bytes via `CredentialByID`
   (covers both discoverable and non-discoverable login — the assertion's
   `rawID` always identifies the credential).
3. Checks `users.active`; returns `ErrAccountDeactivated` (→ HTTP 403) when
   false.
4. Opens a transaction and consumes the challenge row (single-use, TTL-checked).
5. Reconstructs the `webauthn.Credential` via `credentialFromRecord`, which
   sets `Flags.BackupEligible` and `Flags.BackupState` from the stored row (see
   credential model below).
6. Calls `wa.ValidateLogin` to verify the signature.
7. Applies sign-count rules:
   - `CloneWarning` (assertion count ≤ stored, stored > 0): log a warning,
     accept, leave sign_count unchanged, refresh `backup_state`.
   - Both zero (synced/iCloud Keychain passkey): accept silently, refresh
     `backup_state`.
   - Normal advance: update `sign_count` and `backup_state`.
8. Updates `users.last_login_at`.
9. Creates a session row, commits.

On any assertion failure the handler always returns HTTP 401 with the generic
`"authentication failed"` body. The client (`Login.svelte:97`) maps this to
`"Sign in failed. No matching passkey, or the request expired."` The real reason
is logged server-side via `s.log.Warn("login: …")` — see **Common failure
modes** below.

### Client side (`web/src/views/Login.svelte`)

The login view runs two concurrent paths:

**Background conditional-UI (passkey autofill):** `startConditional()` fires
on mount. It calls `GET /auth/login/start` (no email), converts the options,
then calls `navigator.credentials.get({ publicKey, mediation: 'conditional',
signal })`. The email `<input>` carries `autocomplete="username webauthn"` so
the browser can surface matching passkeys inline in the autofill dropdown. The
`AbortController` is cancelled when the user starts an explicit sign-in or the
view is destroyed.

**Explicit sign-in:** `handleSignIn()` aborts any in-flight conditional
ceremony, calls `GET /auth/login/start?email=…` (with the typed email if any),
converts the options, then calls `navigator.credentials.get({ publicKey,
mediation: 'optional' })` for a modal prompt, then POSTs the assertion to
`POST /auth/login/finish`.

Both paths run through `runCeremony`, which calls `loginStart`, decodes options
with `toPublicKeyRequestOptions`, invokes `navigator.credentials.get`,
serialises the assertion with `serializeAssertion`, POSTs to `loginFinish`, and
on success calls `getMe()` and navigates to the dashboard.

---

## Ceremony 3 — Recovery (lost passkey)

Recovery enrolls a **new passkey onto an existing account** without creating a
new user row or revoking old credentials.

### Server side

All logic lives in `internal/auth/recovery.go` (`RecoveryService`).

| Step | Endpoint | Handler | Service method |
|---|---|---|---|
| 1 | `POST /auth/recover` | `RecoverStart` | `StartRecovery` |
| 2 | `GET /auth/recover/verify?token=…` | `RecoverVerify` | `VerifyRecovery` |
| 3 | `POST /auth/recover/finish?token=…[&device_name=…]` | `RecoverFinish` | `FinishRecovery` |

**StartRecovery** looks up the email. When the account does not exist, is
inactive, or the email is malformed, it returns nil (the handler always responds
200) — account existence is never leaked. When the account exists and is active
it creates a recovery token in `pending_registrations` with a **15-minute TTL**
and emails the recovery link.

**VerifyRecovery** validates the recovery token and checks the account is still
active. It then calls `wa.BeginRegistration` with the same `registrationOptions()`
policy as normal registration and saves the challenge to `webauthn_challenges`
(purpose `'recovery'`, 15-minute TTL, bound to both the token and the user's
`user_id`).

**FinishRecovery** (inside one transaction):
1. Consumes the recovery challenge row (recovers the stored `user_id`).
2. Verifies the recovery token is still valid.
3. Calls `wa.FinishRegistration` to verify the attestation.
4. Inserts a new `passkey_credentials` row for the **existing** user —
   `InsertCredential` with `BackupEligible`/`BackupState` from
   `credential.Flags`. Existing credentials are untouched.
5. Deletes the recovery token row.
6. Creates a session and returns.

The `is_admin` flag on the existing user row is preserved throughout — recovery
does not change the account in any way except adding a credential.

### First-admin enrollment path

The `seed` command creates the admin user row (`ADMIN_EMAIL`, `is_admin = true`,
`active = true`) but does **not** enroll a passkey. Registration rejects already-
registered emails, so the normal Register form cannot be used. The correct path
for the first admin is **Recover account** — step 10 of `DEPLOYMENT.md`.

Recovery does not check the `registrations_enabled` gate, so it works regardless
of whether new registrations are open. After clicking "Recover account / lost
passkey" on the login page, entering the admin email, and following the recovery
email link, the browser runs `navigator.credentials.create()` to enroll the
passkey and the admin lands on the dashboard.

### Client side (`web/src/views/RecoverVerify.svelte`)

The `RecoverVerify` view mirrors `RegisterVerify` exactly, but uses
`recoverVerify` and `recoverFinish` API calls that hit the `/auth/recover/*`
endpoints. The ceremony steps are identical: fetch options, call
`navigator.credentials.create()`, serialise with `serializeAttestation`, POST
to finish.

---

## Credential model

Credentials are stored in the `passkey_credentials` table. The schema was
established by `migrations/000004_create_auth_credentials.up.sql` and extended
by `migrations/000009_passkey_backup_flags.up.sql`.

### Table columns

| Column | Type | Notes |
|---|---|---|
| `id` | `BIGSERIAL` | Surrogate PK (used by management UI) |
| `user_id` | `BIGINT` FK | Owning account |
| `credential_id` | `BYTEA UNIQUE` | Raw credential ID from the authenticator; lookup key for assertions |
| `public_key` | `BYTEA` | COSE-encoded public key |
| `aaguid` | `UUID` | Authenticator AAGUID; NULL when absent or all-zero (common for iCloud Keychain) |
| `sign_count` | `BIGINT` | Clone-detection counter; 0 for synced passkeys |
| `device_name` | `TEXT` | Optional user-supplied label |
| `backup_eligible` | `BOOLEAN NOT NULL DEFAULT FALSE` | BE flag — immutable after registration (added by migration 000009) |
| `backup_state` | `BOOLEAN NOT NULL DEFAULT FALSE` | BS flag — updated on each successful login (added by migration 000009) |
| `created_at` | `TIMESTAMPTZ` | Enrollment time |
| `last_used_at` | `TIMESTAMPTZ` | Last successful assertion |

### Go types

`StoredCredential` (`internal/auth/store.go`) carries the fields written at
enrollment. `CredentialRecord` carries the fields read back for assertion
verification (same fields, minus `DeviceName`).

### Backup-Eligible / Backup-State and iCloud Keychain

The `backup_eligible` (BE) and `backup_state` (BS) flags come from the
authenticator's [authenticator data flags](https://www.w3.org/TR/webauthn-3/#flags).

- **BE (Backup Eligible):** immutable. Set to `true` when the passkey can be
  synced to a cloud backup (e.g. iCloud Keychain). `go-webauthn`'s
  `ValidateLogin` treats BE as immutable: the value recorded at registration
  **must equal** the value in every subsequent assertion. Failing to persist BE
  at enrollment and rehydrate it at login causes every iCloud Keychain assertion
  to fail with `"Backup Eligible flag inconsistency detected during login
  validation"` — this was the root cause of issue #0047.
- **BS (Backup State):** mutable. Reflects whether the passkey is currently
  backed up. Updated alongside `sign_count` on every successful login via
  `UpdateSignCount` or `TouchCredentialLastUsed`.

The fix in #0047 (migration `000009`) adds both columns, persists them from
`credential.Flags` in both `InsertCredential` call-sites (registration and
recovery), and rehydrates them in `credentialFromRecord` (`login.go`) before
passing the credential to `ValidateLogin`. The backfill
`UPDATE passkey_credentials SET backup_eligible = TRUE` in migration 000009
is safe for this deployment because every enrolled credential is an Apple-synced
passkey; on a deployment with device-bound (BE=false) credentials the backfill
would need to be omitted or made conditional.

`sign_count` for synced passkeys is always 0 on both sides; `FinishLogin`
handles this case explicitly (the `rec.SignCount == 0 && assertionCount == 0`
branch) to avoid spurious clone warnings.

---

## Binary encoding

All binary fields sent between the server and browser are encoded as **unpadded
base64url** (RFC 4648 §5, Go's `base64.RawURLEncoding`). `web/src/lib/webauthn.ts`
provides `bufferToBase64url` / `base64urlToBytes` and the helpers
`toPublicKeyCreationOptions`, `toPublicKeyRequestOptions`, `serializeAttestation`,
and `serializeAssertion` to convert between the server's JSON representation
and the `ArrayBuffer`s the browser WebAuthn API expects. A mismatch — standard
base64 vs. base64url, or padded vs. unpadded — silently breaks the ceremony.

---

## Common failure modes

All assertion failures return HTTP 401 with `{"error":"authentication failed"}`.
The client maps this to `"Sign in failed. No matching passkey, or the request
expired."` The server logs the exact failing step via `s.log.Warn("login: …")`.
To capture the decisive line:

```bash
sudo journalctl -u shortlinks --since "30 min ago" | grep -i "login:"
```

| Log line | Cause | Fix |
|---|---|---|
| `login: resolving credential` | The credential id in the assertion does not match any `passkey_credentials` row. | Verify the credential was enrolled on this server. Check `passkey_credentials` for the account. |
| `login: consuming challenge` | The challenge is expired (5-min TTL) or already consumed. | Start a fresh login. Both conditional-UI and explicit sign-in each issue their own challenge — a slow OS prompt after a second `loginStart` call can push the first challenge past its TTL. |
| `login: validating assertion` | Signature verification failed. Sub-causes: RP ID or origin mismatch (check `WEBAUTHN_RP_ID` / `WEBAUTHN_RP_ORIGIN`), user-verification not satisfied, or BE flag inconsistency. | See the BE/BS section above and check env config. |
| `login: parsing assertion` | The assertion body could not be parsed. | Client-side encoding error; check `serializeAssertion`. |
| `login: possible cloned credential (sign_count regression)` | `sign_count` in the assertion is ≤ the stored value (and stored > 0). Login succeeds but a warning is emitted. | Not an error for synced passkeys (count is always 0). For device-bound credentials it may indicate credential cloning. |

`ErrAccountDeactivated` returns HTTP 403 with `{"error":"Account deactivated"}`
and is not logged as a `login:` warning — it is a distinct sentinel value in
`login.go`.

To check an account's credential row and active flag directly:

```bash
bash scripts/db-status.sh "$ADMIN_EMAIL"
```
