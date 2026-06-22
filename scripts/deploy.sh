#!/usr/bin/env bash
#
# deploy.sh — build, verify, and (after a Y/n gate) restart the ShortLinks service.
#
# Run on the production host as your normal user (it sudo's only for the install
# + restart steps). Pull latest first, then run from the repo root:
#
#     git pull --rebase origin main
#     ./scripts/deploy.sh
#
# The script FAILS FAST (non-zero exit) at any point where the build does not
# actually produce the expected, fresh artifact — so it can never restart the
# service onto a stale binary. It asks for a Y/n confirmation before restarting.
#
# Gates, in order:
#   1. SPA build produced hashed assets (not the committed placeholder).
#   2. The freshly built Go binary actually EMBEDS that fresh SPA bundle.
#   3. [Y/n] you confirm — only then is the binary installed + the service
#      restarted.
#   4. The service is active after restart.
#   5. The LIVE site is serving the exact bundle we just built (catches the
#      "Apache serves a stale static dir / a proxy is caching" case).
#
# Override defaults with env vars:
#   SERVICE=shortlinks  PUBLIC_URL=https://go.sstools.co  BIN=/path/to/binary
#
set -euo pipefail

case "${1:-}" in -h|--help) sed -n '2,30p' "$0"; exit 0 ;; esac

SERVICE="${SERVICE:-shortlinks}"
PUBLIC_URL="${PUBLIC_URL:-https://go.sstools.co}"
REPO="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$REPO"

step(){ printf '\n\033[1m==> %s\033[0m\n' "$*"; }
ok(){   printf '    \033[32m\xe2\x9c\x93\033[0m %s\n' "$*"; }
info(){ printf '    %s\n' "$*"; }
die(){  printf '\n\033[31m\xe2\x9c\x97 ERROR: %s\033[0m\n' "$*" >&2; exit 1; }

# ---- preflight -------------------------------------------------------------
step "Preflight"
for c in git go npm curl systemctl strings; do
  command -v "$c" >/dev/null 2>&1 || die "required command not found: $c"
done
HEAD_SHA="$(git rev-parse --short HEAD)"
ok "repo:   $REPO"
ok "commit: $HEAD_SHA — $(git log -1 --pretty=%s)"
[ -n "$(git status --porcelain)" ] && info "note: working tree has uncommitted changes"

# Resolve the binary path systemd actually runs, so we build to the SAME file.
BIN="${BIN:-}"
if [ -z "$BIN" ]; then
  BIN="$(systemctl show -p ExecStart --value "$SERVICE" 2>/dev/null \
         | grep -oE 'path=[^ ;]+' | head -1 | cut -d= -f2 || true)"
fi
[ -n "$BIN" ] || die "could not determine the binary path from systemd unit '$SERVICE'. Set BIN=/path/to/binary and re-run."
ok "service '$SERVICE' runs: $BIN"

# Record what the live site serves now, for a before/after comparison.
LIVE_BEFORE="$(curl -fsS --max-time 15 "$PUBLIC_URL/" 2>/dev/null \
               | grep -oE 'index-[A-Za-z0-9_-]+\.js' | head -1 || true)"
info "live now: ${LIVE_BEFORE:-<unreachable>}"

# ---- gate 1: build the SPA, confirm it produced hashed assets --------------
step "Building the Svelte SPA (web/)"
( cd web && { [ -f package-lock.json ] && npm ci || npm install; } && npm run build )
BUILT_JS="$(grep -oE 'index-[A-Za-z0-9_-]+\.js'  web/dist/index.html | head -1 || true)"
BUILT_CSS="$(grep -oE 'index-[A-Za-z0-9_-]+\.css' web/dist/index.html | head -1 || true)"
{ [ -n "$BUILT_JS" ] && [ -n "$BUILT_CSS" ]; } \
  || die "web/dist/index.html references no hashed assets — the SPA build did not run (still the committed placeholder index.html?)."
[ -f "web/dist/assets/$BUILT_JS" ] || die "expected built asset web/dist/assets/$BUILT_JS is missing after build."
ok "SPA built: $BUILT_JS, $BUILT_CSS"

# ---- gate 2: build the binary, confirm it EMBEDS this fresh bundle ----------
step "Building the Go binary (embeds web/dist/)"
TMPBIN="$(mktemp)"; trap 'rm -f "$TMPBIN"' EXIT
go build -o "$TMPBIN" ./cmd/shortlinks
strings "$TMPBIN" | grep -qF "$BUILT_JS" \
  || die "the freshly built binary does NOT embed the fresh SPA bundle ($BUILT_JS). Aborting before the service is touched. (Did 'npm run build' regenerate web/dist before 'go build'?)"
ok "binary built and verified to embed $BUILT_JS"

# ---- gate 3: confirmation before restart -----------------------------------
step "Ready to deploy — review before restarting"
info "commit:        $HEAD_SHA"
info "binary target: $BIN"
info "live bundle:   ${LIVE_BEFORE:-<unknown>}"
info "new bundle:    $BUILT_JS"
[ -n "$LIVE_BEFORE" ] && [ "$LIVE_BEFORE" = "$BUILT_JS" ] \
  && info "note: the new bundle is identical to what's already live — no visible change expected."
printf '\n'
read -r -p "Install new binary to $BIN and restart '$SERVICE'? [y/N] " ans
case "${ans:-}" in
  [yY]|[yY][eE][sS]) : ;;
  *) trap - EXIT; info "Aborted before restart. The verified fresh binary is left at: $TMPBIN"; exit 0 ;;
esac

# ---- install + restart -----------------------------------------------------
step "Installing binary and restarting '$SERVICE'"
sudo install -m 0755 "$TMPBIN" "$BIN"
ok "installed $BIN"
sudo systemctl restart "$SERVICE"
sleep 2
systemctl is-active --quiet "$SERVICE" \
  || die "'$SERVICE' is not active after restart — inspect: journalctl -u $SERVICE -n 50"
ok "'$SERVICE' restarted and active"

# ---- gate 5: confirm the LIVE site serves the fresh bundle ------------------
step "Verifying the live site serves the fresh bundle"
LIVE_AFTER=""
for _ in $(seq 1 10); do
  LIVE_AFTER="$(curl -fsS --max-time 15 "$PUBLIC_URL/" 2>/dev/null \
                | grep -oE 'index-[A-Za-z0-9_-]+\.js' | head -1 || true)"
  [ "$LIVE_AFTER" = "$BUILT_JS" ] && break
  sleep 2
done
[ "$LIVE_AFTER" = "$BUILT_JS" ] \
  || die "live site still serves '${LIVE_AFTER:-<none>}' but we built '$BUILT_JS'. The binary is updated, so something ELSE is serving the SPA — check whether Apache serves a static dir instead of proxying to the Go service, or a CDN/proxy is caching:
    grep -rE 'DocumentRoot|Alias|ProxyPass' /etc/apache2/sites-enabled/ /etc/httpd/conf.d/ 2>/dev/null"
ok "live site is serving $BUILT_JS"
printf '\n\033[1m\xe2\x9c\x85 Deploy complete: %s is live at %s\033[0m\n' "$HEAD_SHA" "$PUBLIC_URL"
