#!/usr/bin/env bash
#
# restore.sh — restore a PostgreSQL backup produced by backup.sh into a target
# database. Handles both formats: custom (.dump → pg_restore) and plain
# (.sql.gz → gunzip | psql).
#
#     # restore the newest shortlinks dump into a scratch DB (a "restore drill")
#     sudo bash scripts/db/restore.sh /var/backups/postgres/shortlinks/shortlinks-latest.dump shortlinks_restore_test
#
#     # restore a specific dump over the live database (DANGEROUS — see below)
#     sudo bash scripts/db/restore.sh /var/backups/postgres/shortlinks/shortlinks-20260625-073001Z.dump shortlinks
#
# Arguments:
#   $1  path to the dump file (.dump or .sql.gz)
#   $2  target database name
#
# Configuration:
#   BACKUP_RUN_AS   OS user to run psql/pg_restore as (default: postgres).
#                   Set empty / to the current user to skip sudo (local dev).
#   RESTORE_CREATE  If "1", create the target database first (must not already
#                   exist). Default: 0 (assume it exists / restore into it).
#
# Safety: this script never drops databases. For a clean-slate restore, create a
# fresh empty target (RESTORE_CREATE=1) or drop/recreate it yourself first.
# Restoring over a live database is destructive at the object level — only do it
# when you mean to.

set -euo pipefail

DUMP="${1:?usage: restore.sh <dump-file> <target-db>}"
TARGET="${2:?usage: restore.sh <dump-file> <target-db>}"
BACKUP_RUN_AS="${BACKUP_RUN_AS:-postgres}"
RESTORE_CREATE="${RESTORE_CREATE:-0}"

[ -f "$DUMP" ] || { echo "ERROR: dump file not found: $DUMP" >&2; exit 2; }

runner=()
if [ -n "$BACKUP_RUN_AS" ] && [ "$BACKUP_RUN_AS" != "$(id -un)" ]; then
  runner=(sudo -u "$BACKUP_RUN_AS")
fi

cd /tmp

echo "Restoring '$DUMP' → database '$TARGET' (as ${BACKUP_RUN_AS:-$(id -un)})"

# ${runner[@]+...} guards an empty array against `set -u` on bash 3.2 (macOS).
if [ "$RESTORE_CREATE" = "1" ]; then
  echo "  creating database '$TARGET'"
  ${runner[@]+"${runner[@]}"} createdb "$TARGET"
fi

case "$DUMP" in
  *.sql.gz)
    gunzip -c "$DUMP" | ${runner[@]+"${runner[@]}"} psql -v ON_ERROR_STOP=1 -d "$TARGET"
    ;;
  *.dump|*)
    # Custom-format archive. --clean --if-exists makes re-restores idempotent.
    ${runner[@]+"${runner[@]}"} pg_restore --no-owner --no-privileges --clean --if-exists \
      -d "$TARGET" "$DUMP"
    ;;
esac

echo "Done. Verify with: psql -d $TARGET -c '\\dt'"
