# Authentication and Sessions

This document covers the session-based authentication and authorization layer:
session lifecycle, the session-guard middleware, public vs protected routes, the
registration gate, and admin authorization. The passkey WebAuthn ceremonies
themselves (registration, login, account recovery) are covered in
[docs/passkeys.md](passkeys.md).

---

## Overview

ShortLinks is passkey-only. There are no passwords. A successful WebAuthn
ceremony (registration, login, or account recovery) is the only way to obtain a
session. Once a session exists, all subsequent requests are authenticated by a
server-side database lookup on the session token carried in a cookie.

---

## Session lifecycle

### Token generation

Session tokens are produced by `auth.NewSessionToken`
(`internal/auth/session.go`). It generates 32 cryptographically random bytes
via `crypto/rand` and encodes them as unpadded URL-safe base64
(`base64.RawURLEncoding`), yielding a ~43-character opaque string safe to use
in both URLs and `Set-Cookie` headers.

```
sessionTokenLen = 32  // bytes; defined in internal/auth/tokens.go
```

### Creation

`Store.CreateSession` (`internal/auth/store.go`) inserts a row into the
`sessions` table inside the same database transaction as the WebAuthn ceremony
finish step. The three ceremony paths that issue sessions are:

| Flow | Source |
|---|---|
| Registration finish | `auth.RegistrationService.FinishRegistration` |
| Login finish | `auth.LoginService.FinishLogin` |
| Recovery finish | `auth.RecoveryService.FinishRecovery` |

All three call `Store.CreateSession` inside a transaction, so the account
operation and the session are created atomically — a rolled-back ceremony leaves
no orphaned session.

The initial TTL is 30 days:

```
sessionTTL = 30 * 24 * time.Hour  // internal/auth/store.go
```

The session row schema (migration `migrations/000005_create_sessions.up.sql`):

```sql
CREATE TABLE sessions (
    id           BIGSERIAL PRIMARY KEY,
    user_id      BIGINT NOT NULL REFERENCES users(id),
    token        TEXT UNIQUE NOT NULL,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at   TIMESTAMPTZ NOT NULL,
    last_seen_at TIMESTAMPTZ NOT NULL
);
```

### Cookie

After `CreateSession` returns, the handler calls `auth.SetSessionCookie`
(`internal/auth/session.go`), which writes the token to the HTTP response with
these fixed attributes:

| Attribute | Value |
|---|---|
| Name | `shortlinks_session` |
| Path | `/` |
| HttpOnly | true |
| Secure | true |
| SameSite | Strict |
| Expires | matches `sessions.expires_at` |

All three ceremony finish handlers (`RegisterFinish`, `LoginFinish`,
`RecoverFinish` in `internal/handlers/auth.go`) call `auth.SetSessionCookie`
directly after the service call returns the token. The attributes are defined
in a single place so every auth flow produces an identical cookie.

### SESSION_SECRET

`SESSION_SECRET` is a required environment variable loaded by
`internal/config/config.go` and stored in `config.Config.SessionSecret`.
However, it is **not used to sign or encrypt cookies**: sessions are
server-side opaque tokens stored in the database. The variable is required at
startup as a deployment signal — its presence signals a production
configuration. If the variable is absent, `config.Load` returns an error and
the server refuses to start.

### Validation and the sliding window

On every protected request, `middleware.RequireSession` reads the
`shortlinks_session` cookie and calls `Store.ResolveSession`
(`internal/auth/store.go`). That method does the following in a single
`UPDATE ... FROM ... RETURNING` round-trip:

1. Joins `sessions` to `users` by the cookie value.
2. Rejects the token if `sessions.expires_at <= now` (expired) or
   `users.active = FALSE` (deactivated account).
3. On success, bumps `sessions.last_seen_at` to now and extends
   `sessions.expires_at` to `now + 30 days` — implementing the 30-day sliding
   window.
4. Returns `(user_id, email, is_admin)` as `auth.SessionUser`.

