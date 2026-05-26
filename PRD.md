# ShortLinks — Product Requirements Document

## Overview

ShortLinks is a self-hosted URL shortener built with Go, backed by PostgreSQL, and deployed on an existing AWS EC2 instance running Apache 2. It provides branded short URLs under `go.sstools.co`, UTM parameter support, and click analytics — essentially a private Bitly.

---

## Infrastructure

| Component | Detail |
|-----------|--------|
| Host | AWS EC2 (existing) |
| Web server | Apache 2 (reverse proxy to Go service) |
| DNS | Wildcard `*.sstools.co` already pointed at EC2 |
| Subdomain | `go.sstools.co` |
| TLS | Let's Encrypt via Certbot (wildcard cert or subdomain cert) |
| Database | PostgreSQL (on EC2 or RDS) |
| App runtime | Go binary managed by systemd |

Apache will act as a reverse proxy: it terminates TLS and forwards requests to the Go service listening on a local port (e.g., `127.0.0.1:8080`).

---

## URL Format

```
https://go.sstools.co/u/{key}
```

- `/u/` is the fixed namespace prefix that distinguishes redirect routes from admin/API routes
- `{key}` is a 6-character alphanumeric identifier (e.g., `8d0d93`)
- Keys are generated at creation time using a random or hash-based scheme
- Full example: `https://go.sstools.co/u/8d0d93`

### UTM Parameter Passthrough

UTM parameters may be appended to the short URL at click time and will be forwarded to the destination:

```
https://go.sstools.co/u/8d0d93?utm_source=email&utm_medium=newsletter&utm_campaign=launch
```

The service will:
1. Detect any `utm_*` query parameters on the inbound request
2. Append them to the destination URL (merging with any already present)
3. Redirect with a `302` (temporary) response

UTM parameters present on the inbound click are also stored in the `clicks` table for analytics.

---

## Database Schema

### `links`

| Column | Type | Notes |
|--------|------|-------|
| `id` | `BIGSERIAL PRIMARY KEY` | |
| `user_id` | `BIGINT REFERENCES users(id) NOT NULL` | Owner — see auth schema |
| `key` | `VARCHAR(12) UNIQUE NOT NULL` | The short code, e.g. `8d0d93` |
| `destination_url` | `TEXT NOT NULL` | Full target URL |
| `title` | `TEXT` | Optional human-readable label |
| `created_at` | `TIMESTAMPTZ` | |
| `expires_at` | `TIMESTAMPTZ` | NULL = never expires |
| `active` | `BOOLEAN DEFAULT TRUE` | `false` = user deactivated or system denied |
| `denied_reason` | `SMALLINT NOT NULL DEFAULT 0` | `0` = not denied; non-zero = denial reason code (see URL Filtering) |

> `users` is defined in the Auth Tables section. Migrations run in order; the `users` migration runs before `links`.

**Effective link states:**

| `active` | `denied_reason` | State |
|----------|-----------------|-------|
| `true` | `0` | Active — redirects normally |
| `false` | `0` | Inactive — user deactivated |
| `false` | `> 0` | Denied — blocked by a URL filter rule |

**Deduplication:** A composite index on `(user_id, destination_url)` supports per-user URL deduplication. Only links with `denied_reason = 0` participate — denied links are excluded so a re-submitted blocked URL is re-evaluated against the current filter rules rather than silently reactivated.

### `clicks`

| Column | Type | Notes |
|--------|------|-------|
| `id` | `BIGSERIAL PRIMARY KEY` | |
| `link_id` | `BIGINT REFERENCES links(id)` | |
| `clicked_at` | `TIMESTAMPTZ` | |
| `ip_address` | `INET` | Hashed or truncated for privacy |
| `user_agent` | `TEXT` | |
| `referer` | `TEXT` | |
| `utm_source` | `TEXT` | |
| `utm_medium` | `TEXT` | |
| `utm_campaign` | `TEXT` | |
| `utm_term` | `TEXT` | |
| `utm_content` | `TEXT` | |

---

## Database Indexes

| Table | Column(s) | Notes |
|-------|-----------|-------|
| `links` | `key` | Covered by `UNIQUE` constraint; primary redirect lookup |
| `links` | `user_id` | List all links owned by a user |
| `links` | `(user_id, destination_url)` WHERE `denied_reason = 0` | Deduplication lookup — excludes denied links |
| `links` | `denied_reason` WHERE `denied_reason > 0` | Admin queries for all denied links |
| `clicks` | `link_id` | Aggregate click counts per link |
| `clicks` | `clicked_at` | Time-range queries for analytics |
| `sessions` | `token` | Covered by `UNIQUE` constraint; hit on every authenticated request |
| `passkey_credentials` | `credential_id` | Covered by `UNIQUE` constraint; WebAuthn assertion lookup |
| `webauthn_challenges` | `expires_at` | Background sweep of expired rows |
| `pending_registrations` | `expires_at` | Background sweep of expired rows |
| `audit_log` | `user_id, created_at DESC` | Fetch audit history for a specific user |
| `audit_log` | `created_at DESC` | Full audit log in reverse chronological order |
| `audit_log` | `action` | Filter by event type |
| `url_filter_rules` | `active` | Load all active rules (checked on every link creation) |

