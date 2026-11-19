-- Site assets (#0176): admin-uploaded images served on the public home page,
-- stored in Postgres so the single binary needs no writable filesystem and the
-- existing database backups cover them. The only row for now is name='hero'.
-- bytes holds the re-encoded image (never the original upload); sha256 is the
-- lowercase hex digest of bytes and doubles as the ETag.
CREATE TABLE site_assets (
    name         TEXT PRIMARY KEY,
    content_type TEXT        NOT NULL,
    bytes        BYTEA       NOT NULL CHECK (octet_length(bytes) <= 2097152),
    width        INTEGER     NOT NULL,
    height       INTEGER     NOT NULL,
    sha256       TEXT        NOT NULL,
    alt          TEXT        NOT NULL,
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);
