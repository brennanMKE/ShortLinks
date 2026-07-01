# Database backups

Automated nightly logical backups of the PostgreSQL database(s), with an offsite
copy pulled to a home Mac mini. Three scripts under [`scripts/db/`](../scripts/db)
cover the whole lifecycle:

| Script | Runs on | Purpose |
|--------|---------|---------|
| [`backup.sh`](../scripts/db/backup.sh) | EC2 (cron) | `pg_dump` each database into a common backup root, with rotation |
| [`pull-backups.sh`](../scripts/db/pull-backups.sh) | Mac mini | `rsync` the whole backup root offsite |
| [`restore.sh`](../scripts/db/restore.sh) | anywhere | Restore a dump into a target database (and run restore drills) |

## Design

- **Logical dumps, not file copies.** `pg_dump` produces a transactionally
  consistent snapshot. A `tar` of the live data directory is *not*
  crash-consistent and can restore to a broken cluster — we don't do that.
- **One common root, many databases.** Every database is dumped into its own
  subfolder under a single root (default `/var/backups/postgres/`), so one
  `rsync` mirrors all of them at once. Add a second database later and it just
  appears as a new subfolder — the pull picks it up automatically.

  ```
  /var/backups/postgres/            ← BACKUP_ROOT (one rsync grabs everything)
  ├── shortlinks/
  │   ├── shortlinks-20260625-073001Z.dump
  │   ├── shortlinks-20260626-073001Z.dump
  │   └── shortlinks-latest.dump -> shortlinks-20260626-073001Z.dump
  └── otherdb/
      └── otherdb-20260626-073002Z.dump
  ```

- **Pull, don't push.** The Mac mini pulls over SSH. The server never holds
  Mac mini credentials, so an instance compromise can't reach the offsite copy.
- **Atomic writes.** Each dump goes to a `*.tmp` file and is renamed on success,
  so a concurrent rsync never copies a half-written file.
- **Rotation.** After a successful dump, each database's folder is pruned of
  dumps older than the retention window (default 14 days). Pruning runs only
  after a fresh dump lands, so the newest copy can never be pruned away.
