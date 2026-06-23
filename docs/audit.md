# Audit log

ShortLinks maintains an append-only audit log of every significant action. Rows are inserted once and never updated or deleted.

## Schema

Migration: `migrations/000007_create_audit_log.up.sql`

```sql
CREATE TABLE audit_log (
    id          BIGSERIAL PRIMARY KEY,
    actor_id    BIGINT REFERENCES users(id),   -- who performed the action (NULL for pre-auth events)
    user_id     BIGINT REFERENCES users(id),   -- whose account/resource was affected
    action      TEXT NOT NULL,                 -- event type string (see Action catalogue below)
    target_type TEXT,                          -- entity kind: "link", "user", "credential", "settings", "url_filter"
    target_id   BIGINT,                        -- row id of the affected entity (NULL when not applicable)
    metadata    JSONB,                         -- action-specific key/value detail (NULL when no extra context)
    ip_address  INET,                          -- actor's client IP (NULL when unknown or pre-auth without IP)
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
```

Three indexes support the admin read path:

| Index | Columns | Purpose |
|---|---|---|
| `idx_audit_log_user_id_created_at` | `(user_id, created_at DESC)` | Per-user filter in the admin view |
| `idx_audit_log_created_at` | `(created_at DESC)` | Full-log newest-first scan |
| `idx_audit_log_action` | `(action)` | Filter by event type (not yet exposed in the UI) |

`ip_address` is stored as PostgreSQL `INET`. The read path returns it via `host(ip_address)` so the caller receives a bare address string (e.g. `203.0.113.7`) rather than the CIDR form.

---

## Action catalogue

Constants are defined in `internal/audit/actions.go`. Every call site references a constant; no action string is written as a string literal elsewhere.

### Account lifecycle

| Action string | Constant | When written | actor_id | Notes |
|---|---|---|---|---|
| `account.registration_started` | `ActionAccountRegistrationStarted` | User submits the registration form (email sent) | NULL | Pre-auth; no user row exists yet |
| `account.registered` | `ActionAccountRegistered` | WebAuthn finish-registration ceremony succeeds | new user | Written inside the ceremony transaction via `WriteTx` |
| `account.login` | `ActionAccountLogin` | WebAuthn finish-assertion ceremony succeeds | user | Written inside the ceremony transaction via `WriteTx` |
| `account.logout` | `ActionAccountLogout` | Session deleted on logout | user | Fire-and-forget; only written when a real session was deleted |
| `account.recovery_started` | `ActionAccountRecoveryStarted` | User requests passkey recovery (email sent) | NULL | Pre-auth; `user_id` is set because the target account is known |
| `account.recovered` | `ActionAccountRecovered` | WebAuthn finish-recovery ceremony succeeds | user | Written inside the ceremony transaction via `WriteTx` |
| `account.deactivated` | `ActionAccountDeactivated` | Admin deactivates a non-admin user | acting admin | Written inside the store's transaction via `WriteTx` |
| `account.reactivated` | `ActionAccountReactivated` | Admin reactivates a user | acting admin | Written inside the store's transaction via `WriteTx` |

### Credential lifecycle

| Action string | Constant | When written | actor_id | Notes |
|---|---|---|---|---|
| `credential.added` | `ActionCredentialAdded` | First passkey registered (finish-registration) or replacement passkey enrolled (finish-recovery) | user | Written inside the ceremony transaction via `WriteTx`; emitted once per ceremony alongside the account event |
| `credential.revoked` | `ActionCredentialRevoked` | User deletes one of their passkeys | user | Fire-and-forget |

### Link lifecycle

| Action string | Constant | When written | actor_id | Notes |
|---|---|---|---|---|
| `link.created` | `ActionLinkCreated` | Short link inserted (or active duplicate returned) | link owner | Fire-and-forget |
| `link.deactivated` | `ActionLinkDeactivated` | Short link soft-deleted (`DELETE /api/links/{key}`) | link owner | Fire-and-forget |
| `link.reactivated` | `ActionLinkReactivated` | Dedup path reactivates an existing inactive link | link owner | Fire-and-forget; emitted instead of `link.created` for the reactivation branch |
| `link.denied` | `ActionLinkDenied` | URL filter match blocks a create request | link owner | Fire-and-forget; denied link row is still inserted (active=false) |

### URL filter rules

| Action string | Constant | When written | actor_id |
|---|---|---|---|
| `url_filter.created` | `ActionURLFilterCreated` | Admin creates a filter rule | acting admin |
| `url_filter.updated` | `ActionURLFilterUpdated` | Admin edits a filter rule | acting admin |
| `url_filter.deleted` | `ActionURLFilterDeleted` | Admin deletes a filter rule | acting admin |

### Settings

| Action string | Constant | When written | actor_id |
|---|---|---|---|
| `settings.updated` | `ActionSettingsUpdated` | Admin changes a runtime setting (e.g. `registrations_enabled`) | acting admin |

---

## Write path

Source: `internal/audit/`

### Core types

`audit.Logger` (defined in `internal/audit/audit.go`) wraps a `*pgxpool.Pool`. It is constructed once in `main` and injected into every service and handler that needs to record actions.

`audit.Entry` is the write shape. Pointer fields map to nullable columns; a nil value stores SQL NULL:

