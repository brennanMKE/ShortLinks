# Deploying ShortLinks

End-to-end instructions for deploying ShortLinks on an AWS EC2 instance running
**Amazon Linux 2023 (AL2023)**, and for redeploying after a code change.
ShortLinks is a single Go binary (with the Svelte SPA embedded via `//go:embed`)
sitting behind Apache (`httpd`), backed by PostgreSQL, and managed by systemd.
The service listens on `127.0.0.1:8080`; Apache terminates TLS for
`go.sstools.co` and reverse-proxies to it.

If you are running your own instance under a different domain, substitute your
domain wherever `go.sstools.co` appears and set `BASE_URL`, `WEBAUTHN_RP_ID`, and
`WEBAUTHN_RP_ORIGIN` in the config accordingly — no code change is required.

---

## 1. Prerequisites

Provision and prepare the host before deploying:

- **EC2 instance** running Amazon Linux 2023.
- **Apache (`httpd`)** installed with SSL support. On AL2023 the proxy modules
  are included in the base `httpd` package — no `a2enmod` step is needed:

  ```bash
  sudo dnf install -y httpd mod_ssl
  sudo systemctl enable --now httpd
  ```

- **PostgreSQL** installed and initialised. AL2023 requires an explicit
  `--initdb` before the first start:

  ```bash
  sudo dnf install -y postgresql15-server postgresql15
  sudo postgresql-setup --initdb
  sudo systemctl enable --now postgresql
  ```

- **Node.js 20+** and **Go 1.22+** installed (used to build the SPA and the Go
  binary). Verify with:

  ```bash
  node --version   # v20.x or newer
  go version       # go1.22 or newer
  ```

- **`golang-migrate` CLI** installed (used to apply schema migrations):

  ```bash
  go install -tags 'postgres' github.com/golang-migrate/migrate/v4/cmd/migrate@latest
  # ensure $(go env GOPATH)/bin is on your PATH so `migrate` is found
  ```

- **DNS**: the wildcard `*.sstools.co` (and therefore `go.sstools.co`) already
  resolves to this EC2 instance's public IP. Confirm with
  `dig +short go.sstools.co` before requesting a TLS certificate.

- **`openssl` and `certbot`** available:

  ```bash
  sudo dnf install -y openssl certbot python3-certbot-apache
  ```

Clone the repository onto the instance (e.g. into `/opt/shortlinks`) and run all
build commands from the repo root unless noted otherwise:

```bash
sudo git clone https://github.com/brennanMKE/ShortLinks.git /opt/shortlinks
cd /opt/shortlinks
```

---

## 2. Database setup

The one-time bootstrap creates the application **login role** and **database**.
It does not create any tables — the schema is owned by `golang-migrate` and is
applied in the Migrations step below.

**Set the password first.** `scripts/db/create.sql` ships with the placeholder
password `CHANGE_ME_IN_PRODUCTION`. Edit the file and replace it with a real,
strong secret, and keep that secret out of source control:

```bash
# scripts/db/create.sql, in the CREATE ROLE line:
CREATE ROLE shortlinks LOGIN PASSWORD '<your-strong-password>';
```

Then create the role and database as the `postgres` superuser:

```bash
sudo -u postgres psql -f scripts/db/create.sql
```

> On AL2023, the `postgres` system user is created by the `postgresql15-server`
> package. If `sudo -u postgres psql` fails with "must be run as root", ensure
> the PostgreSQL service is running (`sudo systemctl status postgresql`) and that
> `postgresql-setup --initdb` completed successfully.

The role creation is idempotent. `CREATE DATABASE` cannot be guarded with
`IF NOT EXISTS` in PostgreSQL, so re-running against an existing database raises
a harmless "database already exists" error you can ignore.

> To reset a development database to a clean slate (this **permanently destroys
> all data** — never run in production):
>
> ```bash
> sudo -u postgres psql -f scripts/db/drop.sql
> ```

