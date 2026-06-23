# Short Links — Documentation

Developer and operator documentation for the Short Links URL shortener. Start
with the architecture overview, then dive into the subsystem you need.

## Overview & setup

- [Architecture overview](architecture.md) — components, request lifecycles, tech stack
- [Configuration & environment variables](configuration.md) — every config value the service reads
- [Deployment & operations](../DEPLOYMENT.md) — EC2 / Apache / systemd setup and redeploys
- [Email / AWS SES](email_setup.md) — SES configuration for magic-link delivery

## Data & backend

- [Database schema & migrations](database.md) — tables, `golang-migrate`, the `seed` command
- [Links, redirects & caching](links.md) — key generation, the `/u/{key}` redirect path, the Ristretto cache
- [Click analytics & metrics](analytics.md) — click recording and the dashboard stats queries
- [UTM / marketing parameters](utm.md) — the UTM builder, redirect passthrough, and analytics
- [URL filtering & safety](url-filters.md) — destination screening, rules, and denial reasons
- [Audit log](audit.md) — recorded actions, write path, and the admin surface
- [Real-time updates (SSE)](events.md) — the events broker and the SPA's live updates

## Authentication

- [Authentication & sessions](auth.md) — sessions, the guard middleware, the registration gate, admin authorization
- [Passkeys / WebAuthn](passkeys.md) — the registration, login, and recovery ceremonies

## Frontend

- [Frontend / SPA](frontend.md) — the Svelte 5 app, the design system, and the build/embed pipeline
