ALTER TABLE proc_purchase_orders
    ADD COLUMN IF NOT EXISTS supplier_order_number VARCHAR(120) NOT NULL DEFAULT '';

CREATE INDEX IF NOT EXISTS idx_proc_purchase_orders_supplier_order_number
    ON proc_purchase_orders (supplier_order_number)
    WHERE supplier_order_number <> '';
