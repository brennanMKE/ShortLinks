-- audit_log: append-only record of every significant action. Rows are never
-- updated or deleted.
CREATE TABLE audit_log (
    id          BIGSERIAL PRIMARY KEY,
    actor_id    BIGINT REFERENCES users(id),
    user_id     BIGINT REFERENCES users(id),
    action      TEXT NOT NULL,
    target_type TEXT,
    target_id   BIGINT,
    metadata    JSONB,
    ip_address  INET,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Fetch audit history for a specific user.
CREATE INDEX idx_audit_log_user_id_created_at ON audit_log (user_id, created_at DESC);

-- Full audit log in reverse chronological order.
CREATE INDEX idx_audit_log_created_at ON audit_log (created_at DESC);

-- Filter by event type.
CREATE INDEX idx_audit_log_action ON audit_log (action);