---

## Redirect Cache

The redirect path (`GET /u/{key}`) is the hottest path in the system. Every click resolves a key to a destination URL. An in-memory cache in front of the database eliminates most DB round-trips.

**Library:** `github.com/dgraph-io/ristretto` — concurrent-safe LRU with TinyLFU eviction policy, suitable for a single-instance Go process.

**Behavior:**

| Scenario | Action |
|----------|--------|
| Cache hit | Return cached `{destination_url, active, expires_at}` immediately; skip DB |
| Cache miss | Query DB, populate cache, then redirect |
| Link deactivated | Explicit cache key deletion on `DELETE /api/links/{key}` |
| Link expired | `expires_at` is checked on the cached value; no special invalidation needed |

**Configuration (via `.env`):**

| Variable | Default | Description |
|----------|---------|-------------|
| `CACHE_MAX_COST` | `10000` | Max number of cached entries |
| `CACHE_TTL_SECONDS` | `300` | Entry TTL (5 minutes) |

The cache lives in `internal/cache/` and is injected into the links domain package. On a cache miss for a key that does not exist in the DB, a negative entry (null destination) is cached with a shorter TTL (30 seconds) to protect against repeated lookups of invalid keys.

---

## URL Filtering

### Denial Reason Codes

A `SMALLINT` code stored in `links.denied_reason`. Zero means the link is not denied; any non-zero value means it was blocked. The same codes are referenced by `url_filter_rules.reason_code`.

| Code | Constant | Label |
|------|----------|-------|
| `0` | `none` | Not denied |
| `1` | `malware` | Malware or ransomware |
| `2` | `phishing` | Phishing |
| `3` | `spam` | Spam |
| `4` | `adult_content` | Adult content |
| `5` | `policy_violation` | Policy violation |
| `6` | `other` | Other |

---

### `url_filter_rules` Table

Admin-managed regex patterns. Each rule maps a pattern to a denial reason code. Rules are evaluated in the Go service — not in PostgreSQL — using `regexp.MatchString`.

| Column | Type | Notes |
|--------|------|-------|
| `id` | `BIGSERIAL PRIMARY KEY` | |
| `pattern` | `TEXT NOT NULL` | Go-compatible regular expression tested against `destination_url` |
| `reason_code` | `SMALLINT NOT NULL` | Denial reason to apply on match |
| `description` | `TEXT` | Human-readable label for the rule (e.g., "Known malware TLD") |
| `active` | `BOOLEAN NOT NULL DEFAULT TRUE` | Disabled rules are skipped without deletion |
| `created_by` | `BIGINT REFERENCES users(id)` | Admin who created the rule |
| `created_at` | `TIMESTAMPTZ` | |

Rules are cached in memory with a 60-second TTL (`internal/cache/`) so every link creation does not hit the database. Cache is invalidated immediately when a rule is created, updated, or deleted.

---

### Filter Evaluation at Link Creation

The URL filter check runs **before** the deduplication check, so a re-submitted blocked URL is re-evaluated against current rules rather than reactivated.

```
POST /api/links
  │
  ├─ 1. Load active filter rules (from cache or DB)
  ├─ 2. Test destination_url against each rule in order
  │       First match → denied_reason = rule.reason_code
  │
  ├─ [denied] ──→ INSERT link (active=false, denied_reason=<code>)
  │               Write audit log: link.denied
  │               Return 422 {error: "url_denied", reason: <code>, label: <label>}
  │
  └─ [not denied] ──→ Deduplication check → insert or reactivate (normal flow)
```

Denied links are stored so they appear in the admin audit log. Each submission of a blocked URL creates a new denied row — this is intentional, giving the admin a count of how many times a blocked URL was attempted.

---

## Authentication — Passkeys (WebAuthn)

ShortLinks uses passkeys exclusively. There are no passwords. Email is used only to establish identity during account creation and as a recovery channel — it is never used to log in.

WebAuthn requires HTTPS, which is already satisfied by the Let's Encrypt setup on `go.sstools.co`. The relying party (RP) ID is `go.sstools.co`.

**Go library:** `github.com/go-webauthn/webauthn`

---

### Account Creation Flow

```mermaid
sequenceDiagram
    actor User
    participant Browser
    participant Server

    User->>Browser: Enter email address
    Browser->>Server: POST /auth/register/start
    Server->>Server: Validate email not already registered
    Server->>Server: Create pending_registrations row (5 min TTL)
    Server->>User: Send verification email with magic link
    Server-->>Browser: 200 "Check your email"

    User->>Browser: Click magic link
    Browser->>Server: GET /auth/register/verify?token=...
    Server->>Server: Validate token (5 min TTL)
    Server->>Server: Issue WebAuthn registration challenge
    Server-->>Browser: WebAuthn PublicKeyCredentialCreationOptions

    Browser->>User: Device passkey prompt (Touch ID / Face ID / etc.)
    User->>Browser: Authenticate with biometric

    Browser->>Server: POST /auth/register/finish (attestation response)
    Server->>Server: Verify challenge
    Server->>Server: Store credential ID + public key
    Server->>Server: Create users row
    Server->>Server: Delete pending_registrations row
    Server-->>Browser: 200 + Set-Cookie (session)
    Browser->>User: Redirect to dashboard
```

