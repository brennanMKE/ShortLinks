#!/usr/bin/env bash
#
# db-status.sh — read-only snapshot of the ShortLinks auth/registration state.
# Run directly on the EC2 instance:
#
#     bash scripts/db-status.sh                     # focus on $ADMIN_EMAIL from .env
#     bash scripts/db-status.sh other@example.com   # focus on a specific email
#
# Uses `sudo -u postgres` peer auth, so no password is needed. cd /tmp first to
# avoid the harmless "could not change directory" warning postgres emits when
# run from a home dir it cannot read.

set -euo pipefail

# Default the email filter to ADMIN_EMAIL. Read it from the project .env when not
# already set in the environment. (Parsed directly rather than `source`d so other
# .env values containing spaces/special chars can't break this script.)
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ENV_FILE="$SCRIPT_DIR/../.env"
if [ -z "${ADMIN_EMAIL:-}" ] && [ -f "$ENV_FILE" ]; then
  ADMIN_EMAIL="$(grep -E '^ADMIN_EMAIL=' "$ENV_FILE" | tail -n1 | cut -d= -f2- | tr -d "\"'")"
fi

DB="${SHORTLINKS_DB:-shortlinks}"
EMAIL="${1:-${ADMIN_EMAIL:-}}"
cd /tmp

psql() { sudo -u postgres /usr/bin/psql -d "$DB" -X -P pager=off "$@"; }

echo "============================================================"
echo " ShortLinks DB status — database: $DB"
[ -n "$EMAIL" ] && echo " filtered to: $EMAIL"
echo "============================================================"

echo
echo "--- registration gate (must be 'true' to allow new signups) ---"
psql -c "SELECT value, updated_at FROM settings WHERE key = 'registrations_enabled';"

echo
echo "--- users ---"
psql -c "SELECT id, email, is_admin, active, created_at, last_login_at
         FROM users ORDER BY id;"

echo
echo "--- pending registrations (magic-link in flight; 5-min TTL) ---"
echo "    expired = link no longer usable; re-request from the site."
psql -c "SELECT id, email, expires_at,
                (expires_at < now()) AS expired,
                round(extract(epoch FROM (expires_at - now()))) AS seconds_left
         FROM pending_registrations
         ${EMAIL:+WHERE email = '$EMAIL'}
         ORDER BY expires_at DESC;"

echo
echo "--- passkey credentials (a row here = a passkey was registered) ---"
psql -c "SELECT pc.id, u.email, pc.device_name, pc.created_at, pc.last_used_at
         FROM passkey_credentials pc JOIN users u ON u.id = pc.user_id
         ${EMAIL:+WHERE u.email = '$EMAIL'}
         ORDER BY pc.id;"

echo
echo "--- recent auth activity (last 20 audit events) ---"
psql -c "SELECT a.created_at, a.action, COALESCE(u.email, '(pre-auth)') AS email, a.ip_address
         FROM audit_log a LEFT JOIN users u ON u.id = a.user_id
         ${EMAIL:+WHERE u.email = '$EMAIL'}
         ORDER BY a.created_at DESC LIMIT 20;"

echo
echo "--- counts ---"
psql -c "SELECT
           (SELECT count(*) FROM users)                  AS users,
           (SELECT count(*) FROM pending_registrations)  AS pending,
           (SELECT count(*) FROM passkey_credentials)    AS passkeys,
           (SELECT count(*) FROM sessions WHERE expires_at > now()) AS active_sessions,
           (SELECT count(*) FROM links)                  AS links;"

echo
echo "Done."
