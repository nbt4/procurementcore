CREATE TABLE IF NOT EXISTS proc_amazon_punchout_sessions (
    id BIGSERIAL PRIMARY KEY,
    token_hash VARCHAR(64) NOT NULL UNIQUE,
    user_id BIGINT NOT NULL,
    username VARCHAR(160) NOT NULL DEFAULT '',
    buyer_email VARCHAR(255) NOT NULL DEFAULT '',
    status VARCHAR(30) NOT NULL,
    expires_at TIMESTAMP NOT NULL,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_proc_amazon_sessions_user_id ON proc_amazon_punchout_sessions(user_id);
CREATE INDEX IF NOT EXISTS idx_proc_amazon_sessions_status ON proc_amazon_punchout_sessions(status);
CREATE INDEX IF NOT EXISTS idx_proc_amazon_sessions_expires_at ON proc_amazon_punchout_sessions(expires_at);

ALTER TABLE proc_requisitions ADD COLUMN IF NOT EXISTS amazon_punchout_session_id BIGINT UNIQUE;
ALTER TABLE proc_purchase_orders ADD COLUMN IF NOT EXISTS amazon_punchout_session_id BIGINT UNIQUE;
ALTER TABLE proc_purchase_orders ADD COLUMN IF NOT EXISTS amazon_payload_id VARCHAR(100) NOT NULL DEFAULT '';
ALTER TABLE proc_requisition_lines ADD COLUMN IF NOT EXISTS supplier_part_id VARCHAR(120) NOT NULL DEFAULT '';
ALTER TABLE proc_requisition_lines ADD COLUMN IF NOT EXISTS supplier_part_auxiliary_id VARCHAR(500) NOT NULL DEFAULT '';
ALTER TABLE proc_purchase_order_lines ADD COLUMN IF NOT EXISTS supplier_part_id VARCHAR(120) NOT NULL DEFAULT '';
ALTER TABLE proc_purchase_order_lines ADD COLUMN IF NOT EXISTS supplier_part_auxiliary_id VARCHAR(500) NOT NULL DEFAULT '';