- **Sensitive content.** Dumps contain session tokens, passkey credentials, and
  the audit log. The root and per-database folders are `0700` and dump files are
  `0600`. Transport is over SSH. See [Encryption at rest](#encryption-at-rest).

## Server setup (EC2 / Amazon Linux 2023)

1. **Confirm the client tools are installed** (they ship with the `postgresqlNN`
   packages; match the client major version to the server):

   ```bash
   pg_dump --version
   ```

2. **Create the backup root** owned by the `postgres` user:

   ```bash
   sudo install -d -o postgres -g postgres -m 0700 /var/backups/postgres
   ```

3. **Test a run by hand** (as `postgres`, peer auth — no password needed):

   ```bash
   sudo bash scripts/db/backup.sh
   ```

   By default it backs up the database named in the project `.env`
   `DATABASE_URL`. To back up several, pass them explicitly or set
   `BACKUP_DATABASES`:

   ```bash
   sudo bash scripts/db/backup.sh shortlinks otherdb
   sudo BACKUP_DATABASES="shortlinks otherdb" bash scripts/db/backup.sh
   ```

4. **Schedule it nightly** in the postgres user's crontab
   (`sudo crontab -u postgres -e`). Pick an off-peak UTC time and log the output:

   ```cron
   # Nightly PostgreSQL backup at 07:30 UTC
   30 7 * * *  /usr/bin/bash /opt/shortlinks/scripts/db/backup.sh >> /var/log/pg-backup.log 2>&1
   ```

   Adjust the repo path to wherever the code is deployed. The script logs a
   header and per-database results, and **exits non-zero if any database fails**
   — see [Failure alerting](#failure-alerting).

### Configuration

All knobs are environment variables (defaults in parentheses):

| Variable | Default | Meaning |
|----------|---------|---------|
| `BACKUP_ROOT` | `/var/backups/postgres` | Common root; every DB gets a subfolder |
| `BACKUP_DATABASES` | parsed from `.env` | Space/comma list of databases |
| `BACKUP_RETENTION_DAYS` | `14` | Prune dumps older than this |
| `BACKUP_FORMAT` | `custom` | `custom` → `.dump` (pg_restore); `plain` → `.sql.gz` |
| `BACKUP_RUN_AS` | `postgres` | OS user to run `pg_dump` as (via `sudo`). Empty/own user skips sudo (local dev). |

## Offsite pull (Mac mini)

Run [`pull-backups.sh`](../scripts/db/pull-backups.sh) **on the Mac mini**, a bit
after the server's dump window:

```bash
BACKUP_SSH_HOST=ec2-user@go.sstools.co bash scripts/db/pull-backups.sh
```

It mirrors `BACKUP_REMOTE_DIR` (`/var/backups/postgres/`) into `BACKUP_LOCAL_DIR`
(`~/Backups/postgres/`) with `--delete-after`, so the local copy tracks
server-side rotation. Use a dedicated, restricted SSH key (`BACKUP_SSH_KEY`).

Schedule it with a launchd agent (preferred on macOS) — e.g. a
`~/Library/LaunchAgents/co.sstools.pg-pull.plist` with a `StartCalendarInterval`
of 08:00 — or a cron entry:

```cron
0 8 * * *  /bin/bash "$HOME/path/to/scripts/db/pull-backups.sh" >> "$HOME/Library/Logs/pg-pull.log" 2>&1
```

## Restoring

[`restore.sh`](../scripts/db/restore.sh) auto-detects the format from the file
extension (`.dump` → `pg_restore`, `.sql.gz` → `gunzip | psql`).

**Restore drill** (recommended monthly — an unverified backup is a guess) into a
throwaway database, then drop it:

```bash
sudo RESTORE_CREATE=1 bash scripts/db/restore.sh \
  /var/backups/postgres/shortlinks/shortlinks-latest.dump shortlinks_restore_test
sudo -u postgres psql -d shortlinks_restore_test -c '\dt'   # sanity check
sudo -u postgres dropdb shortlinks_restore_test
```

**Real recovery** over the live database (destructive — only when you mean it).
The custom-format restore uses `--clean --if-exists` so existing objects are
dropped and recreated:

```bash
sudo systemctl stop shortlinks
sudo bash scripts/db/restore.sh \
  /var/backups/postgres/shortlinks/shortlinks-20260625-073001Z.dump shortlinks
sudo systemctl start shortlinks
```

## Failure alerting

`backup.sh` exits non-zero if any database fails, and logs the error. A silent
cron failure is worse than no backup, so wire up at least one of:

- **cron MAILTO** — set `MAILTO=you@example.com` at the top of the crontab; cron
  emails any output from a failed (non-zero) run.
- **Healthcheck ping** — append a curl to a dead-man's-switch (e.g. Healthchecks.io)
  only on success, so a missed ping alerts you:

  ```cron
  30 7 * * *  /usr/bin/bash /opt/shortlinks/scripts/db/backup.sh >> /var/log/pg-backup.log 2>&1 && curl -fsS -m 10 https://hc-ping.com/<uuid> >/dev/null
  ```

## Encryption at rest

Dumps hold sensitive data. Transport is already protected by SSH, and the files
are owner-only (`0600`). If you also want them encrypted at rest (on the server
and/or the Mac mini), encrypt after dumping — e.g. pipe a `plain` dump through
[`age`](https://github.com/FiloSottile/age):

```bash
pg_dump shortlinks | gzip | age -r <recipient-key> > shortlinks-$(date -u +%Y%m%d)Z.sql.gz.age
```

This is noted as an optional hardening step, not wired into `backup.sh` by
default.

## Out of scope

- **Point-in-time recovery** (WAL archiving / `pg_basebackup`) for sub-second RPO
  — revisit if nightly granularity proves insufficient.
- **S3 (or other cloud) offsite** as a second destination — easy to add as a
  second pull/push target alongside the Mac mini.
