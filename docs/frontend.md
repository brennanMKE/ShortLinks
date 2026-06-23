# Frontend — Svelte 5 SPA

The management UI is a Svelte 5 single-page application (SPA) embedded directly into the Go binary. It is the only client for the `/api`, `/auth`, `/account`, and `/admin` HTTP namespaces.

---

## Directory layout

```
web/
├── index.html              # Shell HTML — mounts #app, sets viewport + color-scheme meta
├── vite.config.ts          # Vite config: outDir=dist, dev-server proxy rules
├── svelte.config.js        # vitePreprocess() for <script lang="ts"> blocks
├── tsconfig.json           # Strict ESNext, moduleResolution=bundler
├── package.json            # Scripts: dev / build / check / test
├── embed.go                # //go:embed all:dist; exports DistFS()
├── dist/
│   ├── index.html          # Committed placeholder (keeps go build clean pre-npm-build)
│   └── assets/             # Gitignored hashed JS/CSS emitted by npm run build
└── src/
    ├── main.ts             # Entry point: imports app.css, mounts App
    ├── app.css             # Design tokens, global styles, responsive + dark-mode overrides
    ├── App.svelte          # Root: view selection / routing logic
    └── lib/
    │   ├── stores.ts       # Svelte writable stores: currentView, currentUser, links, …
    │   ├── api.ts          # Typed fetch wrapper + all endpoint helpers
    │   ├── types.ts        # TypeScript interfaces mirroring Go JSON shapes
    │   ├── branding.ts     # APP_NAME constant ("Short Links")
    │   ├── links.ts        # Short-URL builder, URL validation, denial labels, link status
    │   ├── events.ts       # SSE client for /api/events (live link.created stream)
    │   ├── utm.ts          # UTM-parameter composition helpers
    │   ├── linkDetail.ts   # UTM bucket sorting, empty-stats detection, status label
    │   ├── account.ts      # Passkey management helpers, last-credential guard
    │   ├── admin.ts        # Reason codes, deactivation validation, audit formatting
    │   ├── charts.ts       # Pure SVG-geometry helpers for line/bar charts
    │   ├── Panel.svelte    # Bounded-box primitive
    │   ├── Button.svelte   # Button primitive (four variants)
    │   ├── ClicksChart.svelte   # SVG line chart (clicks over time)
    │   └── UTMBarChart.svelte   # Horizontal proportional bar chart (UTM dimension)
    └── views/
        ├── Login.svelte         # Passkey auth: conditional UI autofill + explicit sign-in
        ├── Dashboard.svelte     # Link create form + paginated link list + SSE updates
        ├── LinkDetail.svelte    # Single-link stats: clicks chart, UTM breakdown, edit/deactivate
        ├── Account.svelte       # Passkey management: list, rename, revoke
        ├── Admin.svelte         # Admin tabs: settings, URL filters, users, audit log
        ├── RegisterVerify.svelte  # Magic-link registration landing → passkey creation
        └── RecoverVerify.svelte   # Magic-link recovery landing → passkey creation
```

---

## Entry point and mounting

**`web/src/main.ts`** is the Vite entry point. It imports the global stylesheet then mounts the root component into `#app` using Svelte 5's imperative `mount()` API:

```ts
import { mount } from 'svelte';
import './app.css';
import App from './App.svelte';

const app = mount(App, { target: document.getElementById('app')! });
```

**`web/index.html`** provides the minimal shell. It sets the mobile viewport, declares `<meta name="color-scheme" content="light dark">` so native form controls and scrollbars follow the OS theme, and holds `<div id="app"></div>` as the mount target. There is no server-side rendering.

---

## View selection (App.svelte)

`App.svelte` is the router. There is no URL-based router library: the active view is a write to the `currentView` Svelte store, and `App.svelte` renders the matching component in a chain of `{#if}` / `{:else if}` blocks.

On mount it does the following, in order:

1. **Magic-link detection** — if `window.location.pathname` is `/register/verify` or `/recover/verify`, it reads the `?token=` query param, saves it to the `pendingVerifyToken` store, strips the token from the URL with `history.replaceState`, and sets the view to `register-verify` or `recover-verify` without calling the API. This ensures a user who opens an email link without an active session is routed to the passkey-creation flow rather than bounced to login.

