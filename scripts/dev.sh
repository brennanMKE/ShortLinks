#!/usr/bin/env bash
#
# dev.sh — start ShortLinks locally on macOS for fast UI iteration.
#
# No PostgreSQL, no systemd, no migrations needed. Uses STORAGE=json (in-memory
# dev store, #0057) and auto-login middleware (#0058) so the dashboard opens
# immediately as the mock admin.
#
# Usage:
#   ./scripts/dev.sh           # hot-reload: Go API on :8080 + Vite dev server on :5173
#   ./scripts/dev.sh --built   # built-SPA: npm build + go run serving on :8080 only
#
# Open in browser:
#   hot-reload mode:  http://localhost:5173  (Vite proxies /api → :8080)
#   built mode:       http://localhost:8080
#
# Override any env var before calling, e.g.:
#   ADMIN_EMAIL=me@example.com ./scripts/dev.sh
#
# Ctrl-C stops everything cleanly.
#
set -euo pipefail

REPO="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$REPO"

# ── Dev environment defaults ─────────────────────────────────────────────────
# All of these can be overridden by setting them in the calling environment.
# DATABASE_URL is intentionally unset: STORAGE=json skips Postgres entirely.
export STORAGE="${STORAGE:-json}"
export BASE_URL="${BASE_URL:-http://localhost:8080}"
export WEBAUTHN_RP_ID="${WEBAUTHN_RP_ID:-localhost}"
export WEBAUTHN_RP_ORIGIN="${WEBAUTHN_RP_ORIGIN:-http://localhost:8080}"
export SESSION_SECRET="${SESSION_SECRET:-dev-session-secret-not-for-production}"
export ADMIN_EMAIL="${ADMIN_EMAIL:-admin@localhost}"
export PORT="${PORT:-8080}"

step() { printf '\n\033[1m==> %s\033[0m\n' "$*"; }
ok()   { printf '    \033[32m✓\033[0m %s\n' "$*"; }
info() { printf '    %s\n' "$*"; }

# ── Parse flags ──────────────────────────────────────────────────────────────
MODE="hot"
case "${1:-}" in
  --built|-b) MODE="built" ;;
  -h|--help) sed -n '2,30p' "$0"; exit 0 ;;
  "") : ;;
  *) printf 'Unknown flag: %s\n' "$1" >&2; exit 1 ;;
esac

# ── Preflight ────────────────────────────────────────────────────────────────
step "Preflight"
for c in go node npm; do
  command -v "$c" >/dev/null 2>&1 || { printf '  ERROR: %s not found\n' "$c" >&2; exit 1; }
done
ok "repo:    $REPO"
ok "storage: $STORAGE (no Postgres)"
ok "admin:   $ADMIN_EMAIL"
ok "port:    $PORT"

# Free the dev ports if a previous run left a server bound. `go run` leaks its
# compiled child process when its parent is killed, so a stale server can keep
# holding :8080 — which both blocks startup ("address already in use") AND keeps
# serving an OLD build. Always start from a clean port.
free_port() {
  local p="$1" pids
  pids="$(lsof -ti tcp:"$p" 2>/dev/null || true)"
  if [ -n "$pids" ]; then
    info "freeing port $p (stale process: $(printf '%s' "$pids" | tr '\n' ' '))"
    # shellcheck disable=SC2086
    kill -9 $pids 2>/dev/null || true
    sleep 1
  fi
}
free_port "$PORT"
free_port 5173

# ── Built-SPA mode ───────────────────────────────────────────────────────────
if [ "$MODE" = "built" ]; then
  step "Building Svelte SPA (web/)"
  ( cd web && { [ -f package-lock.json ] && npm ci --silent || npm install --silent; } && npm run build )
  ok "SPA built into web/dist/"

  step "Starting Go server (embedded SPA) on http://localhost:${PORT}"
  info "Press Ctrl-C to stop."
  printf '\n'
  exec go run ./cmd/shortlinks serve
fi

# ── Hot-reload mode (default) ─────────────────────────────────────────────────
step "Starting Go API server on http://localhost:${PORT}"

# Install npm deps if node_modules is absent or stale.
if [ ! -d web/node_modules ]; then
  info "Installing npm dependencies…"
  ( cd web && npm install --silent )
  ok "npm deps installed"
fi

# Start Go server in background; capture PID for cleanup.
go run ./cmd/shortlinks serve &
GO_PID=$!

cleanup() {
  printf '\n'
  step "Shutting down…"
  kill "$GO_PID" 2>/dev/null || true
  pkill -P "$GO_PID" 2>/dev/null || true   # the go-run child server (go run leaks it otherwise)
  free_port "$PORT" 2>/dev/null || true    # backstop so :PORT is actually released on exit
  # Vite (npm run dev) is the foreground process; it handles its own SIGINT.
  ok "stopped"
}
trap cleanup EXIT INT TERM

# Give the Go server a moment to bind, then validate it's up.
sleep 1
if ! kill -0 "$GO_PID" 2>/dev/null; then
  printf '  ERROR: Go server exited unexpectedly.\n' >&2
  exit 1
fi
ok "Go API server started (pid $GO_PID)"

step "Starting Vite dev server on http://localhost:5173"
info "Vite proxies /api /auth /account /admin /u → http://localhost:${PORT}"
printf '\n'
printf '\033[1m  Open: http://localhost:5173\033[0m\n'
printf '  (logs in automatically as %s)\n' "$ADMIN_EMAIL"
printf '\n'

# Run Vite in foreground — Ctrl-C naturally kills it, then EXIT trap fires.
cd web && npm run dev
