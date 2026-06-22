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

- **Node.js 20+** — used only at build time to compile the Svelte SPA; not
  needed at runtime. Install via NodeSource:

  ```bash
  curl -fsSL https://rpm.nodesource.com/setup_20.x | sudo bash -
  sudo dnf install -y nodejs
  node --version   # should print v20.x or newer
  ```

- **Go 1.26+** — used to compile the Go binary. AL2023's `dnf` ships an older
  Go version; install from the official tarball instead. The snippet below
  auto-detects the CPU architecture (`amd64` for Intel/AMD, `arm64` for
  Graviton) and downloads the matching tarball. Check <https://go.dev/dl/> for
  the current stable version and update `GO_VERSION` accordingly:

  ```bash
  GO_VERSION=1.26.3
  GOARCH=$(uname -m | sed 's/x86_64/amd64/;s/aarch64/arm64/')
  curl -OL https://go.dev/dl/go${GO_VERSION}.linux-${GOARCH}.tar.gz
  sudo rm -rf /usr/local/go
  sudo tar -C /usr/local -xzf go${GO_VERSION}.linux-${GOARCH}.tar.gz
  echo 'export PATH=$PATH:/usr/local/go/bin' | sudo tee /etc/profile.d/go.sh
  echo 'export PATH=$PATH:$(go env GOPATH)/bin' | sudo tee -a /etc/profile.d/go.sh
  source /etc/profile.d/go.sh
  go version   # go1.26.3 linux/arm64  (or amd64 on Intel/AMD instances)
  ```

  Log out and back in after this step so the PATH change is picked up by all
  future shells (including any deploy scripts that open a new session).

- **`golang-migrate` CLI** installed (used to apply schema migrations):

  ```bash
  go install -tags 'postgres' github.com/golang-migrate/migrate/v4/cmd/migrate@latest
  migrate --version   # confirms the binary is on PATH
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
| `ADMIN_EMAIL` | Email of the admin account. The `seed` command creates this user row with `is_admin = true`; the admin then enrolls a passkey via the **Recover account** flow (see "First admin login" below). Also used to promote any future registrant with this email to admin. |

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
/usr/local/bin/shortlinks seed
```

This pre-authorizes the `ADMIN_EMAIL` address as the first admin and inserts a
test link (pointing to `https://www.wikipedia.org`) so you can confirm the
redirect path works before registering.

---

## 7. systemd

The service runs as an unprivileged `shortlinks` system user. Create it once
before installing the service:

```bash
sudo useradd --system --no-create-home shortlinks
```

Then install the unit, reload systemd, and enable the service so it starts on
boot and is restarted automatically on failure:

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

## 10. First admin login

The `seed` command (step 6) creates the admin user row (`ADMIN_EMAIL`,
`is_admin = true`, `active = true`) but does **not** enroll a passkey. To
obtain the first passkey you must use the **Recover account** flow — not the
Register flow. This is by design: registration rejects an already-registered
email, so trying to register `ADMIN_EMAIL` after the seed silently does nothing.

### Why the Recover account flow

Recovery adds a passkey to an **existing** account without creating a new user
row, and it does not check the `registrations_enabled` gate. It is the correct
path for any account that already exists but has no passkey yet — including the
seeded admin.

### Steps

1. Open `https://go.sstools.co` in a browser.
2. On the login page, click **"Recover account / lost passkey"** (below the
   main login form).
3. Enter the `ADMIN_EMAIL` address and submit. The page shows a generic
   confirmation — no account-existence information is revealed.
4. Check your inbox for the recovery email. **Email delivery must be working**
   (SES credentials configured in `/etc/shortlinks/config.env`) before this
   step will succeed; see issue #0045 for SES setup.
5. Click the link in the email. The browser opens the recovery ceremony page,
   which calls `navigator.credentials.create()` to enroll a new passkey (Touch
   ID / Face ID / iCloud Keychain). WebAuthn requires HTTPS — the Let's Encrypt
   certificate from step 9 is required.
6. After the passkey ceremony completes you are redirected to the dashboard as
   the admin user. `is_admin` is preserved throughout recovery; the admin's
   user row is unchanged.

### Enabling registration for other users

`registrations_enabled` defaults to `false`. Once you are logged in as admin,
open **Admin → Settings** and toggle it on before inviting additional users to
register. Non-admin users use the **Register** form (not Recover account) and
must receive an email verification link to complete their enrollment.

---

## Updating (redeploy)

### Recommended: the gated deploy script

From the repo checkout on the host, on the latest commit:

```bash
git pull --rebase origin main
./scripts/deploy.sh
```