**Registration credential parameters:**
- `residentKey: "required"` and `userVerification: "required"` — these ensure a true discoverable passkey (stored in the authenticator, no username needed at login)
- `authenticatorAttachment` is intentionally **omitted** — this lets the browser and OS choose the authenticator. On Apple devices (macOS, iOS, iPadOS), the platform will default to iCloud Keychain, which syncs the passkey across all of the user's Apple devices automatically. On other platforms, Windows Hello or Android screen lock will be used.
- `pubKeyCredParams`: ES256 (`-7`) preferred, RS256 (`-257`) also accepted
- The `user.id` is a random 16-byte value (not the email), per WebAuthn best practice

**iCloud Keychain sync:** When a user registers a passkey on a Mac, the private key is stored in iCloud Keychain and silently synced to all other Apple devices signed into the same Apple ID (iPhone, iPad, other Macs). A single registration covers the user's entire Apple ecosystem — there is no need to register each Apple device individually.

---

### Authentication Flow

```mermaid
sequenceDiagram
    actor User
    participant Browser
    participant Server

    User->>Browser: Visit /login
    Browser->>Server: GET /auth/login/start
    Server->>Server: Issue WebAuthn challenge
    Server-->>Browser: PublicKeyCredentialRequestOptions (+ allowCredentials if email provided)

    Browser->>User: Device passkey prompt (conditional UI / autofill)
    User->>Browser: Authenticate with biometric

    Browser->>Server: POST /auth/login/finish (assertion response)
    Server->>Server: Find credential by ID
    Server->>Server: Verify signature against public key
    Server->>Server: Verify challenge consumed
    Server->>Server: Update sign_count
    Server-->>Browser: 200 + Set-Cookie (session)
    Browser->>User: Redirect to dashboard
```

**Conditional UI (passkey autofill):** The login page's email input uses `autocomplete="username webauthn"`. Modern browsers (Safari, Chrome, Firefox) will surface available passkeys inline in the autofill dropdown, so users can authenticate without manually clicking a "sign in" button.

If the user types their email first, the server can return an `allowCredentials` list filtered to that account, which narrows the device prompt to only matching passkeys.

**Sign count:** The stored `sign_count` is checked on each login. However, iCloud Keychain and other synced passkey implementations always return `sign_count = 0` — Apple deliberately does this because the credential exists on multiple devices and a per-device counter would fire false positives. The server must handle this:

- If the stored `sign_count` is `0` and the assertion returns `0`: accept without warning (synced passkey behavior)
- If the stored `sign_count` is greater than `0` and the assertion returns a count less than or equal to the stored value: log a warning (possible clone of a device-bound credential)

In practice, all Apple platform passkeys will follow the first case. The second case applies only to hardware security keys and device-bound (non-synced) platform credentials.

---

### Account Recovery

**iCloud Keychain significantly reduces recovery scenarios.** Because the passkey syncs to every Apple device on the same Apple ID, losing a single device (or getting a new Mac) does not require recovery — the passkey is already present on the user's other devices and will appear on a new device as soon as iCloud Keychain syncs.

Recovery is needed only when the user has genuinely lost access to all of their registered credentials: all Apple devices are lost/unavailable, iCloud Keychain is inaccessible, and no backup passkey (e.g., hardware key) was registered.

In that case the flow is:

1. User visits `/auth/recover` and submits their email
2. Server sends a single-use recovery link (15-minute TTL)
3. User clicks the link and is prompted to register a new passkey (same ceremony as initial registration)
4. New credential is added to their existing account; old credentials are left in place
5. User can then review and revoke old credentials from account settings

This flow does not remove existing passkeys automatically — the user decides which to keep.

---

### Multiple Passkeys

The recommended setup for most users is:

| Credential | Covers | Purpose |
|------------|--------|---------|
| iCloud Keychain passkey | All Apple devices on the same Apple ID | Primary — one registration covers Mac, iPhone, iPad |
| Hardware security key (optional) | Any device with a USB-A/C or NFC port | Backup for non-Apple devices or iCloud unavailability |

Because iCloud Keychain syncs the private key, registering once on any Apple device is sufficient for the entire Apple ecosystem. A second registration is only recommended as a backup for users who also work from non-Apple devices or want a hardware key fallback.

Each distinct registration is an independent row in `passkey_credentials`. From account settings the user can:

- View all registered passkeys (name, device hint from AAGUID, last used)
- Rename a credential (e.g., "iCloud Keychain", "YubiKey 5C")
- Revoke a credential (cannot revoke the last one without adding a replacement first)

---

### Database Schema — Auth Tables

#### `users`

| Column | Type | Notes |
|--------|------|-------|
| `id` | `BIGSERIAL PRIMARY KEY` | |
| `email` | `TEXT UNIQUE NOT NULL` | Lowercased on insert |
| `is_admin` | `BOOLEAN NOT NULL DEFAULT FALSE` | Grants access to admin routes and settings |
| `active` | `BOOLEAN NOT NULL DEFAULT TRUE` | `false` = account deactivated; login and API access blocked |
| `created_at` | `TIMESTAMPTZ` | |
| `last_login_at` | `TIMESTAMPTZ` | Updated on each successful auth |