If the `UPDATE` affects zero rows (token unknown or expired), a follow-up read
determines the cause:
- Token absent or owner's account absent → `auth.ErrSessionInvalid`
- Row exists but `expires_at` is in the past → `auth.ErrSessionInvalid`; the
  expired row is deleted best-effort.
- Row exists and is live but `users.active = FALSE` →
  `auth.ErrAccountInactive`

Both `ErrSessionInvalid` and `ErrAccountInactive` produce the same `401
{"error":"unauthenticated"}` response to the client so neither case is revealed
externally.

### Expiry and deletion

Sessions expire passively: `ResolveSession` rejects and best-effort-deletes
rows whose `expires_at` is in the past. Active sessions are renewed on every
request by the sliding-window bump.

Explicit deletion happens in two places:

- **Logout** (`auth.LoginService.Logout`, called by `AuthHandler.Logout`):
  `Store.DeleteSession` removes the row for the cookie's token. The handler
  then calls `clearSessionCookie` to send a `Max-Age: -1` cookie to the
  browser.
- **Account deactivation** (`auth.Store.DeactivateUser`): deletes *all*
  sessions for the target user atomically in the same transaction as the
  `users.active = FALSE` flip, forcing immediate logout across every device.

---

## Session-guard middleware

### RequireSession

`middleware.RequireSession` (`internal/middleware/auth.go`) is an
`http.Handler` middleware factory. It:

1. Reads the `shortlinks_session` cookie.
2. Calls the injected `SessionResolver.ResolveSession` (satisfied in production
   by `*auth.Store`).
3. On success, wraps the `auth.SessionUser` into an `*middleware.AuthUser`
   (carrying `ID`, `Email`, `IsAdmin`) and stores it in the request context
   under an unexported key.
4. On failure (missing cookie, invalid/expired session, or inactive account),
   writes `401 {"error":"unauthenticated"}` and stops the chain.

Handlers retrieve the authenticated user with:

```go
u, ok := middleware.UserFromContext(r.Context())
```

### RequireAdmin

`middleware.RequireAdmin` (`internal/middleware/auth.go`) is a plain
`http.Handler` middleware (not a factory) that must be applied **after**
`RequireSession`. It reads the `AuthUser` set by `RequireSession` and:

- Returns `401` if no user is present (guard was not applied).
- Returns `403 {"error":"forbidden"}` if `AuthUser.IsAdmin` is false.
- Calls `next` if the user is an admin.

In `cmd/shortlinks/main.go`, the two are composed into a `requireAdmin`
helper:

```go
requireAdmin := func(next http.Handler) http.Handler {
    return requireSession(middleware.RequireAdmin(next))
}
```

### Public vs protected routes

All routes are registered in `cmd/shortlinks/main.go`. The table below shows
which middleware each route group carries.

**Public (no session required)**

| Route | Purpose |
|---|---|
| `GET /health` | Health check |
| `GET /u/{key}` | Short-link redirect |
| `POST /auth/register/start` | Begin registration (rate-limited) |
| `GET /auth/register/verify` | Verify magic-link token |
| `POST /auth/register/finish` | Complete registration, issue session |
| `GET /auth/login/start` | Issue assertion challenge (rate-limited) |
| `POST /auth/login/finish` | Verify assertion, issue session |
| `POST /auth/logout` | Delete session (idempotent; no guard needed) |
| `POST /auth/recover` | Begin account recovery (rate-limited) |
| `GET /auth/recover/verify` | Verify recovery token |
| `POST /auth/recover/finish` | Complete recovery, issue session |
| `GET /` (catch-all) | Svelte SPA static assets |

**Authenticated (RequireSession)**