2. **Session check** — for all other paths it calls `GET /api/me`. A successful response sets `currentUser` and navigates to `'dashboard'`; a 401 (or any other error) navigates to `'login'`.

The full set of view identifiers is typed in `web/src/lib/stores.ts` as the `View` union:

```ts
export type View =
  | 'login' | 'dashboard' | 'link-detail'
  | 'account' | 'admin'
  | 'register-verify' | 'recover-verify';
```

---

## Views

| View | File | Purpose |
|---|---|---|
| Login | `views/Login.svelte` | Two-path WebAuthn auth: conditional-UI (passkey autofill) fires in background on mount; explicit "Sign in" button calls `/auth/login/start` then `/auth/login/finish`. Registration and recovery sub-forms POST to `/auth/register/start` and `/auth/recover`. |
| Dashboard | `views/Dashboard.svelte` | Link create form (destination URL, optional title/key/expiry, UTM builder) + paginated link list. Opens an SSE connection to `/api/events` on mount and prepends `link.created` events to the `links` store in real time. |
| LinkDetail | `views/LinkDetail.svelte` | Full stats for a link selected via the `selectedLinkKey` store. Shows the short URL, destination, status, dates, total click count, a `ClicksChart` (30-day timeseries), and `UTMBarChart` breakdowns by source/medium/campaign. Inline edit and deactivate actions. |
| Account | `views/Account.svelte` | Lists the user's registered passkeys; supports inline rename and revoke with a guard against revoking the last credential. |
| Admin | `views/Admin.svelte` | Admin-only (gated on `currentUser.is_admin`). Four tabs: Settings (registrations toggle), URL filters (CRUD + dry-run test), Users (list, deactivate, reactivate), Audit log (paginated, filterable by user id). |
| RegisterVerify | `views/RegisterVerify.svelte` | Landing view for the registration magic-link. Reads `pendingVerifyToken`, calls `/auth/register/verify?token=…` to get `PublicKeyCredentialCreationOptions`, invokes `navigator.credentials.create()`, then posts the attestation to `/auth/register/finish`. |
| RecoverVerify | `views/RecoverVerify.svelte` | Same flow as RegisterVerify but targets `/auth/recover/verify` and `/auth/recover/finish`. Adds a credential to the existing account rather than creating a new one. |

---

## Shared state (stores.ts)

`web/src/lib/stores.ts` holds all global reactive state as Svelte `writable` stores. No store mutation happens in `lib/`-layer helpers — only views write to stores.

| Store | Type | Purpose |
|---|---|---|
| `currentView` | `View` | Active view; write to navigate |
| `currentUser` | `User \| null` | Authenticated user profile from `/api/me`; `null` = signed out |
| `links` | `Link[]` | Dashboard link list; populated by REST and extended by SSE |
| `selectedLinkKey` | `string \| null` | Key of the link currently open in LinkDetail |
| `pendingVerifyToken` | `string \| null` | Magic-link token captured by App.svelte on landing |

---

## API client (api.ts)

`web/src/lib/api.ts` is a typed fetch wrapper. All requests are same-origin (`credentials: 'include'` for the session cookie), accept JSON, and throw a typed `ApiError` on non-2xx responses. `ApiError` carries `status` (the HTTP status code) and `body` (the parsed JSON, typically `{error: "…"}`).

The file exposes four low-level primitives (`apiGet`, `apiPost`, `apiPatch`, `apiDelete`) and typed endpoint helpers for every route the views call — link CRUD, auth ceremonies, credential management, and all admin endpoints. Views import named helpers rather than building paths by hand.

---

## Domain helpers (lib/*.ts)

Each helper module is pure TypeScript with no DOM or Svelte dependencies, making every function unit-testable with Vitest without a browser environment.