---

## 3. Configuration

All runtime configuration is loaded from environment variables. In production
these live in `/etc/shortlinks/config.env`, which the systemd unit loads.

Copy the example file from the repo and edit it in place:

```bash
sudo mkdir -p /etc/shortlinks
sudo cp .env.example /etc/shortlinks/config.env
sudo chmod 600 /etc/shortlinks/config.env
sudo nano /etc/shortlinks/config.env
```

Fill in every value. The variables present in `.env.example` are:

| Variable | Notes |
|----------|-------|
| `PORT` | Local port the Go service listens on. Keep `8080` to match the Apache vhost. |
| `BASE_URL` | Public base URL, e.g. `https://go.sstools.co`. |
| `DATABASE_URL` | `postgres://shortlinks:<your-password>@localhost:5432/shortlinks?sslmode=disable` — the password **must** match the one you set in `scripts/db/create.sql`. |
| `WEBAUTHN_RP_ID` | The bare domain the browser is on, e.g. `go.sstools.co`. Must match exactly. |
| `WEBAUTHN_RP_ORIGIN` | Full origin, e.g. `https://go.sstools.co`. |
| `SESSION_SECRET` | Random 32-byte hex string used to HMAC-sign session cookies. Generate one (see below); never commit it. |
| `SES_SMTP_HOST` | AWS SES SMTP endpoint, e.g. `email-smtp.us-east-1.amazonaws.com`. |
| `SES_SMTP_PORT` | SMTP port, typically `587`. |
| `SES_SMTP_USERNAME` | IAM-derived SES SMTP username. |
| `SES_SMTP_PASSWORD` | IAM-derived SES SMTP password (not an AWS root key). |
| `EMAIL_FROM` | From address for verification/recovery email, e.g. `ShortLinks <noreply@sstools.co>`. |
| `CACHE_MAX_COST` | Max cached redirect entries (default `10000`). |
| `CACHE_TTL_SECONDS` | Redirect cache entry TTL in seconds (default `300`). |
| `ADMIN_EMAIL` | Email of the first admin. When the first user registers with this address, they are promoted to admin. Used only once. |

Generate the session secret with:

```bash
openssl rand -hex 32
```

Paste the output as the `SESSION_SECRET` value. Rotating this secret later
invalidates all active sessions.

---

## 4. Build

Build the Svelte SPA first; its output (`web/dist/`) is embedded into the Go
binary at compile time. Then build the binary:

```bash
cd web && npm ci && npm run build
cd ..
go build -o shortlinks ./cmd/shortlinks
```

This produces a `shortlinks` binary in the repo root with the front-end assets
embedded. Move it to a stable location referenced by the systemd unit:

```bash
sudo install -m 0755 shortlinks /usr/local/bin/shortlinks
```

---

## 5. Migrations

Apply the database schema with `golang-migrate`, pointing it at the migration
files in `migrations/` and the database URL from your config:

```bash
export DATABASE_URL='postgres://shortlinks:<your-password>@localhost:5432/shortlinks?sslmode=disable'
migrate -path migrations -database "$DATABASE_URL" up
```

Migration files are named `NNNN_description.{up,down}.sql` and run in order. Run
this again on every deploy that introduces new migrations (see **Updating**).

---

## 6. Seed

Create the admin user (from `ADMIN_EMAIL`) and a test link. The seed command is
idempotent and safe to re-run:

```bash
go run ./cmd/shortlinks seed
```

This pre-authorizes the `ADMIN_EMAIL` address as the first admin and inserts a
test link (pointing to `https://www.wikipedia.org`) so you can confirm the
redirect path works before registering.

---

## 7. systemd

Install the service unit, then enable and start the service so it runs on boot
and is restarted on failure.