The first user registered on a fresh installation is automatically promoted to admin (`is_admin = true`). Subsequent registrations are normal users. Login checks `active = true` before completing the WebAuthn assertion; a deactivated user receives `403 Account deactivated`.

#### `passkey_credentials`

| Column | Type | Notes |
|--------|------|-------|
| `id` | `BIGSERIAL PRIMARY KEY` | |
| `user_id` | `BIGINT REFERENCES users(id)` | |
| `credential_id` | `BYTEA UNIQUE NOT NULL` | Raw bytes from WebAuthn |
| `public_key` | `BYTEA NOT NULL` | COSE-encoded public key |
| `aaguid` | `UUID` | Authenticator model identifier |
| `sign_count` | `BIGINT NOT NULL DEFAULT 0` | Clone detection for device-bound credentials; always 0 for iCloud Keychain and other synced passkeys |
| `device_name` | `TEXT` | User-assigned label (e.g., "MacBook Touch ID") |
| `created_at` | `TIMESTAMPTZ` | |
| `last_used_at` | `TIMESTAMPTZ` | |

#### `webauthn_challenges`

Ephemeral — rows are deleted after use or expiry.

| Column | Type | Notes |
|--------|------|-------|
| `id` | `BIGSERIAL PRIMARY KEY` | |
| `challenge` | `BYTEA UNIQUE NOT NULL` | 32 random bytes |
| `user_id` | `BIGINT REFERENCES users(id)` | NULL during registration (pre-account) |
| `pending_registration_token` | `TEXT REFERENCES pending_registrations(token)` | Set for `registration` purpose; links challenge back to the email being registered |
| `purpose` | `TEXT` | `registration` or `authentication` |
| `expires_at` | `TIMESTAMPTZ` | 5-minute TTL |

#### `pending_registrations`

| Column | Type | Notes |
|--------|------|-------|
| `id` | `BIGSERIAL PRIMARY KEY` | |
| `email` | `TEXT NOT NULL` | |
| `token` | `TEXT UNIQUE NOT NULL` | Cryptographically random, URL-safe |
| `expires_at` | `TIMESTAMPTZ` | 5-minute TTL from send time |

#### `sessions`

| Column | Type | Notes |
|--------|------|-------|
| `id` | `BIGSERIAL PRIMARY KEY` | |
| `user_id` | `BIGINT REFERENCES users(id)` | |
| `token` | `TEXT UNIQUE NOT NULL` | Random 32-byte, stored as `HttpOnly; Secure; SameSite=Strict` cookie |
| `created_at` | `TIMESTAMPTZ` | |
| `expires_at` | `TIMESTAMPTZ` | 30-day sliding window |
| `last_seen_at` | `TIMESTAMPTZ` | Updated on each authenticated request |

#### `settings`

Runtime configuration values that can be changed without restarting the server. Managed by the admin via the Account/Admin view.

| Column | Type | Notes |
|--------|------|-------|
| `key` | `TEXT PRIMARY KEY` | Setting identifier |
| `value` | `TEXT NOT NULL` | Value serialized as text |
| `updated_at` | `TIMESTAMPTZ` | |

Rows seeded by the initial migration:

| Key | Default | Description |
|-----|---------|-------------|
| `registrations_enabled` | `false` | When `true`, the registration form accepts new users; when `false`, `POST /auth/register/start` returns `403` |

#### `audit_log`

Append-only record of every significant action in the system. Rows are never updated or deleted.

| Column | Type | Notes |
|--------|------|-------|
| `id` | `BIGSERIAL PRIMARY KEY` | |
| `actor_id` | `BIGINT REFERENCES users(id)` | Who performed the action; NULL for pre-auth events (e.g., registration started) |
| `user_id` | `BIGINT REFERENCES users(id)` | Whose account or resource was affected; may differ from `actor_id` for admin actions |
| `action` | `TEXT NOT NULL` | Event type — see action catalogue below |
| `target_type` | `TEXT` | Entity kind affected: `link`, `user`, `credential`, `settings`, `url_filter` |
| `target_id` | `BIGINT` | ID of the affected entity (nullable) |
| `metadata` | `JSONB` | Action-specific context (see below) |
| `ip_address` | `INET` | Request IP of the actor |
| `created_at` | `TIMESTAMPTZ NOT NULL DEFAULT now()` | |

**Action catalogue:**

| Action | Actor | Notes |
|--------|-------|-------|
| `account.registration_started` | NULL | Email submitted; pending registration created |
| `account.registered` | new user | Passkey ceremony complete; account created |
| `account.login` | user | Successful WebAuthn assertion |
| `account.logout` | user | Session invalidated |
| `account.recovery_started` | NULL | Recovery email requested |
| `account.recovered` | user | New credential added via recovery flow |
| `account.deactivated` | admin | Admin deactivated a user account |
| `account.reactivated` | admin | Admin reactivated a user account |
| `credential.added` | user | Passkey registered |
| `credential.revoked` | user | Passkey revoked |
| `link.created` | user | New short link created |
| `link.deactivated` | user | Link deactivated |
| `link.reactivated` | user | Inactive link reactivated (dedup flow or manual) |
| `link.denied` | user | Link creation blocked by a URL filter rule |
| `url_filter.created` | admin | New filter rule added |
| `url_filter.updated` | admin | Filter rule modified |
| `url_filter.deleted` | admin | Filter rule removed |
| `settings.updated` | admin | Admin changed a server setting |