| Route | Purpose |
|---|---|
| `GET /api/me` | Current user profile |
| `GET /api/events` | SSE push stream |
| `POST /api/links` | Create short link |
| `GET /api/links` | List short links |
| `GET /api/links/{key}` | Link detail + click stats |
| `PATCH /api/links/{key}` | Update short link |
| `DELETE /api/links/{key}` | Delete short link |
| `GET /account/credentials` | List passkeys |
| `PATCH /account/credentials/{id}` | Rename passkey |
| `DELETE /account/credentials/{id}` | Revoke passkey |

**Admin only (RequireSession + RequireAdmin)**

| Route | Purpose |
|---|---|
| `GET /admin/settings` | List runtime settings |
| `PATCH /admin/settings` | Update a setting |
| `GET /admin/users` | List all users |
| `GET /admin/users/{id}` | User detail |
| `POST /admin/users/{id}/deactivate` | Deactivate a user |
| `POST /admin/users/{id}/reactivate` | Reactivate a user |
| `GET /admin/audit` | Audit log (paginated) |
| `GET /admin/url-filters` | List URL filter rules |
| `POST /admin/url-filters` | Create URL filter rule |
| `POST /admin/url-filters/test` | Dry-run test a rule |
| `PATCH /admin/url-filters/{id}` | Update URL filter rule |
| `DELETE /admin/url-filters/{id}` | Delete URL filter rule |

---

## Registration gate

The `settings` table (migration `migrations/000006_create_settings.up.sql`)
holds runtime configuration as key/value TEXT pairs. It is seeded at migration
time with:

```sql
INSERT INTO settings (key, value, updated_at)
VALUES ('registrations_enabled', 'false', now())
ON CONFLICT DO NOTHING;
```

The default is `'false'` — the server is locked to existing users immediately
after a fresh install. An admin must explicitly set it to `'true'` to open
registration.

### How the gate is enforced

`RegistrationService.StartRegistration` (`internal/auth/registration.go`) calls
`Store.RegistrationsEnabled` at the very start of the handler chain, before
creating any pending registration row or sending any email:

```go
enabled, err := s.store.RegistrationsEnabled(ctx)
if !enabled {
    return ErrRegistrationsDisabled
}
```

`Store.RegistrationsEnabled` (`internal/auth/store.go`) reads the setting fresh
from the database on every call — it is never cached. This means an admin
toggling the setting via `PATCH /admin/settings` takes effect on the very next
`POST /auth/register/start` request, with no restart required.

When `ErrRegistrationsDisabled` is returned, `handlers.AuthHandler.RegisterStart`
(`internal/handlers/auth.go`) responds with `403 {"error":"Registration
closed"}`.

### Toggling the gate

Only admins can read or update the settings table, via the routes guarded by
`RequireSession + RequireAdmin`:

- `GET /admin/settings` — returns the full settings list as
  `{"settings":[{"key":"...","value":"...","updated_at":"..."}]}`.
- `PATCH /admin/settings` — accepts `{"key":"registrations_enabled","value":"true"}`.

The `SettingsHandler.Patch` handler (`internal/handlers/settings.go`) validates
the value for `registrations_enabled` via `validSettingValue`:

```go
case "registrations_enabled":
    return value == "true" || value == "false"
```

