# Database

ShortLinks uses PostgreSQL. The schema is managed exclusively by
[golang-migrate](https://github.com/golang-migrate/migrate); every table, index,
and constraint lives in a numbered migration file. No DDL is embedded in Go code.

---

## Migration workflow

### File convention

Migration files live in `migrations/` and follow the pattern:

```
NNNN_description.up.sql    # forward (apply)
NNNN_description.down.sql  # reverse (undo)
```

`NNNN` is a zero-padded six-digit sequence number. golang-migrate applies files
in ascending numeric order and tracks the current version in a
`schema_migrations` table it manages automatically.

### Applying migrations

```bash
export DATABASE_URL='postgres://shortlinks:<password>@localhost:5432/shortlinks?sslmode=disable'
migrate -path migrations -database "$DATABASE_URL" up
```

Run this on every deploy that introduced new migration files. To check the
currently applied version without changing anything:

```bash
migrate -path migrations -database "$DATABASE_URL" version
```

### Checking whether a deploy needs a migration

A migration is only required when a new `migrations/NNNN_*.{up,down}.sql` pair
was added. Pure Go or front-end changes never need one:

```bash
git diff --name-only <last-deployed-sha>..HEAD -- migrations/
# any output => run migrate up before restarting the service
```

See `DEPLOYMENT.md` section 5 ("Migrations") and the "Is a migration needed this
deploy?" note in the Updating section for the full deploy checklist.

### Rolling back

```bash
migrate -path migrations -database "$DATABASE_URL" down 1   # undo one version
```

### Adding a migration

1. Choose the next sequence number (`N = highest existing NNNN + 1`).
2. Create `migrations/0000NN_short_description.up.sql` with forward DDL.
3. Create `migrations/0000NN_short_description.down.sql` that exactly reverses it.
4. Apply with `migrate ... up` in development, commit both files together.

---

## Tables

### `users` — `000001_create_users.up.sql`

Account records. Email is used for identity and recovery only — users never log
in with a password. The first user to complete registration on a fresh install is
promoted to admin by the service layer; subsequent users with the `ADMIN_EMAIL`
address are auto-promoted on creation.

| Column | Type | Notes |
|---|---|---|
| `id` | `BIGSERIAL` | Primary key |
| `email` | `TEXT` | Unique, not null. UNIQUE constraint doubles as the lookup index. |
| `is_admin` | `BOOLEAN` | Default `false`. Governs admin-only routes. |
| `active` | `BOOLEAN` | Default `true`. Inactive users cannot authenticate. |
| `created_at` | `TIMESTAMPTZ` | Set to `now()` on insert. |
| `last_login_at` | `TIMESTAMPTZ` | Nullable; updated after each successful login. |

**Relationships:** Referenced by `links`, `clicks` (via `links`), `sessions`,
`passkey_credentials`, `webauthn_challenges`, `audit_log`, and `url_filter_rules`.

---

### `links` — `000002_create_links.up.sql`

Short codes that map a key to a destination URL, each owned by one user.
`active` and `denied_reason` together encode the effective link state as
described in the PRD.

| Column | Type | Notes |
|---|---|---|
| `id` | `BIGSERIAL` | Primary key |
| `user_id` | `BIGINT` | FK → `users(id)`, not null |
| `key` | `VARCHAR(12)` | Unique short code used in redirect URLs (`/u/{key}`). UNIQUE constraint is the redirect-lookup index. |
| `destination_url` | `TEXT` | Full destination URL, not null |
| `title` | `TEXT` | Optional display title |
| `created_at` | `TIMESTAMPTZ` | Default `now()` |
| `expires_at` | `TIMESTAMPTZ` | Nullable; links past this timestamp are treated as inactive |
| `active` | `BOOLEAN` | Default `true` |
| `denied_reason` | `SMALLINT` | Default `0` (not denied). Non-zero codes correspond to URL filter rule reason codes. |

**Indexes:**

| Index | Purpose |
|---|---|
| `idx_links_user_id` | List all links owned by a user |
| `idx_links_user_destination` | Per-user dedup lookup — partial, `WHERE denied_reason = 0` |
| `idx_links_denied_reason` | Admin query for all denied links — partial, `WHERE denied_reason > 0` |

---

### `clicks` — `000003_create_clicks.up.sql`

One row per redirect, capturing request metadata and any inbound UTM parameters
for analytics. `link_id` is nullable; the FK does not cascade-delete so click
history is preserved if a link is deleted.

| Column | Type | Notes |
|---|---|---|
| `id` | `BIGSERIAL` | Primary key |
| `link_id` | `BIGINT` | FK → `links(id)`, nullable |
| `clicked_at` | `TIMESTAMPTZ` | Default `now()` |
| `ip_address` | `INET` | Nullable |
| `user_agent` | `TEXT` | Nullable |
| `referer` | `TEXT` | Nullable |
| `utm_source` | `TEXT` | Nullable |
| `utm_medium` | `TEXT` | Nullable |
| `utm_campaign` | `TEXT` | Nullable |
| `utm_term` | `TEXT` | Nullable |
| `utm_content` | `TEXT` | Nullable |

**Indexes:**

| Index | Purpose |
|---|---|
| `idx_clicks_link_id` | Aggregate click counts per link |
| `idx_clicks_clicked_at` | Time-range analytics queries |

---

### `pending_registrations` — `000004_create_auth_credentials.up.sql`

Short-lived rows tracking an email address mid-registration. Created before
the WebAuthn challenge because `webauthn_challenges` references this table's
`token` column. Expired rows are swept by a background process.

| Column | Type | Notes |
|---|---|---|
| `id` | `BIGSERIAL` | Primary key |
| `email` | `TEXT` | Not null |
| `token` | `TEXT` | Unique registration token embedded in the verification email link |
| `expires_at` | `TIMESTAMPTZ` | Nullable |

**Indexes:**

| Index | Purpose |
|---|---|
| `idx_pending_registrations_expires_at` | Background sweep of expired rows |

---

### `passkey_credentials` — `000004_create_auth_credentials.up.sql` + `000009_passkey_backup_flags.up.sql`

One row per registered WebAuthn (passkey) credential. Migration 4 creates the
table; migration 9 adds the `backup_eligible` and `backup_state` flag columns
required to correctly round-trip the WebAuthn Backup Eligible (BE) flag for
synced credentials such as iCloud Keychain passkeys.

| Column | Type | Notes |
|---|---|---|
| `id` | `BIGSERIAL` | Primary key |
| `user_id` | `BIGINT` | FK → `users(id)`, not null |
| `credential_id` | `BYTEA` | Unique WebAuthn credential identifier. UNIQUE constraint is the assertion-lookup index. |
| `public_key` | `BYTEA` | COSE public key, not null |
| `aaguid` | `UUID` | Authenticator AAGUID, nullable |
| `sign_count` | `BIGINT` | Default `0`. Incremented on each assertion to detect cloned authenticators. |
| `device_name` | `TEXT` | Optional user-visible label |
| `created_at` | `TIMESTAMPTZ` | Nullable |
| `last_used_at` | `TIMESTAMPTZ` | Nullable; updated after each successful assertion |
| `backup_eligible` | `BOOLEAN` | Default `false` (added migration 9). Must match the value presented at registration on every subsequent assertion. |
| `backup_state` | `BOOLEAN` | Default `false` (added migration 9). Current backup state of the credential. |

Migration 9 also backfills `backup_eligible = TRUE` for all existing rows,
because every credential enrolled in this deployment at that point was an Apple
iCloud Keychain synced passkey.

---

### `webauthn_challenges` — `000004_create_auth_credentials.up.sql`

Ephemeral challenges issued during registration and authentication ceremonies.
Rows are deleted after use or expiry; the table is not intended to accumulate
long-term data.

| Column | Type | Notes |
|---|---|---|
| `id` | `BIGSERIAL` | Primary key |
| `challenge` | `BYTEA` | Unique random challenge bytes |
| `user_id` | `BIGINT` | FK → `users(id)`, nullable (set for authentication, null for new registrations) |
| `pending_registration_token` | `TEXT` | FK → `pending_registrations(token)`, nullable |
| `purpose` | `TEXT` | Nullable; describes the ceremony type |
| `expires_at` | `TIMESTAMPTZ` | Nullable |

**Indexes:**

| Index | Purpose |
|---|---|
| `idx_webauthn_challenges_expires_at` | Background sweep of expired rows |

---

### `sessions` — `000005_create_sessions.up.sql`

Active authenticated sessions. The `token` value is issued as an
`HttpOnly; Secure; SameSite=Strict` cookie and looked up on every authenticated
request.

| Column | Type | Notes |
|---|---|---|
| `id` | `BIGSERIAL` | Primary key |
| `user_id` | `BIGINT` | FK → `users(id)`, not null |
| `token` | `TEXT` | Unique session token. UNIQUE constraint is the per-request lookup index. |
| `created_at` | `TIMESTAMPTZ` | Default `now()` |
| `expires_at` | `TIMESTAMPTZ` | Not null; sessions past this timestamp are rejected |
| `last_seen_at` | `TIMESTAMPTZ` | Not null; updated on activity |

---

### `settings` — `000006_create_settings.up.sql`

Runtime configuration values that can be changed without a service restart.
Currently one row is seeded by the migration itself.

| Column | Type | Notes |
|---|---|---|
| `key` | `TEXT` | Primary key |
| `value` | `TEXT` | Not null |
| `updated_at` | `TIMESTAMPTZ` | Nullable |

**Seeded row (from migration 6):**

| `key` | Default `value` | Meaning |
|---|---|---|
| `registrations_enabled` | `'false'` | Gate controlling whether new user registration is open. Defaults closed; admin toggles it via **Admin → Settings**. |

The migration inserts this row with `ON CONFLICT DO NOTHING` so re-running
migrations on an existing database does not overwrite an admin's setting.

---

### `audit_log` — `000007_create_audit_log.up.sql`

Append-only record of every significant action in the system. Rows are never
updated or deleted.

| Column | Type | Notes |
|---|---|---|
| `id` | `BIGSERIAL` | Primary key |
| `actor_id` | `BIGINT` | FK → `users(id)`, nullable. The user who performed the action. |
| `user_id` | `BIGINT` | FK → `users(id)`, nullable. The user the action was performed *on* (may equal `actor_id`). |
| `action` | `TEXT` | Event type string, not null (e.g. `account.deactivated`, `link.created`) |
| `target_type` | `TEXT` | Nullable; entity type the action targeted (e.g. `link`, `credential`) |
| `target_id` | `BIGINT` | Nullable; entity ID |
| `metadata` | `JSONB` | Nullable; arbitrary structured data for the event |
| `ip_address` | `INET` | Nullable; IP of the request that triggered the action |
| `created_at` | `TIMESTAMPTZ` | Default `now()` |

**Indexes:**

| Index | Purpose |
|---|---|
| `idx_audit_log_user_id_created_at` | Per-user audit history, newest-first |
| `idx_audit_log_created_at` | Full audit log, newest-first |
| `idx_audit_log_action` | Filter by event type |

---

### `url_filter_rules` — `000008_create_url_filter_rules.up.sql`

Admin-managed regex patterns evaluated in the Go service (not in PostgreSQL)
against each link's `destination_url` at creation time. Each rule maps a
Go-compatible regex to a denial `reason_code` (see PRD URL Filtering). Active
rules are cached in memory with a 60-second TTL and invalidated immediately on
any admin mutation.

| Column | Type | Notes |
|---|---|---|
| `id` | `BIGSERIAL` | Primary key |
| `pattern` | `TEXT` | Go-compatible regex, not null |
| `reason_code` | `SMALLINT` | Denial reason code written to `links.denied_reason`, not null |
| `description` | `TEXT` | Optional human-readable description |
| `active` | `BOOLEAN` | Default `true`. Inactive rules are ignored by the cache loader. |
| `created_by` | `BIGINT` | FK → `users(id)`, nullable |
| `created_at` | `TIMESTAMPTZ` | Default `now()` |

**Indexes:**

| Index | Purpose |
|---|---|
| `idx_url_filter_rules_active` | Load all active rules (cache refresh) |

---

## Seed command

```bash
shortlinks seed
```

The `seed` subcommand bootstraps a fresh install. It reads `ADMIN_EMAIL` from
the environment (same config loaded for `serve`) and performs two idempotent
operations:

1. **Admin user** — inserts a `users` row for `ADMIN_EMAIL` with
   `is_admin = TRUE` and `active = TRUE` using `ON CONFLICT (email) DO NOTHING`.
   Regardless of whether the row was just inserted or already existed, it then
   runs `UPDATE users SET is_admin = TRUE, active = TRUE WHERE email = $1` to
   converge any pre-existing row to the intended admin state.

2. **Test link** — checks whether the admin already owns a non-denied link to
   `https://www.wikipedia.org` (matching the `idx_links_user_destination` partial
   index predicate `denied_reason = 0`). If one exists it is reused; otherwise a
   unique key is generated (with a DB existence check) and a new `links` row is
   inserted. The printed short URL uses the canonical prefix
   `https://go.sstools.co/u/` regardless of `BASE_URL`.

Both steps are safe to re-run at any time; running `seed` against an already-seeded
database produces no duplicate rows and does not overwrite admin state.

The seed command does **not** enroll a passkey. To obtain the first passkey for
the admin account after seeding, use the **Recover account** flow on the login
page — not the Register flow (see `DEPLOYMENT.md` section 10, "First admin
login").

---

## Entity relationship summary

```
users ──< links ──< clicks
  │
  ├──< sessions
  ├──< passkey_credentials
  ├──< pending_registrations ──< webauthn_challenges
  ├──< webauthn_challenges (via user_id)
  ├──< audit_log (actor_id)
  ├──< audit_log (user_id)
  └──< url_filter_rules (created_by)

settings   (no FK; key-value store)
```
