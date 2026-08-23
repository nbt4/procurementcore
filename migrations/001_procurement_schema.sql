-- ProcurementCore owns these tables in the shared PostgreSQL database.
-- The application runs the equivalent GORM migration on startup; this file is
-- provided for review, disaster recovery and clean-volume deployments.
CREATE TABLE IF NOT EXISTS proc_suppliers (
    id BIGSERIAL PRIMARY KEY, name VARCHAR(180) NOT NULL, code VARCHAR(40) NOT NULL UNIQUE,
    website VARCHAR(1000), contact_name VARCHAR(160), email VARCHAR(255), phone VARCHAR(80),
    payment_terms VARCHAR(120), default_lead_days INTEGER NOT NULL DEFAULT 0,
    rating DOUBLE PRECISION NOT NULL DEFAULT 0, preferred BOOLEAN NOT NULL DEFAULT FALSE,
    active BOOLEAN NOT NULL DEFAULT TRUE, risk_level VARCHAR(20) NOT NULL DEFAULT 'low',
    notes TEXT, created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(), updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE TABLE IF NOT EXISTS proc_categories (
    id BIGSERIAL PRIMARY KEY, name VARCHAR(160) NOT NULL UNIQUE, description TEXT,
    parameter_schema JSONB NOT NULL DEFAULT '[]', created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(), updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE TABLE IF NOT EXISTS proc_products (
    id BIGSERIAL PRIMARY KEY, sku VARCHAR(80) NOT NULL UNIQUE, name VARCHAR(240) NOT NULL,
    description TEXT, category_id BIGINT REFERENCES proc_categories(id), unit VARCHAR(30) DEFAULT 'Stk.',
    manufacturer VARCHAR(180), model VARCHAR(180), parameters JSONB NOT NULL DEFAULT '{}',
    active BOOLEAN NOT NULL DEFAULT TRUE, reorder_point DOUBLE PRECISION DEFAULT 0,
    target_stock DOUBLE PRECISION DEFAULT 0, created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(), updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_products_parameters ON proc_products USING GIN(parameters);
CREATE TABLE IF NOT EXISTS proc_offers (
    id BIGSERIAL PRIMARY KEY, product_id BIGINT NOT NULL REFERENCES proc_products(id), supplier_id BIGINT NOT NULL REFERENCES proc_suppliers(id),
    supplier_sku VARCHAR(120), price_cents BIGINT NOT NULL, currency VARCHAR(3) NOT NULL DEFAULT 'EUR',
    minimum_quantity DOUBLE PRECISION DEFAULT 1, pack_size DOUBLE PRECISION DEFAULT 1, lead_days INTEGER DEFAULT 0,
    purchase_url VARCHAR(2000), valid_until TIMESTAMPTZ, active BOOLEAN NOT NULL DEFAULT TRUE,
    last_checked_at TIMESTAMPTZ, created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(), updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE TABLE IF NOT EXISTS proc_price_histories (
    id BIGSERIAL PRIMARY KEY, offer_id BIGINT NOT NULL REFERENCES proc_offers(id), price_cents BIGINT NOT NULL,
    currency VARCHAR(3) NOT NULL, recorded_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE TABLE IF NOT EXISTS proc_price_alerts (
    id BIGSERIAL PRIMARY KEY, product_id BIGINT NOT NULL REFERENCES proc_products(id), target_price_cents BIGINT NOT NULL,
    currency VARCHAR(3) NOT NULL DEFAULT 'EUR', active BOOLEAN NOT NULL DEFAULT TRUE, triggered BOOLEAN NOT NULL DEFAULT FALSE,
    triggered_price_cents BIGINT, triggered_offer_id BIGINT, triggered_at TIMESTAMPTZ,
    created_by BIGINT NOT NULL, created_by_name VARCHAR(160), created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(), updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE TABLE IF NOT EXISTS proc_requisitions (
    id BIGSERIAL PRIMARY KEY, number VARCHAR(40) NOT NULL UNIQUE, title VARCHAR(240) NOT NULL,
    status VARCHAR(30) NOT NULL DEFAULT 'draft', requester_id BIGINT NOT NULL, requester_name VARCHAR(160),
    cost_center VARCHAR(80), justification TEXT, needed_by TIMESTAMPTZ, estimated_total_cents BIGINT NOT NULL DEFAULT 0,
    approved_by BIGINT, approved_by_name VARCHAR(160), decision_note TEXT, submitted_at TIMESTAMPTZ, decided_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(), updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE TABLE IF NOT EXISTS proc_requisition_lines (
    id BIGSERIAL PRIMARY KEY, requisition_id BIGINT NOT NULL REFERENCES proc_requisitions(id) ON DELETE CASCADE,
    product_id BIGINT REFERENCES proc_products(id), description VARCHAR(500) NOT NULL, quantity DOUBLE PRECISION NOT NULL,
    unit VARCHAR(30), estimated_price_cents BIGINT NOT NULL DEFAULT 0, preferred_supplier_id BIGINT, purchase_url VARCHAR(2000)
);
CREATE TABLE IF NOT EXISTS proc_purchase_orders (
    id BIGSERIAL PRIMARY KEY, number VARCHAR(40) NOT NULL UNIQUE, supplier_id BIGINT NOT NULL REFERENCES proc_suppliers(id),
    requisition_id BIGINT REFERENCES proc_requisitions(id), status VARCHAR(30) NOT NULL DEFAULT 'draft',
    currency VARCHAR(3) NOT NULL DEFAULT 'EUR', total_cents BIGINT NOT NULL DEFAULT 0, ordered_by BIGINT NOT NULL,
    ordered_by_name VARCHAR(160), order_date TIMESTAMPTZ, expected_delivery TIMESTAMPTZ, notes TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(), updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE TABLE IF NOT EXISTS proc_purchase_order_lines (
    id BIGSERIAL PRIMARY KEY, purchase_order_id BIGINT NOT NULL REFERENCES proc_purchase_orders(id) ON DELETE CASCADE,
    product_id BIGINT REFERENCES proc_products(id), description VARCHAR(500) NOT NULL, quantity DOUBLE PRECISION NOT NULL,
    received_quantity DOUBLE PRECISION NOT NULL DEFAULT 0, unit VARCHAR(30), unit_price_cents BIGINT NOT NULL DEFAULT 0,
    purchase_url VARCHAR(2000)
);
CREATE TABLE IF NOT EXISTS proc_receipts (
    id BIGSERIAL PRIMARY KEY, purchase_order_id BIGINT NOT NULL REFERENCES proc_purchase_orders(id),
    purchase_order_line_id BIGINT NOT NULL REFERENCES proc_purchase_order_lines(id), quantity DOUBLE PRECISION NOT NULL,
    received_by BIGINT NOT NULL, received_by_name VARCHAR(160), note TEXT, received_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE TABLE IF NOT EXISTS proc_activities (
    id BIGSERIAL PRIMARY KEY, entity_type VARCHAR(40) NOT NULL, entity_id BIGINT NOT NULL, action VARCHAR(80) NOT NULL,
    user_id BIGINT NOT NULL, username VARCHAR(160), details TEXT, created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_procurement_activity_created ON proc_activities(created_at DESC);