| Module | Key exports |
|---|---|
| `links.ts` | `shortUrl`, `isValidHttpUrl`, `linkStatus`, `destinationDomain`, `deniedReasonLabel`, `noticeForCreated`, `noticeForError`. Holds `SHORT_URL_BASE = 'https://go.sstools.co'` — the production display base used for all generated short-link URLs shown in the UI. |
| `events.ts` | `subscribeLinks` (opens `/api/events` EventSource, invokes callback per `link.created` frame), `prependUniqueByKey` (pure dedup-prepend used by the Dashboard store update). |
| `utm.ts` | `composeUtmUrl` — merges non-empty UTM fields onto a base URL via `URLSearchParams`, replacing existing same-named params; falls back to string manipulation when the base is not yet a valid URL (for live preview while typing). |
| `linkDetail.ts` | `utmDimensions`, `sortBuckets`, `isEmptyStats`, `statusLabel`, `formatDate`. |
| `account.ts` | `validateDeviceName`, `canRevoke`, `revokeErrorMessage`, `lastUsedLabel`, `formatDate`. Maps the backend's `409 cannot_revoke_last_credential` to a human message. |
| `admin.ts` | `REASON_OPTIONS`, `DEACTIVATION_REASONS`, `validateDeactivation`, `canDeactivate`, `filterTestNotice`, `actorLabel`, `targetLabel`, `formatMetadata`, `formatDateTime`, `pageInfo`, `parseUserIdFilter`, `registrationsEnabled`. |
| `charts.ts` | `fillDayGaps`, `toTimeseriesPoints`, `toPolylinePoints`, `yAxisTicks`, `yCoord`, `toBarRows`. Pure SVG-geometry helpers; all arithmetic stays here so chart components stay thin. |
| `webauthn.ts` | Base64url ↔ `ArrayBuffer` encoding (`bufferToBase64url`, `base64urlToBytes`), conversion of server JSON options to `PublicKeyCredentialRequestOptions` / `CreationOptions`, and serialization of browser assertion/attestation results to the JSON shapes the Go server expects. Must match Go's `base64.RawURLEncoding` exactly. |

---

## Branding (branding.ts)

`web/src/lib/branding.ts` is the single source of truth for the product name shown in the UI:

```ts
export const APP_NAME = 'Short Links';
```

Every view that shows the brand in a heading or the `.app-title` header imports `APP_NAME` from this file. Functional values — the `SHORT_URL_BASE` in `links.ts` and the WebAuthn `rp.id`/`rp.name` in `webauthn.ts` — are left separate and are not affected by branding changes.

---

## Design system

### Tokens (app.css)

`web/src/app.css` is the global stylesheet, imported once from `main.ts`. It defines all design tokens as CSS custom properties on `:root`:

**Colors — neutrals**

| Token | Light value | Dark value |
|---|---|---|
| `--bg-page` | `#f6f7f8` | `#0d1117` |
| `--bg-panel` | `#ffffff` | `#161b22` |
| `--bg-subtle` | `#f0f1f3` | `#1c2128` |
| `--bg-header` | `#eceef0` | `#21262d` |

**Colors — text**

| Token | Light | Dark |
|---|---|---|
| `--text` | `#1c1e21` | `#e6edf3` |
| `--text-muted` | `#65676b` | `#8b949e` |
| `--text-faint` | `#8a8d91` | `#8b949e` |

**Colors — accent (single)**

| Token | Light | Dark |
|---|---|---|
| `--accent` | `#2d5fa3` | `#4d8ef0` |
| `--accent-hover` | `#244e87` | `#6aa3f5` |
| `--accent-text` | `#ffffff` | `#0d1117` |
| `--accent-subtle` | `#e7eef7` | `#1a2a45` |

**Status colors** — `--success`, `--danger`, `--warning` plus `--success-subtle`, `--danger-subtle`, `--danger-hover-bg` for surface tints (badge backgrounds, notice boxes, Button danger hover).

**Spacing** — 8px grid: `--space-1` (4px) through `--space-6` (32px).

**Typography** — `--font` (system-ui stack), `--font-mono` (monospace stack), `--fs-sm` (12px) / `--fs-base` (13px) / `--fs-md` (15px) / `--fs-lg` (18px) / `--fs-xl` (22px), `--lh` (1.45).

**Shape** — `--radius` (4px), `--border-w` (1px).

### Global element styling

