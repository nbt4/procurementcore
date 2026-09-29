CREATE TABLE IF NOT EXISTS proc_amazon_line_confirmations (
    id BIGSERIAL PRIMARY KEY,
    purchase_order_id BIGINT NOT NULL,
    purchase_order_line_id BIGINT NOT NULL,
    amazon_order_number VARCHAR(120) NOT NULL,
    accepted_quantity DOUBLE PRECISION NOT NULL DEFAULT 0,
    rejected_quantity DOUBLE PRECISION NOT NULL DEFAULT 0,
    expected_delivery TIMESTAMPTZ,
    notice_date TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (purchase_order_line_id, amazon_order_number)
);
CREATE INDEX IF NOT EXISTS idx_proc_amazon_line_confirmations_order ON proc_amazon_line_confirmations(purchase_order_id);

CREATE TABLE IF NOT EXISTS proc_amazon_confirmation_events (
    id BIGSERIAL PRIMARY KEY,
    payload_id VARCHAR(255) NOT NULL UNIQUE,
    purchase_order_id BIGINT NOT NULL,
    confirm_id VARCHAR(120) NOT NULL DEFAULT '',
    type VARCHAR(30) NOT NULL,
    notice_date TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_proc_amazon_confirmation_events_order ON proc_amazon_confirmation_events(purchase_order_id);

ALTER TABLE proc_purchase_orders ADD COLUMN IF NOT EXISTS amazon_order_numbers TEXT NOT NULL DEFAULT '';

CREATE TABLE IF NOT EXISTS proc_amazon_shipments (
    id BIGSERIAL PRIMARY KEY,
    payload_id VARCHAR(255) NOT NULL,
    purchase_order_id BIGINT NOT NULL,
    shipment_id VARCHAR(120) NOT NULL DEFAULT '',
    tracking_number VARCHAR(255) NOT NULL DEFAULT '',
    carrier VARCHAR(120) NOT NULL DEFAULT '',
    shipment_date TIMESTAMPTZ,
    delivery_date TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (payload_id, purchase_order_id)
);
CREATE INDEX IF NOT EXISTS idx_proc_amazon_shipments_order ON proc_amazon_shipments(purchase_order_id);
