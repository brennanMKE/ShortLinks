# ShortLinks — Architecture Overview

This document is the high-level map of how ShortLinks fits together. Read it first, then follow the "Where to look" table at the bottom to dive into any subsystem.

---

## System diagram

```
Browser / curl
      │  HTTPS :443
      ▼
┌──────────────┐
│  Apache 2    │  TLS termination (Let's Encrypt)
│  reverse     │  ProxyPass → 127.0.0.1:8080
│  proxy       │  /api/events: flushpackets=on (SSE)
└──────┬───────┘
       │  HTTP  127.0.0.1:8080
       ▼
┌──────────────────────────────────────────┐
│  Go service   cmd/shortlinks serve       │
│                                          │
│  http.ServeMux (Go 1.22 pattern routing) │
│  ├─ GET /u/{key}       RedirectHandler   │
│  ├─ /auth/*            AuthHandler       │
│  ├─ /account/*         Credentials /     │
│  │                     Settings handlers │
│  ├─ /admin/*           Admin handlers    │
│  ├─ /api/links/*       LinksHandler      │
│  ├─ GET /api/me        MeHandler         │
│  ├─ GET /api/events    EventsHandler     │
│  └─ GET /              SPAHandler        │
│        (catch-all → embedded index.html) │
│                                          │
│  In-process                              │
│  ├─ Ristretto redirect cache             │
│  ├─ Rule cache (60 s TTL)                │
│  └─ SSE broker (in-memory pub/sub)       │
└───────────────┬──────────────────────────┘
                │  pgx/v5 pool
                ▼
       ┌────────────────┐
       │  PostgreSQL    │
       │  (local EC2)   │
       └────────────────┘
```

---

## Components

### `cmd/shortlinks` — the binary entry point

`cmd/shortlinks/main.go` is a thin dispatcher with three subcommands:

| Subcommand | Purpose |
|---|---|
| `serve` | Wires all dependencies and starts the HTTP server on `127.0.0.1:<PORT>` (default 8080). |
| `seed` | Idempotent bootstrap: ensures the admin user (`ADMIN_EMAIL`) and a test link exist. Safe to re-run. |
| _(default)_ | Prints the version string (`0.1.0`) and exits. |

The `serve` function in `cmd/shortlinks/main.go` is where all dependencies are constructed and injected: the pgx pool, the WebAuthn relying party, the audit logger, every handler, the caches, the rate limiters, and the route table.

### `internal/` packages

