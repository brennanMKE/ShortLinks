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
| `SESSION_SECRET` | **required** | — | 32-byte (64 hex character) random secret used to sign session cookies. Generate with `openssl rand -hex 32`. |
| `SES_SMTP_HOST` | optional | `""` | AWS SES SMTP hostname (e.g. `email-smtp.us-east-1.amazonaws.com`). Leave blank to disable email delivery. |
| `SES_SMTP_PORT` | optional | `587` | TCP port for the SES SMTP connection. |
| `SES_SMTP_USERNAME` | optional | `""` | SMTP username issued by AWS SES. |
| `SES_SMTP_PASSWORD` | optional | `""` | SMTP password issued by AWS SES. |
| `EMAIL_FROM` | optional | `""` | RFC 5322 sender address used in outgoing email (e.g. `ShortLinks <noreply@sstools.co>`). |
| `CACHE_MAX_COST` | optional | `10000` | Maximum cost budget for the in-memory Ristretto cache (redirect targets + filter rules). Units are arbitrary cost units passed to Ristretto. |
| `CACHE_TTL_SECONDS` | optional | `300` | Default TTL in seconds for cache entries (5 minutes). |
| `ADMIN_EMAIL` | **required** | — | Email address of the bootstrap admin user. On first startup with an empty database, this address is pre-authorized as admin when the user completes passkey registration. |

**Required fields:** `BASE_URL`, `DATABASE_URL`, `WEBAUTHN_RP_ID`,
`WEBAUTHN_RP_ORIGIN`, `SESSION_SECRET`, `ADMIN_EMAIL`. The loader collects all
missing-required and invalid-integer errors in a single pass and returns them
together; see [Validation behavior](#validation-behavior) below.

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
| `SESSION_SECRET` | Cookie signing key (64 hex chars, 32 bytes of entropy) |
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
invalidates all active sessions, requiring users to sign in again.
