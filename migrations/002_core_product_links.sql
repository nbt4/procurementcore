CREATE TABLE IF NOT EXISTS core_product_links (
    id BIGSERIAL PRIMARY KEY,
    procurement_product_id BIGINT NOT NULL UNIQUE,
    warehouse_product_id BIGINT NOT NULL UNIQUE,
    link_method VARCHAR(24) NOT NULL DEFAULT 'manual',
    linked_by BIGINT NOT NULL DEFAULT 0,
    linked_by_name VARCHAR(160) NOT NULL DEFAULT '',
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
