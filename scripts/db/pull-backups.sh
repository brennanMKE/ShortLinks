#!/usr/bin/env bash
#
# pull-backups.sh — pull the EC2 PostgreSQL backup tree to this machine (the Mac
# mini) over rsync-over-SSH. Run THIS on the Mac mini, not on the server.
#
# The pull model is deliberate: the server never holds credentials for the Mac
# mini, so an instance compromise cannot reach the offsite copy. Because every
# database lives under one common root on the server, a single rsync mirrors all
# of them at once.
#
#     bash scripts/db/pull-backups.sh
#
# Schedule it a bit after the server's nightly dump window (e.g. server dumps at
# 07:30 UTC, the Mac pulls at 08:00 UTC). On macOS prefer a launchd agent; cron
# works too.
#
# ── Configuration (override via environment) ─────────────────────────────────
#   BACKUP_SSH_HOST    user@host of the EC2 box.   Default: ec2-user@go.sstools.co
#   BACKUP_REMOTE_DIR  Remote backup root.         Default: /var/backups/postgres/
#   BACKUP_LOCAL_DIR   Local mirror destination.   Default: ~/Backups/postgres/
#   BACKUP_SSH_KEY     Path to a dedicated SSH key. Default: ssh default key
#
# Use a dedicated, restricted SSH key for this (ideally a read-only command or a
# user that can only read the backup tree).

set -euo pipefail

SSH_HOST="${BACKUP_SSH_HOST:-ec2-user@go.sstools.co}"
REMOTE_DIR="${BACKUP_REMOTE_DIR:-/var/backups/postgres/}"
LOCAL_DIR="${BACKUP_LOCAL_DIR:-$HOME/Backups/postgres/}"
SSH_KEY="${BACKUP_SSH_KEY:-}"

# Ensure trailing slashes so rsync mirrors the *contents* of the root.
REMOTE_DIR="${REMOTE_DIR%/}/"
LOCAL_DIR="${LOCAL_DIR%/}/"

mkdir -p "$LOCAL_DIR"

ssh_cmd="ssh"
[ -n "$SSH_KEY" ] && ssh_cmd="ssh -i $SSH_KEY"

echo "Pulling $SSH_HOST:$REMOTE_DIR → $LOCAL_DIR"

# --delete-after mirrors server-side pruning (old dumps removed locally too, but
# only after a successful transfer). --partial-dir keeps interrupted transfers
# out of the live tree.
rsync -avz --delete-after --partial --partial-dir=.rsync-partial \
  -e "$ssh_cmd" \
  "$SSH_HOST:$REMOTE_DIR" "$LOCAL_DIR"

echo "Done. Local mirror: $LOCAL_DIR"