**Metadata shapes by action:**

| Action | Metadata fields |
|--------|----------------|
| `link.created` | `{key, destination_url, title, duplicate}` |
| `link.deactivated` | `{key, destination_url}` |
| `link.reactivated` | `{key, destination_url}` |
| `account.deactivated` | `{reason, note}` — see deactivation reasons below |
| `account.reactivated` | `{note}` |
| `settings.updated` | `{key, old_value, new_value}` |
| `link.denied` | `{destination_url, reason_code, reason_label, matched_rule_id}` |
| `url_filter.created` | `{pattern, reason_code, description}` |
| `url_filter.updated` | `{old_pattern, new_pattern, old_reason_code, new_reason_code}` |
| `url_filter.deleted` | `{pattern, reason_code, description}` |
| `credential.added` | `{device_name, aaguid}` |
| `credential.revoked` | `{device_name}` |

---

### User Deactivation

An admin can deactivate any non-admin user account from the Admin view. Deactivation:

1. Sets `users.active = false`
2. Deletes all active sessions for that user (forcing immediate logout on next request)
3. Writes an `account.deactivated` audit log entry with the selected reason and optional note
4. The user's links remain in the database but the user cannot log in or manage them

Reactivation reverses steps 1–3 (sets `active = true`, writes `account.reactivated`). Existing links are unaffected.

**Deactivation reasons (dropdown, stored in `metadata.reason`):**

| Value | Label |
|-------|-------|
| `malware_distribution` | Malware or ransomware distribution |
| `phishing` | Phishing |
| `spam` | Spam |
| `harassment` | Harassment |
| `terms_violation` | Terms of service violation |
| `other` | Other |

The free-text `note` field is optional for all reasons except `other`, where it is required.

---

### Registration Settings

Whether new users can register is controlled by the `registrations_enabled` setting in the `settings` table. The default is `false` — the server is locked to existing users until an admin explicitly opens registration.

| State | Behaviour |
|-------|-----------|
| `false` (default) | `POST /auth/register/start` returns `403 Registration closed` |
| `true` | Registration proceeds normally |

The admin toggles this from the Admin view in the Svelte SPA. The setting is read from the DB on each registration attempt (not cached) so changes take effect immediately without a server restart.

**Domain configuration:** The WebAuthn relying party ID and origin are derived from `WEBAUTHN_RP_ID` and `WEBAUTHN_RP_ORIGIN` in `.env`, making the service portable to any domain without a code change. See the Configuration section.

---

### Auth HTTP Routes

| Method | Path | Description |
|--------|------|-------------|
| `POST` | `/auth/register/start` | Submit email; checks `registrations_enabled`; sends verification email |
| `GET` | `/auth/register/verify` | Validate magic-link token; return WebAuthn options |
| `POST` | `/auth/register/finish` | Submit attestation; create account + credential |
| `GET` | `/auth/login/start` | Issue challenge (optionally filtered by email) |
| `POST` | `/auth/login/finish` | Submit assertion; issue session |
| `POST` | `/auth/logout` | Invalidate session |
| `POST` | `/auth/recover` | Send recovery email |
| `GET` | `/auth/recover/verify` | Validate recovery token; return WebAuthn options |
| `POST` | `/auth/recover/finish` | Submit new credential; attach to existing account |
| `GET` | `/account/credentials` | List user's registered passkeys |
| `PATCH` | `/account/credentials/{id}` | Rename a passkey |
| `DELETE` | `/account/credentials/{id}` | Revoke a passkey |
| `GET` | `/admin/settings` | Get all settings (admin only) |
| `PATCH` | `/admin/settings` | Update one or more settings (admin only) |
| `GET` | `/admin/users` | List all users with status and last-login (admin only) |
| `GET` | `/admin/users/{id}` | User detail (admin only) |
| `POST` | `/admin/users/{id}/deactivate` | Deactivate user — body: `{reason, note}` (admin only) |
| `POST` | `/admin/users/{id}/reactivate` | Reactivate user — body: `{note}` (admin only) |
| `GET` | `/admin/audit` | Paginated audit log; optional `?user_id=` filter (admin only) |
| `GET` | `/admin/url-filters` | List all URL filter rules (admin only) |
| `POST` | `/admin/url-filters` | Create a filter rule — body: `{pattern, reason_code, description}` (admin only) |
| `PATCH` | `/admin/url-filters/{id}` | Update a filter rule (admin only) |
| `DELETE` | `/admin/url-filters/{id}` | Delete a filter rule (admin only) |
| `POST` | `/admin/url-filters/test` | Test a URL against all active rules; returns matching rule or `no_match` (admin only) |

---

### Session Security

- Session token is a cryptographically random 32-byte value stored as a `HttpOnly; Secure; SameSite=Strict` cookie
- Sessions expire after 30 days of inactivity; `last_seen_at` is updated on authenticated requests
- On logout, the session row is deleted immediately
- A background job (or on-demand sweep) purges expired rows from `webauthn_challenges` and `pending_registrations`