> The unit file lives at `deploy/systemd/shortlinks.service` in the repo. It is
> tracked by issue **#0012** and may not exist yet — once that issue lands, the
> file at that path is what you install here. The unit runs
> `/usr/local/bin/shortlinks`, loads `/etc/shortlinks/config.env` via
> `EnvironmentFile=`, and listens on `127.0.0.1:8080`.

```bash
sudo cp deploy/systemd/shortlinks.service /etc/systemd/system/shortlinks.service
sudo systemctl daemon-reload
sudo systemctl enable --now shortlinks
sudo systemctl status shortlinks
```

Confirm the service is healthy from the host before fronting it with Apache:

```bash
curl -fsS http://127.0.0.1:8080/health
```

---

## 8. Apache

Install the virtual host config from the repo and reload Apache. On AL2023,
drop the file directly into `/etc/httpd/conf.d/` — any `.conf` file in that
directory is loaded automatically; there is no `a2ensite` command. The vhost
terminates TLS and proxies all traffic to `127.0.0.1:8080`, with `/api/events`
proxied using `flushpackets=on` so Server-Sent Events flush immediately.

```bash
sudo cp deploy/apache/go.sstools.co.conf /etc/httpd/conf.d/
sudo systemctl reload httpd
```

---

## 9. TLS

The virtual host references certificate paths under
`/etc/letsencrypt/live/go.sstools.co/`. Obtain a Let's Encrypt certificate with
Certbot, which will also wire it into the Apache config:

```bash
sudo certbot --apache -d go.sstools.co
```

Certbot installs a renewal timer automatically. After it completes, reload
Apache once more if Certbot did not already do so:

```bash
sudo systemctl reload httpd
```

Verify HTTPS end to end:

```bash
curl -fsS https://go.sstools.co/health
```

---

## 10. First login

1. Open `https://go.sstools.co` in a browser.
2. Register an account using the **`ADMIN_EMAIL`** address you set in
   `/etc/shortlinks/config.env`. Because no users exist yet and the email
   matches `ADMIN_EMAIL`, this first account is promoted to admin
   (`is_admin = true`).
3. Complete the email verification magic link, then the device passkey ceremony
   (Touch ID / Face ID / iCloud Keychain). WebAuthn requires HTTPS, which the
   Let's Encrypt step above provides.
4. You are redirected to the dashboard. From the Admin view you can open
   registration for additional users (`registrations_enabled`), manage URL
   filter rules, and review the audit log.

---

## Updating (redeploy)

To deploy a new version after pulling code changes:

```bash
cd /opt/shortlinks
git pull

# 1. Rebuild the SPA and the binary
cd web && npm ci && npm run build
cd ..
go build -o shortlinks ./cmd/shortlinks
sudo install -m 0755 shortlinks /usr/local/bin/shortlinks

# 2. Apply any new migrations
export DATABASE_URL='postgres://shortlinks:<your-password>@localhost:5432/shortlinks?sslmode=disable'
migrate -path migrations -database "$DATABASE_URL" up

# 3. Restart the service
sudo systemctl restart shortlinks
sudo systemctl status shortlinks
curl -fsS https://go.sstools.co/health
```

If a deploy only changes configuration, edit `/etc/shortlinks/config.env` and run
`sudo systemctl restart shortlinks` — no rebuild needed. If the systemd unit file
itself changed, re-copy it and run `sudo systemctl daemon-reload` before
restarting.

---

## Verification

This is a documentation deliverable, verified by inspection against the repo's
actual files. Every artifact path cited above was confirmed to exist in the repo
— `scripts/db/create.sql`, `scripts/db/drop.sql`, `.env.example`, and
`deploy/apache/go.sstools.co.conf` — and the configuration variables match
`.env.example` exactly (including the `shortlinks`/`shortlinks` role/database and
the `CHANGE_ME_IN_PRODUCTION` password placeholder). The systemd unit at
`deploy/systemd/shortlinks.service` is intentionally forward-referenced to issue
#0012 and does not yet exist. `go build ./...` was run to confirm the Go build is
unaffected by adding this document.
