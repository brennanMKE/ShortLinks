# Database setup scripts

One-time PostgreSQL bootstrap for ShortLinks. These scripts create (and tear
down) the application **login role** and **database**. They do **not** create
any tables — the schema is owned by [golang-migrate](https://github.com/golang-migrate/migrate)
and lives in `migrations/`.

Run order on a fresh server:

1. `create.sql` — create the role and database (this folder)
2. migrations — create the schema:
   `migrate -path migrations -database "$DATABASE_URL" up`

## Prerequisites

- A running PostgreSQL instance
- Access as a superuser (typically the `postgres` role) to create roles and
  databases

## Set the password first

`create.sql` ships with a **placeholder** password,
`CHANGE_ME_IN_PRODUCTION`. Before running it anywhere that matters, edit
`create.sql` and replace the placeholder with a real, strong secret. Keep that
secret out of source control.

The same value must be reflected in your `.env` `DATABASE_URL`, which uses the
role name `shortlinks` and database name `shortlinks`:

```
DATABASE_URL=postgres://shortlinks:<your-password>@localhost:5432/shortlinks?sslmode=disable
```

## Create the database and role

```bash
psql -U postgres -f scripts/db/create.sql
```

The role creation is idempotent (guarded by a `DO` block). `CREATE DATABASE`
cannot be guarded with `IF NOT EXISTS` in PostgreSQL, so re-running on an
existing database raises a harmless "database already exists" error you can
ignore.

## Drop the database and role (development reset)

```bash
psql -U postgres -f scripts/db/drop.sql
```

Both statements use `IF EXISTS`, so this is safe to run repeatedly.

> **Warning:** `drop.sql` permanently destroys all data in the `shortlinks`
> database. Use it only for local development resets — never in production.
