-- settings: runtime configuration values changeable without a restart.
CREATE TABLE settings (
    key        TEXT PRIMARY KEY,
    value      TEXT NOT NULL,
    updated_at TIMESTAMPTZ
);

-- Seed the registration toggle. Default false: the server is locked to
-- existing users until an admin explicitly opens registration.
INSERT INTO settings (key, value, updated_at)
VALUES ('registrations_enabled', 'false', now())
ON CONFLICT DO NOTHING;