---

## Go Service Architecture

```
cmd/
  shortlinks/       # main package — wires everything, reads config, starts server
internal/
  config/           # load and validate .env / environment variables
  db/               # PostgreSQL connection pool, migrations
  cache/            # ristretto-backed redirect cache (key → link row) and filter rule cache
  auth/             # WebAuthn registration/authentication, session management
  links/            # domain logic: create link, resolve key, deactivate, update
  filters/          # URL filter rule loading, regex evaluation, denial reason codes
  clicks/           # record click events
  audit/            # write audit log entries
  events/           # SSE broker — pub/sub between link creation and open streams
  handlers/         # HTTP handlers (redirect, API, SSE, admin, static files)
  middleware/        # logging, recovery, session auth guard, rate limiter
migrations/         # SQL migration files (up/down)
web/                # Svelte SPA (Vite project)
  src/
    lib/            # shared stores, API client, types
    components/     # reusable Svelte components
    views/          # top-level view components (Dashboard, LinkDetail, Account, Login)
    App.svelte      # root component — renders active view from store
    main.ts         # Vite entry point
  dist/             # build output — embedded into Go binary at compile time
  vite.config.ts
  package.json
```

### Key HTTP Routes

| Method | Path | Description |
|--------|------|-------------|
| `GET` | `/u/{key}` | Resolve key → redirect to destination |
| `POST` | `/api/links` | Create a short link; `422` if blocked by filter rule; returns existing link with `duplicate: true` if URL already exists (non-denied) for this user |
| `GET` | `/api/links` | List all links (paginated) |
| `GET` | `/api/links/{key}` | Get link detail + click stats |
| `PATCH` | `/api/links/{key}` | Update link title, destination URL, or expiry |
| `DELETE` | `/api/links/{key}` | Deactivate a link |
| `GET` | `/api/me` | Current user profile: `{id, email, is_admin}` — used by Svelte to gate admin view |
| `GET` | `/api/events` | SSE stream — pushes `link.created` events to connected clients |
| `GET` | `/health` | Health check for monitoring |
| `GET` | `/*` | Serve Svelte SPA (`index.html` + hashed assets from embedded `web/dist`) |

### SSE Event Broker (`internal/events/`)

The broker is a singleton created in `main` and injected into both the link handler (publisher) and the SSE handler (subscriber). It is safe for concurrent use.

```
type Event struct {
    Name    string  // "link.created"
    Payload []byte  // JSON-encoded link row
}

type Broker struct {
    mu          sync.RWMutex
    subscribers map[int64][]chan Event  // keyed by userID
}

func (b *Broker) Subscribe(userID int64) chan Event
func (b *Broker) Unsubscribe(userID int64, ch chan Event)
func (b *Broker) Publish(userID int64, event Event)
```

Flow: `POST /api/links` succeeds → link handler calls `broker.Publish(userID, event)` → broker fans out to every open channel for that user → each SSE handler goroutine receives the event, writes the SSE frame, and flushes.

Disconnected clients are detected via `r.Context().Done()` in the SSE handler, which calls `Unsubscribe` and exits the goroutine cleanly.

---

All API routes (except `/u/{key}`, `/health`, `/auth/*`, and static assets) require a valid session cookie. The session middleware checks the cookie, loads the user from the `sessions` table, and attaches the user to the request context. Unauthenticated requests to protected routes receive `401`.

---

## Link Creation Behavior

### Deduplication

URL filter evaluation runs first (see URL Filtering). If the URL passes all filter rules, the server checks whether the authenticated user has any existing non-denied link pointing to the same `destination_url`:

```sql
SELECT * FROM links
WHERE user_id = $1
  AND destination_url = $2
  AND denied_reason = 0
LIMIT 1;
```

| Result | Action |
|--------|--------|
| Active link found | Return existing link with `"duplicate": true`; no insert; no SSE event |
| Inactive link found | Set `active = true`; return reactivated link with `"duplicate": true`; broadcast `link.created` SSE event |
| No row found | Insert new link; return with `"duplicate": false`; broadcast `link.created` SSE event |

The API response shape is identical in all cases — the caller receives a complete link object. The `duplicate` field allows the Svelte dashboard to show a contextual notice ("This URL was already shortened — here's your existing link") without special-casing the rest of the flow. The SSE broadcast on reactivation ensures the link reappears in the dashboard list if it had been removed.

**Custom aliases are not deduplicated.** If the user supplies a specific key, the server attempts the insert with that key regardless of whether another link already targets the same destination URL.

### Key Generation

- Default: 6-character base-62 random string (`[a-z0-9A-Z]`)
- Collision check on `key` uniqueness; retry up to 5 times before returning an error
- Custom aliases (user-supplied key) accepted if the key is not already taken

---

## Redirect Behavior

1. Check redirect cache for key
2. On cache miss: query `links` table; populate cache; cache negative entry on DB miss
3. If key not found or `active = false`: return `404`
4. If `expires_at` is set and in the past: return `410 Gone`
5. Log click (async, non-blocking) with UTM params and request metadata
6. Merge inbound UTM params onto destination URL
7. Return `302 Found` with `Location` header