| Package | Role |
|---|---|
| `config` | Loads `Config` from environment variables (and a `.env` file in development). Collects all validation errors before returning. |
| `db` | Opens and pings the `*pgxpool.Pool`. The pool is the single shared connection object injected into every store. |
| `auth` | WebAuthn registration, login, and recovery ceremonies; session management; passkey credential store. Talks to `users`, `passkey_credentials`, `webauthn_challenges`, `pending_registrations`, and `sessions` tables. |
| `links` | Link key generation (`keygen.go`), key uniqueness checking, and the DB-backed CRUD store (`store.go`) scoped by `user_id`. `resolver.go` implements the cache→DB read-through used by the redirect path. |
| `clicks` | `Recorder` persists one row per redirect click to the `clicks` table (best-effort, fire-and-forget). `StatsStore` aggregates UTM breakdowns and daily time-series for analytics. |
| `filters` | Denial reason code constants, `CompileRules`/`Evaluate` for regex-based URL filtering, and the `Store` for admin CRUD on `url_filter_rules`. |
| `cache` | Two independent caches: a Ristretto-backed per-key `Cache` for redirect targets (positive and negative entries), and a mutex-guarded `RuleCache` for the compiled filter rule snapshot (60 s TTL, invalidated immediately on any rule mutation). |
| `audit` | Append-only `audit_log` writer. Two write paths: `WriteTx` (inside an auth ceremony's transaction, so the row commits or rolls back atomically) and `Record` (fire-and-forget from API handlers whose action has already committed). |
| `events` | In-memory pub/sub `Broker`. The links handler publishes `link.created` events; each `GET /api/events` SSE stream subscribes for the authenticated user. |
| `handlers` | All `http.Handler` implementations: redirect, auth, credentials, settings, links, me, events, admin (users, audit, url-filters), health, and the SPA catch-all. |
| `middleware` | `RequireSession` (reads `shortlinks_session` cookie, resolves session, attaches `AuthUser` to context), `RequireAdmin` (checks `is_admin`), and per-IP token-bucket `RateLimiter`. |
| `testdb` | Test helper that connects to a real PostgreSQL database for integration tests. |

### Embedded SPA — `web/`

The Svelte 5 + Vite + TypeScript single-page app lives in `web/src/`. During a production build, `npm run build` writes hashed assets to `web/dist/`. The Go package `web` (in `web/embed.go`) embeds `web/dist/` with `//go:embed all:dist` at compile time, so the Go binary is self-contained — no external static file serving is needed.

`web.DistFS()` returns an `fs.FS` rooted at the `dist/` directory. In development, the Vite dev server runs on `:5173` and proxies `/api`, `/auth`, `/account`, `/admin`, and `/u` to the Go service on `:8080`.

SPA views (in `web/src/views/`):

| View | Shown when |
|---|---|
| `Login` | No active session |
| `Dashboard` | Active session — lists links, SSE live-updates |
| `LinkDetail` | User navigates to a link's detail/analytics |
| `Account` | Credential management, settings |
| `Admin` | Authenticated admin — user management, filter rules, audit log |
| `RegisterVerify` | Passkey registration email-link landing |
| `RecoverVerify` | Account recovery email-link landing |

### PostgreSQL

Migrations live in `migrations/` and are numbered sequentially (`000001` … `000009`). Apply them with `golang-migrate`:

```bash
migrate -path migrations -database "$DATABASE_URL" up
```

Key tables:

| Table | Purpose |
|---|---|
| `users` | Account records. `is_admin` and `active` flags. |
| `links` | Short codes. `key` (base-62, up to 12 chars), `destination_url`, `active`, `denied_reason` (0 = allowed). |
| `clicks` | One row per redirect. Stores client IP, user agent, referer, and all five UTM parameters. |
| `passkey_credentials` | One row per registered WebAuthn/FIDO2 passkey. |
| `sessions` | Active sessions. `token` maps to the `shortlinks_session` cookie. 30-day sliding-window expiry. |
| `webauthn_challenges` | Ephemeral WebAuthn challenge rows, deleted after ceremony completion. |
| `pending_registrations` | Short-lived rows tracking an email mid-registration before a `users` row exists. |
| `settings` | Key/value admin-controlled runtime settings (e.g. `registrations_enabled`). |
| `audit_log` | Append-only. Rows are never updated or deleted. JSONB `metadata` column for structured context. |
| `url_filter_rules` | Admin-managed Go regex patterns with a denial reason code. Evaluated in Go, not in the database. |

### Apache reverse proxy — `deploy/apache/go.sstools.co.conf`

Apache handles TLS (Let's Encrypt certificates) and proxies all traffic to the Go service on `127.0.0.1:8080`. The SSE endpoint (`/api/events`) is registered separately with `flushpackets=on` so events are not buffered.

### systemd service — `deploy/systemd/shortlinks.service`

The service runs as the unprivileged `shortlinks` user with a hardened sandbox (`NoNewPrivileges`, `ProtectSystem=strict`, `PrivateTmp`, etc.). Runtime configuration comes from `/etc/shortlinks/config.env` (not in source control). The binary path is `/usr/local/bin/shortlinks serve`.

---

## Three request lifecycles

### (a) Short-link redirect — `GET /u/{key}`

The hot, public path. No session required.

```
Browser GET /u/{key}
  │
  ▼ RedirectHandler.ServeHTTP (internal/handlers/redirect.go)
  │
  ├─ 1. Extract {key} from path
  │
  ├─ 2. Resolver.Resolve (internal/links/resolver.go)
  │       ├─ cache.Cache.Get(key)
  │       │     hit  → return CachedLink (may be Negative entry → 404)
  │       │     miss ↓
  │       └─ links.Store.ResolveByKey (DB: SELECT … FROM links WHERE key = $1)
  │               not found → cache.SetNegative(key, 30 s TTL) → 404
  │               found     → cache.Set(key, link, 300 s TTL)
  │
  ├─ 3. Active check: !found || !link.Active → 404
  │      (denied links have active=false, so they 404 here)
  │
  ├─ 4. Expiry check: link.ExpiresAt past → 410 Gone
  │
  ├─ 5. go recorder.RecordClick(info)  ← detached goroutine, best-effort
  │       clicks.Recorder.RecordClick (internal/clicks/recorder.go)
  │       → INSERT INTO clicks … (subquery resolves key → link_id)
  │       5 s bounded context; errors logged, never surface to user
  │
  ├─ 6. mergeUTM: overlay inbound utm_* params onto destination URL
  │
  └─ 7. 302 Found → Location: <merged destination>
```

Cache TTLs: positive entries 300 s (configurable via `CACHE_TTL_SECONDS`), negative entries 30 s (hardcoded). PATCH/DELETE on a link evicts its key immediately so the next redirect re-reads the DB.

### (b) Authenticated API request — e.g. `POST /api/links`

All `/api/*`, `/admin/*`, and `/account/*` routes follow this pattern:

```
Browser POST /api/links  (JSON body)
  │
  ▼ middleware.RateLimiter.Middleware (if applicable)
  │   token-bucket per client IP; 429 if exceeded
  │
  ▼ middleware.RequireSession (internal/middleware/auth.go)
  │   reads shortlinks_session cookie
  │   → auth.Store.ResolveSession (DB lookup + sliding-window bump)
  │   invalid/expired/inactive → 401
  │   ok → attaches AuthUser{ID, Email, IsAdmin} to context
  │
  ▼ [middleware.RequireAdmin — admin-only routes only]
  │   reads AuthUser from context; !IsAdmin → 403
  │
  ▼ handlers.LinksHandler.Create (internal/handlers/links.go)
  │
  ├─ 1. Decode JSON body; validate destination URL
  │
  ├─ 2. cache.RuleCache.Rules → filters.Evaluate(rules, url)
  │       match → linkStore.CreateDeniedLink (INSERT active=false, denied_reason=code)
  │               → audit.Record(link.denied)
  │               → 422 Unprocessable Entity
  │
  ├─ 3. linkStore.CreateOrReactivateLink (dedup + insert atomically)
  │       a. existing non-denied link for same (user, destination) → reactivate
  │       b. no existing link → GenerateUniqueKey + INSERT
  │
  ├─ 4. audit.Record(link.created / link.reactivated)
  │
  ├─ 5. broker.Publish(userID, Event{Name:"link.created", Payload: JSON})
  │       → fans out to all GET /api/events SSE streams open for this user
  │
  └─ 6. 201 Created  (JSON link row)
```

The same `RequireSession → handler` pattern applies to every authenticated route; the depth of logic inside the handler varies by endpoint.

### (c) SPA catch-all — serving `index.html`

```
Browser GET /dashboard  (or any SPA deep link)
  │
  ▼ handlers.SPAHandler.ServeHTTP (internal/handlers/static.go)
  │   registered as "GET /" — least-specific pattern under Go 1.22 mux,
  │   so every explicit route above wins over it
  │
  ├─ fs.Stat(dist, "dashboard") → not found (no such embedded file)
  │
  └─ serve dist/index.html (Content-Type: text/html)
        │
        ▼ Browser loads Svelte SPA
            onMount: GET /api/me
              200 → currentView = 'dashboard'
              401 → currentView = 'login'
```

Hashed static assets (e.g. `/assets/index-abc123.js`) are served directly by `http.FileServerFS` because `fs.Stat` finds them in the embedded FS. Only unknown paths fall back to `index.html`.

---

## Tech stack and key dependencies

| Concern | Technology | Module / import |
|---|---|---|
| Language | Go 1.26 | — |
| HTTP routing | `net/http` (Go 1.22 pattern mux) | stdlib |
| Database driver | pgx v5 | `github.com/jackc/pgx/v5` |
| DB migrations | golang-migrate (CLI, not a Go import) | external CLI |
| WebAuthn / passkeys | go-webauthn | `github.com/go-webauthn/webauthn` |
| In-process cache | Ristretto v2 | `github.com/dgraph-io/ristretto/v2` |
| Rate limiting | token-bucket | `golang.org/x/time/rate` |
| `.env` loading | godotenv | `github.com/joho/godotenv` |
| Frontend framework | Svelte 5 | `svelte ^5.20.0` |
| Frontend build | Vite 6 + TypeScript | `vite ^6.1.0` |
| Email | AWS SES via SMTP | custom `auth.SESMailer` |

---

## Where to look

| Subsystem | Package(s) | Per-topic doc |
|---|---|---|
| Architecture (this file) | — | `docs/architecture.md` |
| Configuration | `internal/config` | [`docs/configuration.md`](configuration.md) |
| Database schema & migrations | `migrations/`, `internal/db` | _(#0064 — pending)_ |
| Auth & passkeys | `internal/auth`, `internal/middleware` | _(#0065 — pending)_ |
| Passkey ceremonies detail | `internal/auth` (`registration.go`, `login.go`, `recovery.go`) | _(#0066 — pending)_ |
| Link management | `internal/links`, `internal/handlers` (`links.go`) | _(#0067 — pending)_ |
| Click analytics | `internal/clicks` | _(#0068 — pending)_ |
| UTM parameter handling | `internal/handlers` (`redirect.go`), `internal/clicks` | _(#0069 — pending)_ |
| URL filter rules | `internal/filters`, `internal/cache` (`rules.go`) | _(#0070 — pending)_ |
| Audit log | `internal/audit` | _(#0071 — pending)_ |
| SSE / real-time events | `internal/events`, `internal/handlers` (`events.go`) | _(#0072 — pending)_ |
| Frontend SPA | `web/src/` | _(#0073 — pending)_ |
| Deployment (Apache + systemd) | `deploy/apache/`, `deploy/systemd/` | [`DEPLOYMENT.md`](../DEPLOYMENT.md) |

---

## Key design decisions

**Single binary, zero external assets.** The Svelte SPA is embedded in the Go binary at compile time via `//go:embed all:dist` in `web/embed.go`. A production deploy copies one binary.

**No passwords.** Authentication is passkey-only (WebAuthn FIDO2 with `residentKey=required`, `userVerification=required`). iCloud Keychain is explicitly supported. The `ADMIN_EMAIL` address is seeded by `shortlinks seed`; to enroll a passkey, the admin uses "Recover account" on the login page.

**Cache layering.** Two independent caches avoid DB hits on the hot paths: the Ristretto `Cache` for redirect key lookups (positive TTL 300 s, negative TTL 30 s) and the mutex-guarded `RuleCache` for URL filter rules (60 s TTL, immediately invalidated on any rule mutation).

**Audit-failure policy.** An audit write must never break a user request. API handlers call `audit.Record` (fire-and-forget; errors are logged at WARN). Auth ceremony handlers call `audit.WriteTx` inside the open transaction so the audit row commits or rolls back atomically with the action.

**Click recording is best-effort.** `clicks.Recorder.RecordClick` runs in a detached goroutine after the redirect response is written, under a 5 s context. DB errors are logged and swallowed; a click recording failure never affects the redirect.

**Per-user deduplication.** `links.Store.CreateOrReactivateLink` uses an atomic DB check against the `idx_links_user_destination` partial index (`WHERE denied_reason = 0`) so two link-creation requests for the same destination always return the same key rather than creating duplicates. A previously denied URL is re-evaluated rather than silently reactivated.
