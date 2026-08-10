-- Enforce constraints that 000004 and 000005 were edited *in place* to add,
-- after they had already been applied to some databases (commit 791e7d3,
-- "Fix audit findings...", 2026-05-25). golang-migrate tracks version
-- *numbers*, not file *content*, so any database that had already run 4/5
-- before that edit never re-applied it: schema_migrations reports a version
-- implying the strict shape while the tables kept the original permissive
-- one. See issues/0110.md (found it in shortlinks_test) and issues/0111.md
-- (confirmed the same drift in production).
--
-- Do NOT edit 000004 or 000005 again to "fix" this — that in-place edit is
-- the root cause this migration exists to correct. Any further change to
-- these tables belongs in a new migration, never a rewrite of an applied one.
--
-- Columns brought in line with a clean replay:
--   passkey_credentials.user_id  -> NOT NULL
--   sessions.user_id             -> NOT NULL
--   sessions.created_at          -> NOT NULL DEFAULT now()
--   sessions.expires_at          -> NOT NULL
--   sessions.last_seen_at        -> NOT NULL
--
-- The DEFAULT now() matters as much as the nullability: 791e7d3 added both to
-- sessions.created_at, so a drifted database is missing the default too, and a
-- nullability-only repair would leave the two schemas divergent.
--
-- On a database created *after* 791e7d3 (a clean replay, including every fresh
-- `migrate up` from here on) all five columns already have these constraints
-- and this migration is a deliberate no-op: SET NOT NULL / SET DEFAULT on a
-- column that already has them succeeds and changes nothing. Verified for
-- #0111 by dumping a drifted-then-migrated database and a fresh replay and
-- diffing them — identical.
--
-- Ordering vs. the undeployed campaign migrations (10-12): irrelevant. Those
-- touch campaigns/links/clicks only; this touches sessions and
-- passkey_credentials only. Production is at version 9, so a single
-- `migrate up` will apply 10, 11, 12 and then 13 in that order, and no
-- interaction between them exists in either direction.
--
-- Safety: there is no truthful value to backfill a NULL user_id with (it is
-- a FK to an unknown user), and these are the session/passkey tables, so
-- guessing or silently deleting rows is worse than stopping. This migration
-- FAILS LOUDLY, before making any change, if it finds a row that would
-- violate the new constraints. Zero rows violated these constraints when
-- production was inspected for #0111, but that was a point-in-time check;
-- this re-checks live at apply time rather than trusting that snapshot.
--
-- This whole file runs in one transaction (golang-migrate's default for the
-- postgres driver), so an abort here leaves the schema completely
-- untouched — no partial application. golang-migrate does, however, mark
-- schema_migrations dirty at version 13 when a migration raises. Recovering
-- from an abort therefore takes three steps, not one:
--
--   1. Inspect and resolve (or deliberately delete) the offending rows, e.g.
--        SELECT * FROM passkey_credentials WHERE user_id IS NULL;
--        SELECT * FROM sessions
--          WHERE user_id IS NULL OR created_at IS NULL
--             OR expires_at IS NULL OR last_seen_at IS NULL;
--   2. migrate -path migrations -database "$DATABASE_URL" force 12
--      (safe: the transaction rolled back, so the schema really is still at
--       12. `force` rewrites schema_migrations — it sets the recorded
--       version back to 12 AND clears the dirty flag — but it alters no
--       tables, which is why it is the right tool here and not a way to
--       skip a migration that genuinely half-applied.)
--   3. migrate -path migrations -database "$DATABASE_URL" up
--
-- Both tables are checked before either is altered, and all violations are
-- reported in one message, so an operator fixing them by hand does not have
-- to discover the second table on a second failed attempt.
--
-- Locking: SET NOT NULL takes ACCESS EXCLUSIVE and scans the table to verify.
-- These are the session and passkey tables — small by nature (production held
-- 5 and 1 rows when #0111 was filed) — so the pause is negligible. Take a
-- backup first anyway (scripts/db/backup.sh); they are the auth tables.
--
-- Postgres compatibility: DO blocks, RAISE EXCEPTION, SET NOT NULL and SET
-- DEFAULT are all long-standing syntax. Production runs 15.15; nothing here
-- requires 16.
DO $$
DECLARE
    bad_passkeys BIGINT;
    bad_sessions BIGINT;
BEGIN
    SELECT count(*) INTO bad_passkeys
        FROM passkey_credentials
        WHERE user_id IS NULL;

    SELECT count(*) INTO bad_sessions
        FROM sessions
        WHERE user_id IS NULL
           OR created_at IS NULL
           OR expires_at IS NULL
           OR last_seen_at IS NULL;

    IF bad_passkeys > 0 OR bad_sessions > 0 THEN
        RAISE EXCEPTION
            'migration 000013 aborted: % row(s) in passkey_credentials have a NULL user_id and % row(s) in sessions have a NULL user_id/created_at/expires_at/last_seen_at; no change was made. Resolve those rows, then: migrate force 12 && migrate up',
            bad_passkeys, bad_sessions;
    END IF;
END
$$;

ALTER TABLE passkey_credentials
    ALTER COLUMN user_id SET NOT NULL;

ALTER TABLE sessions
    ALTER COLUMN user_id SET NOT NULL,
    ALTER COLUMN created_at SET NOT NULL,
    ALTER COLUMN created_at SET DEFAULT now(),
    ALTER COLUMN expires_at SET NOT NULL,
    ALTER COLUMN last_seen_at SET NOT NULL;