---

## Apache Configuration

A new virtual host for `go.sstools.co` will proxy to the Go service:

```apache
<VirtualHost *:443>
    ServerName go.sstools.co

    ProxyPreserveHost On

    # SSE requires response buffering disabled so events flush immediately
    ProxyPass /api/events http://127.0.0.1:8080/api/events flushpackets=on
    ProxyPassReverse /api/events http://127.0.0.1:8080/api/events

    ProxyPass / http://127.0.0.1:8080/
    ProxyPassReverse / http://127.0.0.1:8080/

    SSLEngine on
    SSLCertificateFile    /etc/letsencrypt/live/go.sstools.co/fullchain.pem
    SSLCertificateKeyFile /etc/letsencrypt/live/go.sstools.co/privkey.pem
</VirtualHost>
```

---

## Configuration

All runtime configuration is loaded from environment variables. In development, these are read from a `.env` file in the project root via `godotenv`. In production, they are set in `/etc/shortlinks/config.env` loaded by the systemd unit.

**`.env` is never committed to source control.** The repository includes `.env.example` with every variable defined but no real values. `.env` is listed in `.gitignore`.

### `.env.example`

```env
# ── Server ────────────────────────────────────────────────────────────────────
PORT=8080
BASE_URL=https://go.sstools.co

# ── Database ──────────────────────────────────────────────────────────────────
DATABASE_URL=postgres://user:password@localhost:5432/shortlinks?sslmode=disable

# ── WebAuthn (configurable per deployment domain) ─────────────────────────────
WEBAUTHN_RP_ID=go.sstools.co
WEBAUTHN_RP_ORIGIN=https://go.sstools.co

# ── Sessions ──────────────────────────────────────────────────────────────────
# Generate with: openssl rand -hex 32
SESSION_SECRET=replace_with_random_64_char_hex_string

# ── Email (AWS SES SMTP) ──────────────────────────────────────────────────────
SES_SMTP_HOST=email-smtp.us-east-1.amazonaws.com
SES_SMTP_PORT=587
SES_SMTP_USERNAME=
SES_SMTP_PASSWORD=
EMAIL_FROM=ShortLinks <noreply@sstools.co>

# ── Cache ─────────────────────────────────────────────────────────────────────
CACHE_MAX_COST=10000
CACHE_TTL_SECONDS=300

# ── Bootstrap ─────────────────────────────────────────────────────────────────
# Email of the first admin user. On first startup with no users in the DB,
# this address is pre-authorized as admin when it completes registration.
ADMIN_EMAIL=you@example.com
```

### Notes

- `WEBAUTHN_RP_ID` must exactly match the domain the browser is on. Anyone cloning the repo to run their own server sets this to their own domain — no code change required.
- `SESSION_SECRET` is used to HMAC-sign the session token before writing it to the cookie, so the server can reject tampered cookies before doing a DB lookup. Must never appear in source control, logs, or LLM interactions. Rotating it invalidates all active sessions.
- `SES_SMTP_PASSWORD` is the IAM-derived SMTP credential from SES, not an AWS root key.
- `ADMIN_EMAIL` is used only once: when the first user registers, the server checks whether their email matches this value and sets `is_admin = true`. It has no effect after the first admin exists.

---

## Deployment

1. Build Svelte app (`cd web && npm run build`) — outputs to `web/dist/`
2. Build Go binary (`go build ./cmd/shortlinks`) — the binary embeds `web/dist` via `//go:embed`
3. Install as a systemd service (`/etc/systemd/system/shortlinks.service`)
4. Service reads config from `/etc/shortlinks/config.env` (DB DSN, port, SES credentials)
5. Apache virtual host enabled via `a2ensite`
6. Database migrations run via `golang-migrate/migrate` — files in `migrations/` named `NNNN_description.{up,down}.sql`; run with `migrate -path migrations -database $DATABASE_URL up`

---

## Frontend — Svelte SPA

### Stack

| Tool | Role |
|------|------|
| Svelte 5 | Component framework |
| Vite | Dev server and build tool |
| TypeScript | Type safety |
| `//go:embed` | Embed `web/dist` into the Go binary at compile time |

No client-side router is used. A single `currentView` Svelte store holds a string (`"login"` \| `"dashboard"` \| `"link-detail"` \| `"account"` \| `"admin"`) and `App.svelte` renders the matching view component. Navigating between sections is a store write, not a URL change. On load, `GET /api/me` determines whether the admin tab is shown.

### Views

| View | Description |
|------|-------------|
| `Login` | Email input with `autocomplete="username webauthn"` for passkey autofill; triggers WebAuthn ceremony |
| `Dashboard` | Create link form + live list of recent links |
| `LinkDetail` | Click stats, UTM breakdown, copy short URL, deactivate |
| `Account` | List, rename, and revoke registered passkeys |
| `Admin` | Toggle `registrations_enabled`; view registered users (admin only; hidden from non-admins) |

### Real-Time Link Updates (SSE)

When a link is created, the dashboard link list updates immediately without polling. The mechanism is a Server-Sent Events stream.

