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

## Resolution workflow (review-gated subagents)

Issues are worked **one at a time, in ascending order**, each on its own branch `issue/NNNN` cut from `main`, and nothing reaches `main` until an independent review approves. Three roles:

- **Implementer subagent — model `claude-sonnet-4-6`.** Implements and verifies on the issue branch, committing checkpoints prefixed `#NNNN`. Flips the issue to `in-progress`. Never touches `main`, never edits resolution sections.
- **Reviewer subagent — model `claude-opus-4-8`.** Reviews `git diff main...issue/NNNN` against the issue's acceptance criteria. Returns **Approve** or **Request changes** with a specific list. Never edits code, commits, or changes status.
- **Orchestrator (main session).** Cuts the branch, dispatches both subagents, routes review findings back to the same implementer, records work-log rows, and — only after approval — marks the issue `resolved` and squash-merges to `main` (`git merge --squash`), keeping the branch.

A project override may change the model assignments, but the default is **Sonnet implements, Opus reviews**. Status flow is `open` → `in-progress` → `resolved`; only the user sets `closed`.

**Build-artifact note:** `web/dist/*` is gitignored except the placeholder `web/dist/index.html`; hashed `dist/assets/*` are never committed (production build regenerates them via `cd web && npm run build && go build`). Implementers must NOT commit a real `dist/index.html` build — leave the placeholder.

## Work log & cost tracking

Every implementer and reviewer dispatch appends one row to the issue's `## Work log` section (last section of the file), with exact token counts and cost. Prices live in `issues/model-pricing.json` (refreshed at most once/day from the Anthropic pricing docs). Token counts come from the subagent's transcript, deduped by `requestId`. Cost = (input×input + output×output + cache_read×cache_read + cache_write×cache_write_5m) / 1e6, rounded to the cent. Bailed attempts still get a row — they cost real tokens.

```
## Work log

| Date | Model | Input | Output | Cache read | Cache write | Cost |
|---|---|---|---|---|---|---|
| 2026-06-17 | claude-sonnet-4-6 | 3,992 | 8,349 | 1,316,061 | 52,407 | $0.73 |

**Total: $0.73**
```
