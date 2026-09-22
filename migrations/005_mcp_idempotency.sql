CREATE TABLE IF NOT EXISTS proc_idempotency_records (
    id BIGSERIAL PRIMARY KEY,
    user_id BIGINT NOT NULL,
    operation VARCHAR(80) NOT NULL,
    key_hash VARCHAR(64) NOT NULL,
    request_hash VARCHAR(64) NOT NULL,
    response JSONB NOT NULL DEFAULT '{}'::jsonb,
    status_code INTEGER NOT NULL DEFAULT 200,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT idx_proc_idempotency UNIQUE (user_id, operation, key_hash)
);

CREATE INDEX IF NOT EXISTS idx_proc_idempotency_records_created_at
    ON proc_idempotency_records(created_at);