`app.css` also provides global styling for `body`, all form input types (`input[type="text"]`, `select`, `textarea`, date/time fields), `label`, `table`/`thead`/`tbody`, and the `.field` wrapper. These apply everywhere without class names so views get consistent form controls and bordered dense tables without per-component styles.

Date and time inputs get `-webkit-appearance: none` so iOS Safari renders them as styled boxes that obey `width: 100%` rather than native controls that overflow their container.

### Utility classes

`app.css` defines the following utility classes used across views:

| Class(es) | Purpose |
|---|---|
| `.app-shell` | Centered content column (`max-width: 1040px`, auto margins, padding) |
| `.app-header` | Bordered panel bar for the title + nav tabs + sign-out button |
| `.app-title` | 18px semibold heading in the header |
| `.app-columns` | Two-column grid (main + 280px sidebar); collapses to single column at ≤720px |
| `.table-scroll` | Horizontal scroll container for wide tables on mobile (iOS momentum scrolling) |
| `.badge`, `.badge-success`, `.badge-danger`, `.badge-muted` | Inline pill badges for link status |
| `.text-muted`, `.text-faint` | Dim text at `--text-muted` and `--text-faint` |
| `.text-error`, `.text-notice`, `.text-warn` | Inline status messages below form fields |
| `.row`, `.spread` | Flex row helpers |
| `.divider` | 1px horizontal rule using `--border` |
| `.sr-only` | Visually hidden (screen-reader only) |

### Panel primitive (Panel.svelte)

`web/src/lib/Panel.svelte` is a bordered box that wraps content sections. Props:

- `title?: string` — when provided, renders a `.panel-header` bar (`--bg-header` background, 1px bottom border, 600 weight) above the body.
- `noPadding?: boolean` — omits the default `var(--space-4)` body padding; used when the body is a flush `<table>`.
- `children: Snippet` — the body content.

All colors are token-driven; Panel itself needs no changes when the theme switches.

### Button primitive (Button.svelte)

`web/src/lib/Button.svelte` provides a consistent button with four variants:

| Variant | Appearance |
|---|---|
| `default` | `--bg-subtle` fill, `--border-strong` border, `--text` label |
| `primary` | `--accent` fill, `--accent-text` label; hover darkens to `--accent-hover` |
| `subtle` | Transparent fill, `--accent` text; hover adds `--accent-subtle` background |
| `danger` | `--bg-subtle` fill, `--danger` text; hover switches to `--danger-hover-bg` |

Props: `variant`, `type` (`button`/`submit`/`reset`), `disabled`, `onclick`. Svelte 5 has no `on:event` forwarding; `onclick` is taken via `$props()` and additional attributes are spread with `{...rest}`. On mobile (`≤480px`) Button adds `min-height: 40px` and extra vertical padding to ensure reachable tap targets.

### SVG chart components

**`ClicksChart.svelte`** renders a responsive inline SVG line chart for clicks over time. Props: `timeseries: TimeseriesResult | null`, `days` (default 30), `title`. The SVG uses `width="100%"` with a `viewBox` so it scales to its container. Y-axis grid lines, tick labels, and the area fill all use CSS custom properties so both themes work without per-component overrides.

**`UTMBarChart.svelte`** renders a horizontal proportional bar chart for one UTM dimension (source, medium, or campaign). Each row shows the value name, a filled bar whose width is `pct%` of the dimension's total, and a count. The underlying data table is visible and serves as the accessible fallback; bars are `aria-hidden` decorations.

---

## Light and dark mode

Dark mode is implemented entirely via token-value restatement in `app.css` — no component CSS changes:

```css
@media (prefers-color-scheme: dark) {
  :root {
    --bg-page:    #0d1117;
    --bg-panel:   #161b22;
    /* … all 18 color tokens … */
  }
}
```

The palette is a GitHub Primer-style dark scale. The accent is brightened from `#2d5fa3` to `#4d8ef0` to maintain contrast on dark backgrounds. The last literal hex tints that would break in dark (Button danger hover, Dashboard denied-box background, badge tints) were converted to `--danger-hover-bg`, `--danger-subtle`, and `--success-subtle` tokens so the dark override restates them in one place.

