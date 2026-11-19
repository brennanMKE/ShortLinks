#!/usr/bin/env bash
#
# mcp-smoke.sh — check that a ShortLinks /mcp endpoint works end to end.
#
# Usage:
#   SHORTLINKS_TOKEN=slk_… scripts/mcp-smoke.sh https://go.example.com
#   SHORTLINKS_TOKEN=slk_… scripts/mcp-smoke.sh            # defaults to http://localhost:8080
#
# The token comes from the environment, never argv (argv shows up in `ps`).
# It is also kept out of curl's argv by passing the header through stdin.
#
# Runs initialize, tools/list and a whoami tool call, prints the tool count,
# and exits non-zero on the first failure. Needs only bash and curl.
#
set -euo pipefail

BASE="${1:-${SHORTLINKS_URL:-http://localhost:8080}}"
BASE="${BASE%/}"
TOKEN="${SHORTLINKS_TOKEN:-}"

if [ -z "$TOKEN" ]; then
  echo "error: set SHORTLINKS_TOKEN to an API token (see docs/mcp.md)" >&2
  exit 2
fi

if ! [[ $TOKEN =~ ^slk_[A-Za-z0-9_-]+$ ]]; then
  echo "error: SHORTLINKS_TOKEN is not shaped like an API token (slk_ followed by letters, digits, - or _)" >&2
  exit 2
fi

fail() { echo "FAIL: $*" >&2; exit 1; }

# rpc <json-body> -> prints the response body; fails unless HTTP 200.
rpc() {
  local out status
  # -K - reads curl options from stdin so the Authorization header is not in argv.
  out=$(printf 'header = "Authorization: Bearer %s"\n' "$TOKEN" |
    curl -sS -K - --max-time 30 -w '\n%{http_code}' \
      -H 'Content-Type: application/json' \
      -H 'Accept: application/json, text/event-stream' \
      -X POST --data "$1" "$BASE/mcp") || fail "could not reach $BASE/mcp"
  status="${out##*$'\n'}"
  out="${out%$'\n'*}"
  if [ "$status" != "200" ]; then
    fail "HTTP $status from $BASE/mcp: $out"
  fi
  case "$out" in
    *'"error":{'*) fail "JSON-RPC error: $out" ;;
  esac
  printf '%s' "$out"
}

echo "Checking $BASE/mcp"

init=$(rpc '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"mcp-smoke","version":"1"}}}')
case "$init" in
  *'"serverInfo"'*) echo "ok  initialize" ;;
  *) fail "initialize returned no serverInfo: $init" ;;
esac

tools=$(rpc '{"jsonrpc":"2.0","id":2,"method":"tools/list"}')
# Each tool has exactly one "inputSchema" key at its top level; nested schema
# properties never use that name, so counting it counts tools.
count=$(printf '%s' "$tools" | { grep -o '"inputSchema"' || true; } | wc -l | tr -d ' ')
[ "$count" -gt 0 ] || fail "tools/list returned no tools: $tools"
echo "ok  tools/list: $count tools"

who=$(rpc '{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"whoami","arguments":{}}}')
case "$who" in
  *'"isError":true'*) fail "whoami returned a tool error: $who" ;;
  *'"result"'*) echo "ok  whoami" ;;
  *) fail "unexpected whoami response: $who" ;;
esac

echo "PASS: $count tools"
