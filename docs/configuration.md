# Configuration

ShortLinks is configured entirely through environment variables. The loader is
implemented in [`internal/config/config.go`](../internal/config/config.go).

---

## Environment variables

| Variable | Required | Default | Purpose |
|---|---|---|---|
| `PORT` | optional | `8080` | TCP port the HTTP server binds to. Behind Apache the value should stay at `8080` (the proxy listens on 443). |
| `BASE_URL` | **required** | — | Fully-qualified base URL of the service (e.g. `https://go.sstools.co`). Used when generating short-link URLs. |
| `DATABASE_URL` | **required** | — | PostgreSQL connection string (e.g. `postgres://shortlinks:pw@localhost:5432/shortlinks?sslmode=disable`). |
| `WEBAUTHN_RP_ID` | **required** | — | WebAuthn Relying Party ID — the registerable domain suffix (e.g. `go.sstools.co`). Must match the domain users visit. |
| `WEBAUTHN_RP_ORIGIN` | **required** | — | WebAuthn Relying Party origin — the full scheme+host (e.g. `https://go.sstools.co`). Must exactly match the browser origin. |
| `SESSION_SECRET` | **required** | — | 32-byte (64 hex character) random secret used to sign session cookies; it also derives the key that hashes visitor IPs in click records (#0121). Generate with `openssl rand -hex 32`. |
| `SES_SMTP_HOST` | optional | `""` | AWS SES SMTP hostname (e.g. `email-smtp.us-east-1.amazonaws.com`). Leave blank to disable email delivery. |
| `SES_SMTP_PORT` | optional | `587` | TCP port for the SES SMTP connection. |
| `SES_SMTP_USERNAME` | optional | `""` | SMTP username issued by AWS SES. |
| `SES_SMTP_PASSWORD` | optional | `""` | SMTP password issued by AWS SES. |
| `EMAIL_FROM` | optional | `""` | RFC 5322 sender address used in outgoing email (e.g. `ShortLinks <noreply@sstools.co>`). |
| `CACHE_MAX_COST` | optional | `10000` | Maximum cost budget for the in-memory Ristretto cache (redirect targets + filter rules). Units are arbitrary cost units passed to Ristretto. |
| `CACHE_TTL_SECONDS` | optional | `300` | Default TTL in seconds for cache entries (5 minutes). |
| `ADMIN_EMAIL` | **required** | — | Email address of the bootstrap admin user. On first startup with an empty database, this address is pre-authorized as admin when the user completes passkey registration. |
| `DEFAULT_TIMEZONE` | optional | `UTC` | IANA time zone name (e.g. `America/Los_Angeles`) the reports API (`/api/reports`, #0139) uses to bucket clicks into days when a request has no `tz` parameter. Validated at load: an unknown zone name stops startup with an error naming `DEFAULT_TIMEZONE`. |
| `PORTIKO_ENABLED` | optional | `false` | Runs the embedded [Portiko](https://github.com/Kolonnade/Portiko) identity provider under `/id` on this instance's own host (#0130). **Off by default**: with it unset or `false` the binary behaves exactly as before, reads no other `PORTIKO_*` value and creates nothing in the database. `true` or `false` only; any other value stops startup. It is refused with `STORAGE=json`. Sign-in does not use the provider yet (#0132). See [Embedded Portiko provider](#embedded-portiko-provider-id). |
| `PORTIKO_CRYPTO_KEY` | **required when enabled** | — | 32 bytes as 64 hex characters (or base64). Seals the provider's signing keys and authorization codes. Generate with `openssl rand -hex 32`. Keep it stable: with a different key the provider refuses to start (`cannot decrypt the private key`). |
| `PORTIKO_CLIENT_SECRET` | **required when enabled** | — | At least 16 characters. The secret ShortLinks itself signs in with; it is registered as the `shortlinks` client on every start. Generate with `openssl rand -hex 32`. |
| `PORTIKO_DATABASE_URL` | optional | `DATABASE_URL` with `search_path=portiko` | Where the provider keeps its tables. The default is the same database in its own `portiko` schema, created at startup if missing. If you set it, include a `search_path` for a schema of your own (created if missing) or point it at a separate database. |
| `PORTIKO_TRUSTED_PROXIES` | optional | `""` | Comma-separated IPs or CIDRs whose `X-Forwarded-For` the provider believes, so its rate limits see real client addresses. Behind Apache on the same host set `127.0.0.1,::1`. Unset, the provider logs a warning and every visitor shares the proxy's address. |
| `REDIRECT_RATE_PER_MIN` | optional | `120` | Per-client-IP limit on all `GET /u/{key}` requests per minute (#0178). Must be at least 1. |
| `REDIRECT_MISS_RATE_PER_MIN` | optional | `20` | Stricter per-IP limit on `/u/{key}` requests that answer 404 or 410, per minute, with a burst of half this value (default 10). When it is exhausted, every `/u/*` request from that IP gets `429` with `Retry-After` until it refills, even for live keys. Hits under `REDIRECT_RATE_PER_MIN` are unaffected, so a poster scanned by many people behind one NAT keeps working. 429s are not recorded as clicks. Must be at least 1. |

The per-IP key is the **last** `X-Forwarded-For` entry (the one Apache appends; earlier entries are client-supplied and ignored), and only honoured when the connection comes from loopback (Apache); otherwise the connection address is used. IPv6 clients are bucketed by their /64. If you put a CDN or another proxy in front of Apache, every visitor will appear as the proxy's address (or the proxy's own XFF handling must be reconfigured), so the limiter would throttle everyone together; revisit before doing so.

**Trade-off:** the miss limit is per IP, so a venue where many people share one NAT and several scan stale or mistyped links can exhaust that IP's miss bucket and lock out everyone there (including valid links) until it refills (about 3 seconds per token at the default). Raise `REDIRECT_MISS_RATE_PER_MIN` if that is a problem. Also, a request holds a miss token until its response is known, so more than about 10 requests in flight at once from one IP can get a `429` even if they would all have been hits. `REDIRECT_MISS_RATE_PER_MIN` must not exceed `REDIRECT_RATE_PER_MIN`.

**Required fields:** `BASE_URL`, `DATABASE_URL`, `WEBAUTHN_RP_ID`,
`WEBAUTHN_RP_ORIGIN`, `SESSION_SECRET`, `ADMIN_EMAIL`. The loader collects all
missing-required and invalid-integer errors in a single pass and returns them
together; see [Validation behavior](#validation-behavior) below.

---

## Embedded Portiko provider (`/id`)

With `PORTIKO_ENABLED=true` the binary also serves Portiko's identity provider
under `/id` (#0126, #0130). Derived from existing settings, so there is nothing
else to set:

| Provider setting | Value |
|---|---|
| Issuer | `BASE_URL` + `/id` (discovery at `/id/.well-known/openid-configuration`) |
| WebAuthn RP ID and origin | `WEBAUTHN_RP_ID` and `WEBAUTHN_RP_ORIGIN`, so existing passkeys stay usable |
| Service name | `ShortLinks` |
| Mail | SES through the same `SES_SMTP_*` settings; without them the codes are written to the log |
| Who may register | the admin's `registrations_enabled` setting, read on every attempt |
| Own client | `shortlinks`, first party, redirect `BASE_URL/auth/portiko/callback`, post-logout `BASE_URL/`, back-channel `BASE_URL/auth/portiko/backchannel` |

At startup it creates the `portiko` schema if missing, migrates it, builds the
provider and registers the client. The provider's `users` and
`schema_migrations` tables live in that schema and never in `public`. The
provider's health check is `/id/health`; `/health` stays ShortLinks'. Apache's
`ProxyPass /` already carries `/id/...`, so no vhost change is needed.

---

## How configuration is loaded

`config.Load()` (in `internal/config/config.go`) follows this sequence:

1. **Check for a `.env` file.** If a file named `.env` exists in the current
   working directory, it is loaded with
   [`godotenv`](https://github.com/joho/godotenv). A missing `.env` is not an
   error (variables may already be present in the process environment). A
   malformed `.env` is a fatal error returned to the caller.
2. **Read environment variables.** All variables are read from the process
   environment via `os.Getenv`. Variables set in the shell or by the init
   system override nothing that `godotenv` loaded — `godotenv.Load` does not
   overwrite variables already set in the environment.
3. **Apply defaults.** Integer variables (`PORT`, `SES_SMTP_PORT`,
   `CACHE_MAX_COST`, `CACHE_TTL_SECONDS`) fall back to their compiled-in
   defaults when the environment variable is unset or empty.
4. **Parse integers.** Each integer variable is parsed with `strconv.Atoi`. A
   non-empty value that cannot be parsed appends an error to the running error
   list; the default is used for the remainder of startup so that all other
   errors can still be reported.
5. **Validate required fields.** The six required string variables are checked
   for emptiness. Every missing variable is appended to the error list.
6. **Return.** If the error list is non-empty, `Load` returns `nil` and a
   single error whose message lists all problems separated by `; `. Otherwise
   the populated `*Config` is returned.

---

## Validation behavior

The loader reports **all** problems at once rather than failing on the first.
For example, if both `DATABASE_URL` and `SESSION_SECRET` are missing and `PORT`
is set to `"abc"`, the returned error reads:

```
config: invalid integer for PORT: "abc"; missing required variable DATABASE_URL; missing required variable SESSION_SECRET
```

This means a single startup attempt surfaces every misconfiguration in the log.

---

## Development: `.env` file

Copy `.env.example` to `.env` and fill in the blanks:

```bash
cp .env.example .env
$EDITOR .env
```

The `.env` file is listed in `.gitignore` and must never be committed.
`.env.example` is the authoritative list of all variables the service reads and
is safe to commit because it contains no real credentials.

```
# Generate SESSION_SECRET
openssl rand -hex 32
```

---

## Production: `/etc/shortlinks/config.env`

In production the service runs under systemd
([`deploy/systemd/shortlinks.service`](../deploy/systemd/shortlinks.service)).
The unit file references:

```ini
EnvironmentFile=/etc/shortlinks/config.env
```

The operator creates and manages `/etc/shortlinks/config.env` directly on the
server. This file is **not** in source control. Recommended permissions:

```bash
sudo install -m 0600 -o shortlinks -g shortlinks /dev/stdin /etc/shortlinks/config.env
```

The service runs as the unprivileged `shortlinks` user with a hardened systemd
sandbox (`ProtectSystem=strict`, `NoNewPrivileges=true`, etc.), so the config
file must be readable by that user and by nothing else.

---

## Secrets handling

The following variables are secrets and must never be committed to source
control or logged:

| Variable | Secret type |
|---|---|
| `SESSION_SECRET` | Cookie signing key and source of the click-IP hashing key (64 hex chars, 32 bytes of entropy) |
| `DATABASE_URL` | Contains the database password |
| `SES_SMTP_USERNAME` | AWS SES SMTP credential |
| `SES_SMTP_PASSWORD` | AWS SES SMTP credential |

**In development:** keep secrets in the local `.env` file (gitignored).

**In production:** store secrets in `/etc/shortlinks/config.env` on the EC2
instance, with file permissions `0600` owned by the `shortlinks` service
account. Do not store them in AWS SSM Parameter Store, Secrets Manager, or any
other system unless you also update the loader — `config.Load` reads only from
the process environment and the local `.env` file.

`SESSION_SECRET` should be regenerated if it is ever exposed. Changing it
invalidates all active sessions, requiring users to sign in again. It also
changes the click-IP hashing key, which only means hashes from before and
after the change are not comparable; analytics are unaffected.

## Public home page settings (#0174)

These live in the `settings` table and are edited by an admin, not set through
environment variables. `home_mode` (`default`, `about`, `activity`), `home_title`
and `home_about` choose what signed-out visitors see at `/`; `home_redirect_url`
applies only in `default` mode. Modes, validation and the public
`GET /api/public/home` body are described in [auth.md](auth.md#public-home-page-modes-and-get-apipublichome).

## Home page hero image (#0176)

An admin can upload one hero image for the public home page. It is stored in
Postgres (`site_assets`, name `hero`), so it needs no writable disk and the
normal database backups cover it.

**Routes**

| Route | Who | What |
|---|---|---|
| `PUT /admin/home/hero` | admin, browser cookie session (API tokens get 403) | `multipart/form-data` with `image` and `alt`; replaces any existing image and returns `{"hero": {url, alt, width, height}}` |
| `DELETE /admin/home/hero` | same | removes the image (204; 404 if none) |
| `GET /public/hero` | anyone | the stored image; 404 when none is set |

`GET /api/public/home` returns `hero` as `{"url": "/public/hero?v=<sha256 prefix>", "alt", "width", "height"}`, or `null`.

**Upload rules.** Every failure is a 400 whose message says what to change.

- PNG, JPEG or WebP only, identified from the file's bytes. The filename and the
  declared content type are ignored. SVG, GIF and everything else is refused.
- At most 2 MB (also checked again after re-encoding).
- 1200x400 to 4000x2000 pixels, with a width:height ratio from 2:1 to 4:1. The
  dimensions are read from the header before any pixels are decoded.
- `alt` text is required: at most 200 characters, one line.
- The image is decoded and re-encoded, so EXIF, GPS and other metadata never
  reach visitors: JPEG at quality 85, or PNG when the image has transparency.
  WebP and opaque PNG inputs become JPEG. EXIF orientation is not applied, so
  export the image the right way up.

**Serving.** `ETag` is the sha256 of the stored bytes, `X-Content-Type-Options:
nosniff`, and a `Content-Security-Policy` of `default-src 'none'; sandbox`. A
request whose `?v=` matches the current image is cached for a year (`immutable`);
any other request revalidates daily (`max-age=86400`), and `If-None-Match` gets a
304. A new upload changes the hash, so the URL in `/api/public/home` busts caches.
`middleware.Compress` skips `/public/hero` because the bytes are already compressed.

Audit entries `home.hero.updated` and `home.hero.removed` record size,
dimensions and hash, never the bytes.

**Brief for the image author (Codex)**

- Wide banner, **2400x800** (3:1), JPEG or PNG, under 2 MB.
- Keep the subject in the middle third; the page crops the sides on phones to a 4:3 centre.
- No text in the image, so it stays readable at any width and the title can change without regenerating it.
- It must work on both the light and dark page backgrounds.