`color-scheme: light dark` on `:root` (in `app.css`) and `<meta name="color-scheme" content="light dark">` in `index.html` instruct the browser to apply dark styling to native form controls and scrollbars.

The SPA currently follows the OS setting only. An explicit light/dark/system toggle is a possible follow-up (would require one header button + a few lines of `localStorage` logic).

---

## Responsiveness

All responsive rules live in a single `@media (max-width: 480px)` block in `app.css` (covering iPhone SE at 375px through iPhone Pro Max at 430px). All rules are additive — the desktop layout is untouched above 480px and no hardcoded hex appears in the block.

Key behaviors at ≤480px:

- `.app-shell` gutter tightens from `var(--space-5)` to `var(--space-4)`.
- `.app-header` adds `flex-wrap: wrap` so the title, nav tabs, and sign-out stack onto multiple rows instead of competing for horizontal space.
- Wide tables (Dashboard link list, Admin sub-sections) are wrapped in `.table-scroll` inside their `Panel` so they scroll horizontally within a bounded container rather than overflowing the viewport.
- The `LinkDetail` field grid collapses from two columns to one.
- Account credential cards stack vertically.
- `Button` gains `min-height: 40px` tap targets.
- Login/RegisterVerify/RecoverVerify shell uses `width: 100%` with reduced top margin.

Charts (`ClicksChart`) were already `viewBox` + `width: 100%` and scale to mobile without additional rules.

---

## Build and embed

### Vite build

`web/package.json` defines four scripts:

| Script | What it does |
|---|---|
| `npm run dev` | Start Vite dev server with API proxy (see below) |
| `npm run build` | `vite build` — emits hashed assets to `web/dist/` |
| `npm run check` | `svelte-check` — type-check all `.svelte` and `.ts` files |
| `npm test` | `vitest run` — run all unit tests once |

`vite.config.ts` sets `build.outDir = 'dist'` and `emptyOutDir = true`. There is no additional Vite configuration beyond the `@sveltejs/vite-plugin-svelte` plugin.

### Go embed

`web/embed.go` embeds the built `dist/` directory into the Go binary at compile time:

```go
//go:embed all:dist
var distFS embed.FS

func DistFS() fs.FS {
    sub, err := fs.Sub(distFS, "dist")
    // …
    return sub
}
```

The `all:` prefix ensures Vite's dot-prefixed or underscore-prefixed output files are included. `DistFS()` returns an `fs.FS` rooted at `dist/` so callers see `index.html` and `assets/*` at the root — the Go HTTP handler serves these directly.

### The dist/index.html placeholder

`web/dist/index.html` is committed to the repository as a minimal placeholder:

```html
<!doctype html><title>ShortLinks</title><div id="app"></div>
```

This placeholder satisfies the `//go:embed all:dist` directive so `go build ./...` and `go test ./...` compile from a clean checkout before any `npm run build` has run. `.gitignore` ignores `web/dist/*` but whitelists `!web/dist/index.html`. The real Vite build overwrites `index.html` with the full document and emits hashed `dist/assets/index-*.js` and `dist/assets/index-*.css`, which remain gitignored. Developers working the codebase must NOT commit a real built `dist/index.html`.

### Full production build sequence

```bash
cd web && npm run build   # emits web/dist/assets/* + overwrites web/dist/index.html
cd ..  && go build ./cmd/shortlinks   # embeds web/dist/ into the binary
```

### Development (Vite proxy vs production embedded serving)

In development, `npm run dev` starts the Vite dev server (default port 5173). `vite.config.ts` proxies five path prefixes to the Go service on `:8080`:

```
/api      → http://localhost:8080
/auth     → http://localhost:8080
/account  → http://localhost:8080
/admin    → http://localhost:8080
/u        → http://localhost:8080
```

This means the browser sees everything as same-origin; the session cookie, WebAuthn origin checks, and CORS all behave identically to production. Developers run both `go run ./cmd/shortlinks` (or the compiled binary) and `npm run dev` simultaneously.

In production the Go binary serves the embedded SPA directly. The Go HTTP mux routes every unmatched non-API `GET /` request to `index.html` (the SPA catch-all), and the Vite-built `assets/` files are served from the embedded filesystem. No separate Node.js process or static file server is needed.
