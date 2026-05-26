-- url_filter_rules: admin-managed regex patterns evaluated in the Go service
-- (not in PostgreSQL) against each link's destination_url at creation. Each rule
-- maps a Go-compatible regex to a denial reason code (see PRD URL Filtering).
CREATE TABLE url_filter_rules (
    id          BIGSERIAL PRIMARY KEY,
    pattern     TEXT NOT NULL,
    reason_code SMALLINT NOT NULL,
    description TEXT,
    active      BOOLEAN NOT NULL DEFAULT TRUE,
    created_by  BIGINT REFERENCES users(id),
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Load all active rules — checked on every link creation (cached 60s in Go).
CREATE INDEX idx_url_filter_rules_active ON url_filter_rules (active);
