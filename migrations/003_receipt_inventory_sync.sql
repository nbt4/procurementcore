-- Preserve the Warehouse target and applied inventory quantity for every
-- Procurement goods receipt. Warehouse tables remain the inventory source of
-- truth; these fields provide an immutable audit reference on the receipt.
ALTER TABLE proc_receipts ADD COLUMN IF NOT EXISTS warehouse_product_id BIGINT;
ALTER TABLE proc_receipts ADD COLUMN IF NOT EXISTS warehouse_tracking_mode VARCHAR(20);
ALTER TABLE proc_receipts ADD COLUMN IF NOT EXISTS warehouse_quantity_applied DOUBLE PRECISION NOT NULL DEFAULT 0;
CREATE INDEX IF NOT EXISTS idx_proc_receipts_warehouse_product_id ON proc_receipts(warehouse_product_id);
