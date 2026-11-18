SET lock_timeout = '5s';

-- Idempotency-Key records (#0147). A client-supplied key on a create request
-- makes a retry safe: the first request's response is stored and replayed.
-- status IS NULL means the first request is still in flight (pending); once it
-- finishes with a 2xx/4xx the status, body and replay headers are filled in.
-- Rows older than 24 hours are expired and pruned by the hourly retention job.
CREATE TABLE idempotency_keys (
    user_id               BIGINT      NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    key                   TEXT        NOT NULL,
    method                TEXT        NOT NULL,
    path                  TEXT        NOT NULL,
    request_sha256        BYTEA       NOT NULL,
    -- Random token minted by whichever request owns the pending row; Complete and
    -- Release must present it, so a stale owner that finishes after the row was
    -- reclaimed can neither overwrite nor delete the new owner's record.
    claim_id              TEXT        NOT NULL,
    status                INTEGER,
    response_body         BYTEA,
    response_content_type TEXT,
    response_location     TEXT,
    created_at            TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, key)
);

-- Supports the retention prune (DELETE ... WHERE created_at < cutoff).
CREATE INDEX idx_idempotency_keys_created_at ON idempotency_keys (created_at);
