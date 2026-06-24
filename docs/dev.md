# Local Development

Run the full ShortLinks app on your Mac for fast UI iteration — **no PostgreSQL,
no systemd, no migrations required.**

## Quick start (hot-reload)

```bash
./scripts/dev.sh
```

Then open **http://localhost:5173** in your browser.

You land on the dashboard already signed in as the mock admin (`admin@localhost`).
No passkey ceremony, no setup.

## What happens

- The Go API server starts on `:8080` using `STORAGE=json` — an in-memory dev
  store (#0057) pre-seeded with a mock admin user.
- The dev auto-login middleware (#0058) injects a session cookie on every
  unauthenticated request, so the dashboard is immediately accessible.
- The Vite dev server starts on `:5173` and proxies `/api`, `/auth`, `/account`,
  `/admin`, and `/u` to the Go server on `:8080`.
- Svelte HMR keeps the browser in sync with source changes in `web/src/`.
- Ctrl-C stops both processes cleanly.

## Built-SPA mode

To test the production embedding (the same `//go:embed` path used in production):

```bash
./scripts/dev.sh --built
```

This runs `npm run build` first, then `go run ./cmd/shortlinks serve`, serving
the embedded SPA at **http://localhost:8080**. Useful for verifying asset
embedding, favicon, and SPA deep-link handling before deploying.

## Environment variables

All variables have sensible defaults — no `.env` file is needed. Override any
of them before calling the script:

| Variable | Default | Description |
|----------|---------|-------------|
| `STORAGE` | `json` | Must stay `json` for dev mode |
| `BASE_URL` | `http://localhost:8080` | Public base URL |
| `WEBAUTHN_RP_ID` | `localhost` | WebAuthn relying-party ID |
| `WEBAUTHN_RP_ORIGIN` | `http://localhost:8080` | WebAuthn origin |
| `SESSION_SECRET` | *(dev value)* | HMAC signing key — insecure, dev only |
| `ADMIN_EMAIL` | `admin@localhost` | Mock admin email |
| `PORT` | `8080` | Go server port |

Example override:

```bash
ADMIN_EMAIL=me@example.com PORT=9090 ./scripts/dev.sh
```

## Production path is unchanged

`scripts/dev.sh` does not touch `scripts/deploy.sh`, the systemd unit, or any
Postgres migration files. The dev store is engaged solely by `STORAGE=json` and
is refused if that variable is not set.