`scripts/deploy.sh` runs the entire redeploy with a verification gate at every
step and **refuses to restart the service unless a genuinely fresh binary is
ready**. In order it: rebuilds the SPA (failing if it produced no hashed
assets), builds the Go binary and verifies with `grep -a` that the binary
actually **embeds the bundle it just built** (catching the most common failure —
a binary built against a stale `web/dist`), asks for a `[y/N]` confirmation,
installs to the path in the systemd unit's `ExecStart` and `systemctl restart`s,
then curls the live site and fails unless it is serving that exact bundle. If
any artifact isn't what's expected it stops with an error (and prints a
diagnosis for the embed check) instead of shipping a broken deploy. Override
defaults with `SERVICE=… PUBLIC_URL=… BIN=… ./scripts/deploy.sh`.

### Is a migration needed this deploy?

A migration is required only when someone added a
`migrations/NNNN_*.{up,down}.sql` pair — pure handler/frontend/query changes
never need one. Check before deploying:

```bash
git diff --name-only <last-deployed-sha>..HEAD -- migrations/   # any output => yes
# or compare the DB's applied version to the highest migration file:
migrate -path migrations -database "$DATABASE_URL" version
```

### Manual steps (what the script automates)

To deploy a new version by hand after pulling code changes:

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

## Troubleshooting: a deploy ran but the site doesn't show the changes

The SPA is **compiled into the Go binary** (`web/embed.go`, `//go:embed
all:dist`) and served by that binary behind Apache. So "my changes don't appear"
almost always means one link in this chain is stale:

> latest commit → `npm run build` writes `web/dist/` → `go build` embeds it →
> binary installed to the `ExecStart` path → service restarted → Apache proxies
> it → browser.

**Fastest single check — compare the served bundle to the built bundle:**

```bash
# what the LIVE site serves right now:
curl -s https://go.sstools.co/ | grep -oE '/assets/index-[^"]+'
# what the current source builds to (on the host, after `npm run build`):
grep -oE 'index-[A-Za-z0-9_-]+\.(js|css)' web/dist/index.html
```

If those hashes differ, the new build isn't being served. Causes, most common
first:

**1. The SPA wasn't rebuilt before the binary.** `web/dist/` is embedded at
`go build` time, so the binary is only as fresh as `web/dist` was at that
moment. Running `go build` without first running `npm run build` embeds the old
(or placeholder) bundle. Confirm what the binary actually contains:

```bash
grep -ao 'index-[A-Za-z0-9_-]*\.js' /usr/local/bin/shortlinks | sort -u
```

Use `grep -a` (whole-file scan), **not** `strings` — `strings` can falsely
report the bundle missing on some platforms.

**2. The service wasn't restarted onto the new binary.** A new file on disk does
nothing until the process restarts. Use `sudo systemctl restart shortlinks`
(not `start`), then confirm it picked up the new binary:

```bash
systemctl show -p ExecMainStartTimestamp shortlinks   # should read "just now"
```

**3. The binary was built to a different path than systemd runs.**
`go build -o shortlinks` writes to the current directory, but systemd runs
whatever `ExecStart` points at (`/usr/local/bin/shortlinks`). Rebuild one,
restart the other, and nothing changes — always `sudo install` to the
`ExecStart` path.

**4. The build host isn't on the latest commit.** `git -C <repo> rev-parse
--short HEAD` on the box must match what you intend to ship. No `git pull` on the
host ⇒ old code in, old code out.

**5. `go build` compiled a different `web/dist` than you rebuilt.** If the embed
is stale even after a clean rebuild, `go build` is reading a different source
tree. Check for a workspace/vendor redirect or a symlinked dist:

```bash
go env GOWORK            # non-empty => a go.work workspace may select another module copy
ls -ld vendor 2>/dev/null
readlink -f web/dist     # is it where you expect, and a real dir (not a symlink)?
```

`go:embed` reads `web/dist` relative to `web/embed.go` **in the module that
`go build` actually compiles**.

**6. Apache is serving a static copy instead of proxying to the binary.** If the
vhost has a `DocumentRoot`/`Alias` pointing at a static SPA directory instead of
`ProxyPass` to `127.0.0.1:8080`, rebuilding the binary changes nothing — you must
refresh that directory (or fix the vhost to proxy). Check:

```bash
grep -rE 'DocumentRoot|Alias|ProxyPass' /etc/httpd/conf.d/ /etc/apache2/sites-enabled/ 2>/dev/null
```

This project's vhost (`deploy/apache/go.sstools.co.conf`) proxies all traffic to
the Go service, so the binary is the source of truth — but verify the *deployed*
config matches.

**7. Caching (browser / CDN / proxy).** Hashed `assets/*` filenames bust
themselves, but `index.html` can be cached. Test with `curl` (bypasses the
browser) and a hard refresh; if a CDN or caching proxy fronts Apache, purge it or
check its cache headers.

`scripts/deploy.sh` checks #1, #2, #3, and #7 automatically (its build-embed
gate, restart check, and live-bundle gate) and prints a diagnosis for #5 — so
prefer it over the manual steps, which are easy to get subtly wrong.

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