**Server (`GET /api/events`):**
- Requires a valid session cookie
- Keeps the connection open; writes newline-delimited SSE frames
- On each `POST /api/links` that succeeds, broadcasts a `link.created` event containing the new link as JSON to all connected clients for that user
- Go's `http.Flusher` interface flushes each event to the wire immediately

**Client (`web/src/lib/events.ts`):**
- Opens `new EventSource('/api/events')` on mount of the Dashboard view
- Listens for `link.created` messages and prepends the new link to the Svelte `links` store
- Closes the connection when the Dashboard view is unmounted

```
Server                          Svelte Dashboard
  |                                    |
  |<-- GET /api/events (SSE) ----------|
  |-- event: link.created ------------>|
  |   data: {"key":"8d0d93",...}       |-- prepend to links store
  |                                    |   (list updates instantly)
  |-- event: link.created ------------>|
  |   data: {"key":"f3a12b",...}       |-- prepend to links store
```

### Development Workflow

During development, Vite runs on `localhost:5173` and proxies all `/api`, `/auth`, and `/u` requests to the Go service on `localhost:8080`. No CORS configuration is needed because the proxy makes them same-origin from the browser's perspective.

```typescript
// vite.config.ts
server: {
  proxy: {
    '/api': 'http://localhost:8080',
    '/auth': 'http://localhost:8080',
    '/u': 'http://localhost:8080',
  }
}
```

For production, `npm run build` outputs hashed static assets to `web/dist/`. The Go binary embeds this directory and serves it. The catch-all `GET /*` handler returns `index.html` for any path not matched by an API or redirect route, which is what makes the SPA work after a hard refresh.

---

## Open Questions / Future Work

- **QR code generation** — per-link QR codes surfaced in the dashboard
- **Click deduplication** — suppress repeated clicks from the same IP within a window
- **Custom domains** — allow links under additional domains beyond `go.sstools.co`
- **Bulk import** — CSV upload to create many links at once
- **Expiry notifications** — alert before a link expires
- **External malware scanning** — nightly/weekly job to re-evaluate existing active links against a malware intelligence service (e.g., Google Safe Browsing, VirusTotal); auto-deny and notify admin on hit
- **LLM-based URL evaluation** — use a Claude API call to evaluate the content and context of a destination URL for signs of phishing or malware at link creation time; apply `denied_reason` automatically on a positive signal

---

## Phased Build Plan

### Phase 1 — Core redirect service
- `.env.example` and config loader (`internal/config/`)
- PostgreSQL schema: `users`, `links`, `clicks` (in migration order)
- Ristretto redirect cache (`internal/cache/`)
- Key generation and link creation
- Redirect handler with cache lookup and click logging
- Health endpoint
- Seed command (`go run ./cmd/shortlinks seed`) — creates the admin user from `ADMIN_EMAIL` and a test link pointing to `https://www.wikipedia.org` with a generated key; safe to re-run (idempotent)
- systemd + Apache config

### Phase 2 — Passkey authentication + rate limiting
- DB schema: `passkey_credentials`, `webauthn_challenges`, `pending_registrations`, `sessions`, `settings`
- `settings` seeded with `registrations_enabled = false`
- First-admin promotion logic (matches `ADMIN_EMAIL` on first registration)
- iCloud Keychain-compatible WebAuthn registration ceremony (`/auth/register/*`)
- WebAuthn authentication ceremony with conditional UI (`/auth/login/*`)
- Session middleware guarding API and management routes
- Account recovery via email re-verification
- Credential management routes (list, rename, revoke)
- Per-IP rate limiting via `golang.org/x/time/rate`:
  - `POST /auth/register/start`: 3 requests / hour / IP
  - `POST /auth/login/start`: 10 requests / minute / IP
  - `POST /auth/recover`: 3 requests / hour / IP

### Phase 3 — API, audit log, URL filtering, and user management
- REST API for link CRUD, protected by session auth
- Per-user URL deduplication on `POST /api/links` (`duplicate` field in response)
- URL filter rules: `url_filter_rules` schema, regex evaluation at link creation, denial reason codes, 60-second rule cache
- `POST /admin/url-filters/test` endpoint for admin rule testing
- SSE broker (`internal/events/`) and `GET /api/events` endpoint
- `audit_log` DB schema and write path (every action in the catalogue emits an entry, including `link.denied`)
- Admin routes: settings, URL filter CRUD, user list, user detail, deactivate/reactivate user
- `GET /admin/audit` with `?user_id=` filter
- Session invalidation on account deactivation
- Pagination and basic stats (click counts per link)

### Phase 4 — UTM analytics
- Store and query UTM breakdown per link
- Aggregate click stats by source/medium/campaign

### Phase 5 — Svelte SPA
- Vite + Svelte project under `web/`
- Built `web/dist/` embedded in Go binary via `//go:embed`; served from `/*`
- Single-page app — no URL changes; active view driven by a Svelte writable store
- Views: Login, Dashboard, Link Detail / Stats, Account (passkey management), Admin
- Dashboard: link creation form + live link list updated via SSE
- Login page uses passkey autofill (`autocomplete="username webauthn"`)
- Admin view: toggle `registrations_enabled`; URL filter rule CRUD with test tool; user list with active/inactive status; deactivate/reactivate user with reason dropdown and note field; full audit log with per-user filter
