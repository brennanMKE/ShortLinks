#!/usr/bin/env bash
#
# backup.sh — nightly logical backup of one or more PostgreSQL databases into a
# common backup root, so a single rsync grabs every database at once.
#
# Designed to run on the EC2 host (Amazon Linux 2023) from cron, but it works
# anywhere pg_dump can reach the cluster. Run directly:
#
#     # back up the databases named in BACKUP_DATABASES (or the project default)
#     sudo bash scripts/db/backup.sh
#
#     # back up specific databases
#     sudo bash scripts/db/backup.sh shortlinks otherdb
#
# Each database is dumped with `pg_dump -Fc` (PostgreSQL's compressed custom
# format, restored with pg_restore) into its own subfolder under BACKUP_ROOT:
#
#     /var/backups/postgres/
#     ├── shortlinks/
#     │   ├── shortlinks-20260625-073001Z.dump   # timestamped, compressed
#     │   └── shortlinks-latest.dump -> …          # convenience symlink
#     └── otherdb/
#         └── otherdb-20260625-073002Z.dump
#
# A Mac mini (or any offsite host) then pulls the whole tree in one shot:
#
#     rsync -avz --delete-after ec2-user@<host>:/var/backups/postgres/ ~/Backups/postgres/
#
# Each dump is written to a *.tmp file and atomically renamed on success, so a
# concurrent rsync never copies a half-written file. After a successful dump the
# database's folder is pruned of dumps older than BACKUP_RETENTION_DAYS.
#
# ── Configuration (all overridable via environment) ──────────────────────────
#   BACKUP_ROOT             Common backup root.        Default: /var/backups/postgres
#   BACKUP_DATABASES        Space/comma list of DBs.   Default: parsed from ../../.env
#   BACKUP_RETENTION_DAYS   Prune dumps older than N.  Default: 14
#   BACKUP_FORMAT           custom | plain.            Default: custom
#                             custom → <db>-<ts>.dump  (pg_restore)
#                             plain  → <db>-<ts>.sql.gz (gunzip | psql)
#   BACKUP_RUN_AS           OS user to run pg_dump as. Default: postgres
#                             Uses `sudo -u <user>` for peer auth on the server.
#                             Set empty (or to the current user) to skip sudo —
#                             handy for local dev where you own the cluster.
#
# Exit status is non-zero if ANY database fails, so cron/monitoring can alert.

set -euo pipefail

# ── Resolve configuration ────────────────────────────────────────────────────
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ENV_FILE="$SCRIPT_DIR/../../.env"

BACKUP_ROOT="${BACKUP_ROOT:-/var/backups/postgres}"
BACKUP_RETENTION_DAYS="${BACKUP_RETENTION_DAYS:-14}"
BACKUP_FORMAT="${BACKUP_FORMAT:-custom}"
BACKUP_RUN_AS="${BACKUP_RUN_AS:-postgres}"

# Databases: positional args win; else $BACKUP_DATABASES; else the db name from
# the project .env DATABASE_URL (parsed, not sourced, so other values can't break
# this script); else "shortlinks".
if [ "$#" -gt 0 ]; then
  DATABASES=("$@")
elif [ -n "${BACKUP_DATABASES:-}" ]; then
  # split on commas and/or whitespace
  IFS=', ' read -r -a DATABASES <<<"$BACKUP_DATABASES"
else
  db_from_env=""
  if [ -f "$ENV_FILE" ]; then
    url="$(grep -E '^DATABASE_URL=' "$ENV_FILE" | tail -n1 | cut -d= -f2- | tr -d "\"'")"
    # postgres://user:pass@host:port/DBNAME?params  → DBNAME
    db_from_env="$(printf '%s' "$url" | sed -E 's#^.*/([^/?]+)(\?.*)?$#\1#')"
  fi
  DATABASES=("${db_from_env:-shortlinks}")
fi

# ── pg_dump runner (peer auth as the postgres user on the server) ─────────────
runner=()
if [ -n "$BACKUP_RUN_AS" ] && [ "$BACKUP_RUN_AS" != "$(id -un)" ]; then
  runner=(sudo -u "$BACKUP_RUN_AS")
fi

# cd somewhere world-readable so the postgres user doesn't emit the harmless
# "could not change directory to ..." warning when run from a home dir.
cd /tmp

timestamp="$(date -u +%Y%m%d-%H%M%SZ)"

case "$BACKUP_FORMAT" in
  custom) ext="dump" ;;
  plain)  ext="sql.gz" ;;
  *) echo "ERROR: BACKUP_FORMAT must be 'custom' or 'plain', got '$BACKUP_FORMAT'" >&2; exit 2 ;;
esac

echo "============================================================"
echo " PostgreSQL backup — $(date -u +'%Y-%m-%d %H:%M:%SZ')"
echo " root:       $BACKUP_ROOT"
echo " databases:  ${DATABASES[*]}"
echo " format:     $BACKUP_FORMAT (.$ext)   retention: ${BACKUP_RETENTION_DAYS}d"
echo " run as:     ${BACKUP_RUN_AS:-$(id -un)}"
echo "============================================================"

# Lock down the root: backups contain session tokens, passkey credentials, and
# the audit log. Owner-only access.
mkdir -p "$BACKUP_ROOT"
chmod 0700 "$BACKUP_ROOT" 2>/dev/null || true

failures=0

for db in "${DATABASES[@]}"; do
  [ -n "$db" ] || continue
  dir="$BACKUP_ROOT/$db"
  base="$db-$timestamp.$ext"
  final="$dir/$base"
  tmp="$final.tmp"

  echo
  echo "--- $db → $final"
  mkdir -p "$dir"
  chmod 0700 "$dir" 2>/dev/null || true

  # ${runner[@]+...} guards against an empty array tripping `set -u` on bash 3.2
  # (macOS); on the server's bash 5.x it's equivalent to a bare "${runner[@]}".
  set +e
  if [ "$BACKUP_FORMAT" = "custom" ]; then
    ${runner[@]+"${runner[@]}"} pg_dump -Fc --no-owner --no-privileges -d "$db" >"$tmp"
    rc=$?
  else
    # Plain SQL piped through gzip. PIPESTATUS[0] is pg_dump's status.
    ${runner[@]+"${runner[@]}"} pg_dump --no-owner --no-privileges -d "$db" | gzip >"$tmp"
    rc=${PIPESTATUS[0]}
  fi
  set -e

  if [ "$rc" -ne 0 ] || [ ! -s "$tmp" ]; then
    echo "ERROR: pg_dump for '$db' failed (rc=$rc) — leaving previous backups untouched" >&2
    rm -f "$tmp"
    failures=$((failures + 1))
    continue
  fi

  chmod 0600 "$tmp"
  mv -f "$tmp" "$final"                      # atomic publish
  ln -sfn "$base" "$dir/$db-latest.$ext"     # convenience pointer to newest
  echo "    ok — $(du -h "$final" | cut -f1)"

  # Prune old dumps for THIS database only (after a successful fresh write, so we
  # can never prune away the only copy — today's dump has age 0).
  pruned="$(find "$dir" -maxdepth 1 -type f -name "$db-*.$ext" \
              -mtime +"$BACKUP_RETENTION_DAYS" -print -delete | wc -l | tr -d ' ')"
  [ "$pruned" -gt 0 ] && echo "    pruned $pruned dump(s) older than ${BACKUP_RETENTION_DAYS}d"
done

echo
if [ "$failures" -gt 0 ]; then
  echo "FAILED: $failures database(s) did not back up. See errors above." >&2
  exit 1
fi
echo "Done — all databases backed up."
