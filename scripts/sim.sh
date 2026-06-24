#!/usr/bin/env bash
#
# sim.sh — load the local ShortLinks app in the iOS Simulator's Safari and
#           capture a screenshot for mobile-rendering verification.
#
# Requires:
#   - Xcode with at least one iOS Simulator runtime installed
#   - The app already running locally (e.g. via ./scripts/dev.sh --built or
#     a manually started Go server on :8080)
#
# Usage:
#   ./scripts/sim.sh [URL] [DEVICE_NAME] [OUTPUT_PATH]
#
#   URL          URL to open in Simulator Safari (default: http://localhost:8080)
#   DEVICE_NAME  Simulator device name to use  (default: "iPhone 17")
#   OUTPUT_PATH  Where to write the screenshot  (default: /tmp/shortlinks-sim.png)
#
# Examples:
#   ./scripts/sim.sh
#   ./scripts/sim.sh http://localhost:8080
#   ./scripts/sim.sh http://localhost:8080 "iPhone 17 Pro" ~/Desktop/dashboard.png
#
# Notes:
#   - If the named device is already booted, it is reused (no error).
#   - If multiple runtimes are installed, the first device with DEVICE_NAME wins.
#   - Use --help or -h to print this header.
#
set -euo pipefail

# ── Args ─────────────────────────────────────────────────────────────────────
case "${1:-}" in -h|--help) sed -n '2,36p' "$0"; exit 0 ;; esac

URL="${1:-http://localhost:8080}"
DEVICE_NAME="${2:-iPhone 17}"
OUT="${3:-/tmp/shortlinks-sim.png}"

step()  { printf '\n\033[1m==> %s\033[0m\n' "$*"; }
ok()    { printf '    \033[32m✓\033[0m %s\n' "$*"; }
info()  { printf '    %s\n' "$*"; }
error() { printf '\033[31mERROR: %s\033[0m\n' "$*" >&2; }

# ── Preflight ────────────────────────────────────────────────────────────────
step "Preflight"

if ! command -v xcrun >/dev/null 2>&1; then
  error "xcrun not found — is Xcode installed?"
  exit 1
fi

if ! xcrun simctl help >/dev/null 2>&1; then
  error "xcrun simctl not available — check your Xcode installation."
  exit 1
fi

ok "xcrun simctl available"
ok "device:  $DEVICE_NAME"
ok "url:     $URL"
ok "output:  $OUT"

# ── Resolve device UDID ──────────────────────────────────────────────────────
step "Resolving device"

# Find the UDID of the first device matching DEVICE_NAME (exact name match).
# Prefer "Booted" devices; otherwise take the first available one.
UDID=$(
  xcrun simctl list devices available -j 2>/dev/null \
  | python3 -c "
import json, sys
data = json.load(sys.stdin)
devices = []
for runtime, devlist in data.get('devices', {}).items():
    for d in devlist:
        if d.get('name') == '${DEVICE_NAME}' and d.get('isAvailable', False):
            devices.append(d)
# Prefer already-booted
booted = [d for d in devices if d.get('state') == 'Booted']
chosen = (booted or devices)
if chosen:
    print(chosen[0]['udid'])
" 2>/dev/null || true
)

if [ -z "$UDID" ]; then
  error "No available device named '$DEVICE_NAME' found."
  info  "Available devices:"
  xcrun simctl list devices available | grep -v "^==" | grep -v "^--" | grep -v "^$" | head -20 >&2
  exit 1
fi

ok "UDID: $UDID"

# ── Boot if needed ───────────────────────────────────────────────────────────
step "Booting simulator"

STATE=$(xcrun simctl list devices | grep "$UDID" | grep -oE '\(Booted\)|\(Shutdown\)' | tr -d '()' || echo "Unknown")

if [ "$STATE" = "Booted" ]; then
  ok "Already booted — reusing"
else
  info "Booting $DEVICE_NAME ($UDID)…"
  xcrun simctl boot "$UDID"
  ok "Boot command issued"
fi

# Give Springboard time to finish loading (skip if already booted).
if [ "$STATE" != "Booted" ]; then
  info "Waiting for Springboard to be ready…"
  WAIT=0
  until xcrun simctl spawn "$UDID" launchctl list >/dev/null 2>&1 || [ $WAIT -ge 30 ]; do
    sleep 2
    WAIT=$((WAIT + 2))
  done
  ok "Simulator ready (${WAIT}s)"
fi

# ── Open URL in Safari ───────────────────────────────────────────────────────
step "Opening URL in Safari"

xcrun simctl openurl "$UDID" "$URL"
ok "URL sent: $URL"

# Wait for Safari to load the page — adjust if the app is slow to respond.
# 6 s is enough on a warmed simulator; increase if the Go server is slow to start.
info "Waiting for page load…"
sleep 6

# ── Capture screenshot ───────────────────────────────────────────────────────
step "Capturing screenshot"

xcrun simctl io "$UDID" screenshot "$OUT"
ok "Screenshot saved: $OUT"

printf '\n\033[1mDone.\033[0m  Open the screenshot:\n'
printf '  open "%s"\n\n' "$OUT"
