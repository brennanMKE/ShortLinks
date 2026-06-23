# ShortLinks

A self-hosted URL shortener built with Go, PostgreSQL, and Svelte. Deployed on AWS EC2 behind Apache 2, serving branded short URLs on your own domain.

## Features

- **Short URLs** — 6-character base-62 keys, custom aliases supported
- **Passkey authentication** — WebAuthn/FIDO2 only, no passwords; iCloud Keychain sync supported
- **Per-user URL deduplication** — submitting the same destination URL returns the existing link
- **URL filtering** — admin-managed regex rules block malware, phishing, and spam links
- **Click analytics** — UTM parameter capture and breakdown per link
- **Real-time dashboard** — new links appear instantly via Server-Sent Events
- **Audit log** — append-only record of every significant action
- **Admin panel** — user management, URL filter CRUD, registration toggle

## Stack

| Layer | Technology |
|-------|-----------|
| Service | Go (`cmd/shortlinks`) |
| Database | PostgreSQL + `golang-migrate/migrate` |
| Cache | Ristretto (in-process LRU) |
| Auth | WebAuthn (`github.com/go-webauthn/webauthn`) |
| Email | AWS SES (SMTP) |
| Frontend | Svelte 5 + Vite + TypeScript (embedded via `//go:embed`) |
| Host | AWS EC2 + Apache 2 reverse proxy + Let's Encrypt TLS |
| Process | systemd |

## Quick Start (Development)

```bash
# 1. Copy and fill in environment variables
cp .env.example .env

# 2. Start PostgreSQL and run migrations
migrate -path migrations -database "$DATABASE_URL" up

# 3. Seed initial admin user and a test link
go run ./cmd/shortlinks seed

# 4. Start the Go service
go run ./cmd/shortlinks serve

# 5. In a separate terminal, start the Svelte dev server
cd web && npm install && npm run dev
```

The Svelte dev server proxies `/api`, `/auth`, and `/u` to the Go service on `localhost:8080`. Open `http://localhost:5173` in your browser.

## Production Build

```bash
# Build the Svelte SPA (outputs to web/dist/)
cd web && npm run build && cd ..

# Build the Go binary (embeds web/dist/)
go build -o shortlinks ./cmd/shortlinks

# Install and start the systemd service
sudo cp deploy/systemd/shortlinks.service /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable shortlinks
sudo systemctl start shortlinks
```

See [`DEPLOYMENT.md`](DEPLOYMENT.md) for full EC2 setup instructions and [`docs/email_setup.md`](docs/email_setup.md) for AWS SES configuration.

Full developer/operator documentation lives in [`docs/`](docs/README.md) — architecture, configuration, database, auth, passkeys, links, analytics, UTM, URL filtering, audit, events, and the frontend.

## Configuration

All configuration is via environment variables. Copy `.env.example` to `.env` and fill in the values. In production, `/etc/shortlinks/config.env` is loaded by the systemd unit.

Key variables:

| Variable | Description |
|----------|-------------|
| `DATABASE_URL` | PostgreSQL connection string |
| `BASE_URL` | Public base URL (e.g. `https://go.example.com`) |
| `SESSION_SECRET` | HMAC signing key — generate with `openssl rand -hex 32` |
| `WEBAUTHN_RP_ID` | WebAuthn relying party ID (must match domain) |
| `ADMIN_EMAIL` | Email of the first admin user |
| `SES_SMTP_*` | AWS SES SMTP credentials |

**`.env` must never be committed to source control.**

## Project Layout

```
cmd/shortlinks/       # main package
internal/
  config/             # config loader
  db/                 # PostgreSQL pool + migrations
  cache/              # Ristretto cache (redirect + filter rules)
  auth/               # WebAuthn, sessions, mailer
  links/              # link creation, key generation, deduplication
  filters/            # URL filter rule loading and regex evaluation
  clicks/             # click recording
  audit/              # audit log write path
  events/             # SSE broker
  handlers/           # HTTP handlers
  middleware/         # auth guard, rate limiter, logging
migrations/           # SQL up/down migration files
web/                  # Svelte SPA
deploy/
  apache/             # Apache virtual host config
  systemd/            # systemd service unit
issues/               # Issue tracker
```

## License

MIT — see [LICENSE](LICENSE).