Only the values `"true"` and `"false"` are accepted; anything else returns
`400`. Unknown keys return `400` (the store's `ErrSettingNotFound`); arbitrary
key creation is forbidden.

Every `PATCH` writes a `settings.updated` audit entry via `audit.Logger`,
recording `{key, old_value, new_value}` and the acting admin's `actor_id` and
IP address.

---

## Admin authorization

### Who is an admin

The `users` table (`migrations/000001_create_users.up.sql`) carries a boolean
`is_admin` column (default `FALSE`). Admin status is granted in exactly two
situations:

1. **First registrant on a fresh install.** During `FinishRegistration`,
   `Store.UserCount` is called inside the ceremony transaction. If the count
   is 0, `promoteAdmin = true` and `CreateUser` inserts the new row with
   `is_admin = TRUE`.

2. **`ADMIN_EMAIL` match.** If the registrant's (lowercased) email equals the
   `ADMIN_EMAIL` environment variable (also lowercased at service construction
   in `NewRegistrationService`), `promoteAdmin = true` regardless of whether
   other users exist.

```go
promoteAdmin := count == 0 || (s.adminEmail != "" && email == s.adminEmail)
```

There is no UI to promote or demote admins after the fact; it is set at
registration time only.

### How the flag is propagated

`Store.ResolveSession` returns `auth.SessionUser` including `IsAdmin`.
`RequireSession` copies this into `middleware.AuthUser.IsAdmin` and stores it
on the request context. `RequireAdmin` reads `AuthUser.IsAdmin` from the
context and short-circuits with `403` if it is false.

The SPA uses `GET /api/me` (handler `handlers.MeHandler`, `internal/handlers/me.go`)
to read `{id, email, is_admin}` from the session context and gate the Admin tab
client-side. The server-side gate is authoritative; the client-side tab is
display-only.

### What admins can do that regular users cannot

- Read and update the `settings` table (including the registration gate).
- List, view, deactivate, and reactivate user accounts.
- Read the audit log.
- Create, update, delete, and test URL filter rules.
- Deactivating a user sets `users.active = FALSE` and deletes all of that
  user's sessions atomically (forcing immediate logout). Admins themselves
  cannot be deactivated, and a user cannot deactivate themselves.

---

## Rate limiting on auth endpoints

`middleware.RateLimiter` (`internal/middleware/ratelimit.go`) is a per-IP
token-bucket limiter (backed by `golang.org/x/time/rate`). A separate instance
is constructed for each protected public auth endpoint in `main.go`:

| Route | Limit |
|---|---|
| `POST /auth/register/start` | 3 requests / hour / IP |
| `GET /auth/login/start` | 10 requests / minute / IP |
| `POST /auth/recover` | 3 requests / hour / IP |

Client IP is extracted from `X-Forwarded-For` (set by the Apache reverse proxy
in production) with a fallback to `RemoteAddr`. A request that exceeds the
limit receives `429 {"error":"rate_limit_exceeded"}` and the chain is stopped.

Limiters are never evicted from the in-memory map. For a single-instance
service behind Apache, the bounded set of real-world client IPs makes this
acceptable; see `issues/0020.md` for the documented tradeoff.

The session-validation path (`RequireSession`) and all authenticated API routes
are not rate-limited at the middleware layer.

---

## Key source files

| File | Role |
|---|---|
| `internal/auth/session.go` | Cookie name, `SetSessionCookie`, `NewSessionToken` |
| `internal/auth/tokens.go` | `randomURLToken`, `sessionTokenLen` |
| `internal/auth/store.go` | `CreateSession`, `ResolveSession`, `DeleteSession`, `RegistrationsEnabled`, settings CRUD |
| `internal/auth/registration.go` | `RegistrationService.StartRegistration` (registration gate check) |
| `internal/auth/login.go` | `LoginService.FinishLogin`, `Logout` |
| `internal/auth/users.go` | `DeactivateUser` (session deletion on deactivation), `ReactivateUser` |
| `internal/middleware/auth.go` | `RequireSession`, `RequireAdmin`, `AuthUser`, `UserFromContext` |
| `internal/middleware/ratelimit.go` | Per-IP token-bucket rate limiter |
| `internal/handlers/auth.go` | `AuthHandler` — all `/auth/*` endpoints |
| `internal/handlers/settings.go` | `SettingsHandler` — `/admin/settings` |
| `internal/handlers/users.go` | `AdminUsersHandler` — `/admin/users/*` |
| `internal/config/config.go` | `SESSION_SECRET`, `ADMIN_EMAIL` env vars |
| `migrations/000005_create_sessions.up.sql` | `sessions` table schema |
| `migrations/000006_create_settings.up.sql` | `settings` table schema + `registrations_enabled` seed |
| `cmd/shortlinks/main.go` | Route registration, middleware wiring |