```go
type Entry struct {
    ActorID    *int64  // NULL for pre-auth events
    UserID     *int64  // NULL when no user is involved
    Action     string
    TargetType string  // empty string → NULL via nullIfEmpty helper
    TargetID   *int64
    Metadata   any     // marshalled to JSONB; nil → NULL
    IP         string  // empty string → NULL
}
```

### Two write methods — one policy choice

| Method | Used by | Behaviour on error |
|---|---|---|
| `Logger.WriteTx(ctx, tx, entry)` | Auth ceremonies (registration, login, recovery, deactivation, reactivation) | Returns the error; caller rolls back the surrounding transaction |
| `Logger.Record(ctx, entry)` | All other call sites (links, credentials, URL filters, settings) | Logs the error at WARN and continues — the user's action already committed |

The policy is: **an audit write must never break the user's request**. The `WriteTx` path is the exception: auth ceremonies own an open transaction and write the audit row inside it, so the row commits or rolls back atomically with the action itself. A ceremony failure rolls back both the action and its audit row together.

`Logger.Write` (returns the error) exists for tests that want to observe insert results; request-path code never calls it.

### Metadata captured per action

| Action | Metadata keys |
|---|---|
| `account.registration_started` | *(none)* |
| `account.registered` | *(none)* |
| `account.login` | *(none)* |
| `account.logout` | *(none)* |
| `account.recovery_started` | *(none)* |
| `account.recovered` | *(none)* |
| `account.deactivated` | `reason`, `note` |
| `account.reactivated` | `note` |
| `credential.added` | `device_name`, `aaguid` |
| `credential.revoked` | `device_name` |
| `link.created` | `key`, `destination_url`, `title`, `duplicate` |
| `link.deactivated` | `key`, `destination_url` |
| `link.reactivated` | `key`, `destination_url` |
| `link.denied` | `destination_url`, `reason_code`, `reason_label`, `matched_rule_id` |
| `url_filter.created` | `pattern`, `reason_code`, `description` |
| `url_filter.updated` | `old_pattern`, `new_pattern`, `old_reason_code`, `new_reason_code` |
| `url_filter.deleted` | `pattern`, `reason_code`, `description` |
| `settings.updated` | `key`, `old_value`, `new_value` |

---

## Admin surface

### API endpoint

`GET /admin/audit` — served by `AdminAuditHandler.List` in `internal/handlers/audit.go`.

Requires an active admin session (mounted behind `middleware.RequireSession` + `middleware.RequireAdmin`).

Query parameters:

| Parameter | Default | Max | Notes |
|---|---|---|---|
| `page` | 1 | — | 1-based; non-positive integers return 400 |
| `per_page` | 50 | 200 | Clamped silently at 200 |
| `user_id` | *(unset)* | — | Optional; filters to rows where `user_id` matches |

Response shape:

```json
{
  "audit_log": [
    {
      "id": 42,
      "actor_id": 1,
      "user_id": 1,
      "action": "link.created",
      "target_type": "link",
      "target_id": 17,
      "metadata": { "key": "abc123", "destination_url": "https://example.com", "title": "", "duplicate": false },
      "ip_address": "203.0.113.7",
      "created_at": "2026-06-18T12:34:56Z"
    }
  ],
  "total": 234,
  "page": 1,
  "per_page": 50
}
```

Rows are returned newest-first (`ORDER BY created_at DESC, id DESC`). `metadata` is raw JSONB — it reaches the client as a JSON object, never a quoted string. `ip_address` is the bare host string (INET `host()` applied server-side).

### Svelte UI

`web/src/views/Admin.svelte` renders the audit log under the Admin view's "Audit log" subtab.

Features:

- **Paginated table** — 50 rows per page, Previous/Next controls, page X of N displayed.
- **Per-user filter** — a numeric `user_id` field; submitting filters the table and resets to page 1. A "Clear" button appears when a filter is active.
- **Columns displayed**: When, Action, Actor, Target, Metadata, IP.
- Helper functions from `web/src/lib/admin.ts` (`actorLabel`, `targetLabel`, `formatMetadata`, `formatDateTime`) handle display formatting. `formatMetadata` renders the raw JSONB payload inline in a monospace cell.

The UI is admin-only: a non-admin who reaches the Admin view sees an access-denied notice and no data is fetched.

---

## Privacy and retention notes

- **No automated deletion** — the table has no TTL, scheduled purge, or soft-delete mechanism. Rows accumulate indefinitely.
- **IP addresses** — stored as PostgreSQL `INET`. An empty client IP stores NULL rather than an empty string (enforced in `insert`).
- **Pre-auth events** — `account.registration_started` stores no `actor_id` and no `user_id`; only the IP and a timestamp are captured (no email address is written to the audit log).
- **`account.recovery_started`** — `actor_id` is NULL but `user_id` is set to the target account, so the existence of a recovery attempt for a specific user is recorded.
- **Credential metadata** — `credential.added` records `device_name` and `aaguid` (authenticator model identifier) but never the raw credential bytes or public key.
- **Settings** — `settings.updated` records the old and new value of the changed setting key, making the full change history visible to admins with log access.
- **Access control** — the read endpoint (`GET /admin/audit`) is admin-only. There is no user-facing self-service audit view.
