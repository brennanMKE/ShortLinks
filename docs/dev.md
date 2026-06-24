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

---

## Verifying mobile rendering (iOS Simulator)

The iOS Simulator runs **genuine mobile Safari**, so it can expose rendering
differences that desktop browsers hide — e.g. `datetime-local` picker sizing
(#0053), responsive layout (#0050), and dark-mode colours (#0051).

### Prerequisites

- **Xcode** installed (App Store or `xcode-select --install`)
- At least one **iOS Simulator runtime** installed  
  (Xcode → Settings → Platforms → add an iOS runtime if none is listed)
- The ShortLinks app must be **running locally** before opening the Simulator

### Quick workflow

```bash
# 1. Start the app (built-SPA mode so the simulator hits the embedded assets)
./scripts/dev.sh --built

# 2. In a second terminal, boot the simulator and open the app
./scripts/sim.sh

# 3. The script prints the screenshot path; open it to review
open /tmp/shortlinks-sim.png
```

### `scripts/sim.sh` options

```
./scripts/sim.sh [URL] [DEVICE_NAME] [OUTPUT_PATH]
```

| Argument | Default | Description |
|----------|---------|-------------|
| `URL` | `http://localhost:8080` | URL to open in Safari |
| `DEVICE_NAME` | `iPhone 17` | Simulator device name |
| `OUTPUT_PATH` | `/tmp/shortlinks-sim.png` | Where to save the screenshot |

Examples:

```bash
# Default — iPhone 17 at localhost:8080
./scripts/sim.sh

# Different device
./scripts/sim.sh http://localhost:8080 "iPhone 17 Pro"

# Custom output path
./scripts/sim.sh http://localhost:8080 "iPhone 17" ~/Desktop/dashboard.png
```

If the simulator is already booted, `sim.sh` reuses it (no error).

### What to check

- **Responsive layout** (#0050): nav collapses, table scrolls horizontally, no
  clipped content at 390 px viewport width.
- **Dark mode** (#0051): colours match the design on both Light and Dark system
  settings (toggle in the Simulator's Settings app or via the Feature menu).
- **Expires field** (#0053): the `datetime-local` input in the Create Short Link
  form should match the height and width of the adjacent text inputs.
- **Favicon** (#0061): the favicon should appear in the Safari address bar and
  in the browser's tab strip.

### Limitations

- This is **manual visual inspection**, not automated E2E testing. There are no
  assertions; you review the screenshots yourself.
- First boot of a simulator can take 30–60 seconds.
- `sim.sh` always uses the first matching device name across all installed
  runtimes; if you have both iOS 17 and iOS 18 runtimes you may get either one.
  Pass the UDID directly via `xcrun simctl boot <UDID>` for precise control.
- The simulator must be able to reach `localhost` on the Mac (it always can —
  the simulator shares the Mac's network stack).
