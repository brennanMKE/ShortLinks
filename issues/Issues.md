# ShortLinks

A self-hosted URL shortener built with Go and PostgreSQL, deployed on AWS EC2 behind Apache 2. Features branded short URLs under `go.sstools.co`, passkey-only authentication (WebAuthn / iCloud Keychain), UTM parameter passthrough, click analytics, URL safety filtering, and an audit log. The management interface is a Svelte 5 SPA embedded in the Go binary.

This file is the local guide for managing issues in this project. The companion Mac app (Issues.app) watches the `issues/` folder and renders the current state. Markdown files and `project.json` are the source of truth.

## Folder layout

```
issues/
├── project.json       # canonical project name + repo URL
├── Issues.md          # this file
├── 0001.md
├── 0001/              # optional sibling folder for screenshots, logs, etc.
└── …
```

## Status values

| File value | Display name | Meaning |
|---|---|---|
| `open` | Open | Filed but not yet started |
| `in-progress` | In Progress | Actively being worked on |
| `resolved` | Resolved | Work done; awaiting confirmation |
| `closed` | Closed | Confirmed complete |
| `wontfix` | Won't Fix | Acknowledged, not addressing |

## Critical rule: never close without explicit confirmation

Never mark an issue `resolved`, `closed`, or `wontfix` based on inference. Only on explicit user instruction.

## Module conventions

| Module | Covers |
|--------|--------|
| `infra` | EC2, Apache, systemd, deployment |
| `config` | `.env`, config loader |
| `db` | PostgreSQL schema, migrations |
| `cache` | Ristretto redirect and filter-rule cache |
| `auth` | WebAuthn, sessions, passkeys |
| `links` | Link domain logic, key generation, deduplication |
| `clicks` | Click recording |
| `filters` | URL filter rules, denial reason codes |
| `audit` | Audit log write path |
| `events` | SSE broker |
| `handlers` | HTTP handlers, routing |
| `middleware` | Logging, rate limiting, session guard |
| `web` | Svelte SPA, Vite, all views |

## Build / verify command

```bash
# Go service
go build ./...
go test ./...

# Svelte SPA
cd web && npm run build

# Full production build
cd web && npm run build && cd .. && go build ./cmd/shortlinks
```

## Git tracking

`issues/` is tracked in this repo. Each lifecycle event produces a commit per the skill's conventions.
