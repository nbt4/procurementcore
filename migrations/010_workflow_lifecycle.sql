ALTER TABLE proc_requisitions ADD COLUMN IF NOT EXISTS is_archived BOOLEAN NOT NULL DEFAULT false;
ALTER TABLE proc_requisitions ADD COLUMN IF NOT EXISTS archived_at TIMESTAMP;
ALTER TABLE proc_purchase_orders ADD COLUMN IF NOT EXISTS is_archived BOOLEAN NOT NULL DEFAULT false;
ALTER TABLE proc_purchase_orders ADD COLUMN IF NOT EXISTS archived_at TIMESTAMP;
ALTER TABLE proc_receipts ADD COLUMN IF NOT EXISTS putaway_task_id BIGINT;
CREATE INDEX IF NOT EXISTS idx_proc_receipts_putaway_task_id ON proc_receipts(putaway_task_id);
DO $$ BEGIN
 IF to_regclass('audit_log') IS NOT NULL THEN
  EXECUTE $backfill$UPDATE proc_receipts r SET putaway_task_id=(a.new_values#>>'{receipt,putawayTaskId}')::BIGINT
  FROM audit_log a WHERE a.entity_type='procurement_order' AND a.action='order.goods_received'
  AND a.new_values#>>'{receipt,id}'=r.id::text AND r.putaway_task_id IS NULL
  AND a.new_values#>>'{receipt,putawayTaskId}' ~ '^[1-9][0-9]{0,17}$'$backfill$;
 END IF;
END $$;
CREATE OR REPLACE FUNCTION procurement_order_has_open_putaway(order_id BIGINT) RETURNS BOOLEAN AS $$
DECLARE found BOOLEAN:=false;
BEGIN
 IF to_regclass('warehouse_tasks') IS NOT NULL THEN
  EXECUTE 'SELECT EXISTS(SELECT 1 FROM proc_receipts r JOIN warehouse_tasks t ON t.task_id=r.putaway_task_id WHERE r.purchase_order_id=$1 AND t.status NOT IN (''done'',''cancelled'') AND NOT t.is_archived)' INTO found USING order_id;
 END IF;
 RETURN found;
END;$$ LANGUAGE plpgsql;
CREATE OR REPLACE FUNCTION guard_procurement_workflow_lifecycle() RETURNS TRIGGER AS $$
DECLARE old_business JSONB;new_business JSONB;
BEGIN
 IF TG_OP='DELETE' THEN RAISE EXCEPTION 'Archive procurement workflows to retain identities and history' USING ERRCODE='23514';END IF;
 IF TG_OP='INSERT' THEN
  IF NEW.is_archived THEN RAISE EXCEPTION 'Create active workflows and archive separately' USING ERRCODE='23514';END IF;
  NEW.updated_at:=clock_timestamp() AT TIME ZONE 'UTC';NEW.archived_at:=NULL;RETURN NEW;
 END IF;
 IF (NEW.id,NEW.created_at) IS DISTINCT FROM (OLD.id,OLD.created_at) THEN RAISE EXCEPTION 'Procurement workflow identity and provenance are immutable' USING ERRCODE='23514';END IF;
 IF TG_TABLE_NAME='proc_requisitions' AND (to_jsonb(NEW)->'requester_id') IS DISTINCT FROM (to_jsonb(OLD)->'requester_id') THEN RAISE EXCEPTION 'Requisition requester identity is immutable' USING ERRCODE='23514';END IF;
 old_business:=to_jsonb(OLD)-'is_archived'-'archived_at'-'updated_at';new_business:=to_jsonb(NEW)-'is_archived'-'archived_at'-'updated_at';
 IF (OLD.is_archived OR NEW.is_archived) AND (old_business IS DISTINCT FROM new_business OR (OLD.is_archived=NEW.is_archived AND OLD.is_archived)) THEN RAISE EXCEPTION 'Restore workflow before editing; separate business edits from lifecycle' USING ERRCODE='23514';END IF;
 IF NOT OLD.is_archived AND NEW.is_archived THEN
  IF TG_TABLE_NAME='proc_requisitions' THEN
   IF OLD.status NOT IN ('draft','returned','rejected','ordered') THEN RAISE EXCEPTION 'Resolve submitted or approved requisitions before archival' USING ERRCODE='23514';END IF;
   IF EXISTS(SELECT 1 FROM proc_purchase_orders WHERE requisition_id=OLD.id AND NOT is_archived AND status NOT IN ('cancelled','received')) THEN RAISE EXCEPTION 'Open orders block requisition archival' USING ERRCODE='23514';END IF;
  ELSE
   IF OLD.status NOT IN ('draft','received','cancelled') THEN RAISE EXCEPTION 'Complete or cancel outstanding orders before archival' USING ERRCODE='23514';END IF;
   IF procurement_order_has_open_putaway(OLD.id::BIGINT) THEN RAISE EXCEPTION 'Open receipt putaway tasks block order archival' USING ERRCODE='23514';END IF;
  END IF;
 END IF;
 IF OLD.is_archived AND NOT NEW.is_archived THEN
  IF TG_TABLE_NAME='proc_requisitions' THEN
   IF EXISTS(SELECT 1 FROM proc_requisition_lines l LEFT JOIN proc_products p ON p.id=l.product_id LEFT JOIN proc_suppliers s ON s.id=l.preferred_supplier_id WHERE l.requisition_id=OLD.id AND ((l.product_id IS NOT NULL AND (p.id IS NULL OR NOT p.active)) OR (l.preferred_supplier_id IS NOT NULL AND (s.id IS NULL OR NOT s.active)))) THEN RAISE EXCEPTION 'Active retained requisition parents required for restore' USING ERRCODE='23514';END IF;
  ELSE
   IF NOT EXISTS(SELECT 1 FROM proc_suppliers WHERE id=NEW.supplier_id AND active) THEN RAISE EXCEPTION 'Active supplier required for order restore' USING ERRCODE='23514';END IF;
   IF NEW.requisition_id IS NOT NULL AND NOT EXISTS(SELECT 1 FROM proc_requisitions WHERE id=NEW.requisition_id AND NOT is_archived) THEN RAISE EXCEPTION 'Restore original requisition before order' USING ERRCODE='23514';END IF;
   IF EXISTS(SELECT 1 FROM proc_purchase_order_lines l LEFT JOIN proc_products p ON p.id=l.product_id WHERE l.purchase_order_id=OLD.id AND l.product_id IS NOT NULL AND (p.id IS NULL OR NOT p.active)) THEN RAISE EXCEPTION 'Active retained order products required for restore' USING ERRCODE='23514';END IF;
  END IF;
 END IF;
 NEW.updated_at:=GREATEST(clock_timestamp() AT TIME ZONE 'UTC',OLD.updated_at+INTERVAL '1 microsecond');
 NEW.archived_at:=CASE WHEN NEW.is_archived THEN COALESCE(OLD.archived_at,clock_timestamp() AT TIME ZONE 'UTC') ELSE NULL END;
 RETURN NEW;
END;$$ LANGUAGE plpgsql;
DROP TRIGGER IF EXISTS proc_requisitions_guard_lifecycle_version ON proc_requisitions;
CREATE TRIGGER proc_requisitions_guard_lifecycle_version BEFORE INSERT OR UPDATE OR DELETE ON proc_requisitions FOR EACH ROW EXECUTE FUNCTION guard_procurement_workflow_lifecycle();
DROP TRIGGER IF EXISTS proc_purchase_orders_guard_lifecycle_version ON proc_purchase_orders;
CREATE TRIGGER proc_purchase_orders_guard_lifecycle_version BEFORE INSERT OR UPDATE OR DELETE ON proc_purchase_orders FOR EACH ROW EXECUTE FUNCTION guard_procurement_workflow_lifecycle();
CREATE OR REPLACE FUNCTION guard_procurement_workflow_line() RETURNS TRIGGER AS $$
DECLARE parent_id BIGINT;archived BOOLEAN;
BEGIN
 IF TG_TABLE_NAME='proc_requisition_lines' THEN
  parent_id:=CASE WHEN TG_OP='DELETE' THEN OLD.requisition_id ELSE NEW.requisition_id END;
  IF TG_OP='UPDATE' AND (NEW.id,NEW.requisition_id) IS DISTINCT FROM (OLD.id,OLD.requisition_id) THEN RAISE EXCEPTION 'Requisition line identity is immutable' USING ERRCODE='23514';END IF;
  SELECT is_archived INTO archived FROM proc_requisitions WHERE id=parent_id FOR SHARE;
 ELSE
  parent_id:=CASE WHEN TG_OP='DELETE' THEN OLD.purchase_order_id ELSE NEW.purchase_order_id END;
  IF TG_OP='UPDATE' AND (NEW.id,NEW.purchase_order_id) IS DISTINCT FROM (OLD.id,OLD.purchase_order_id) THEN RAISE EXCEPTION 'Order line identity is immutable' USING ERRCODE='23514';END IF;
  SELECT is_archived INTO archived FROM proc_purchase_orders WHERE id=parent_id FOR SHARE;
 END IF;
 IF archived IS NULL OR archived THEN RAISE EXCEPTION 'Active existing procurement workflow required for line edits' USING ERRCODE='23514';END IF;
 IF TG_OP='DELETE' AND TG_TABLE_NAME='proc_purchase_order_lines' AND EXISTS(SELECT 1 FROM proc_receipts WHERE purchase_order_line_id=OLD.id) THEN RAISE EXCEPTION 'Retain receipted purchase-order line identities and history' USING ERRCODE='23514';END IF;
 IF TG_OP='DELETE' THEN RETURN OLD;END IF;RETURN NEW;
END;$$ LANGUAGE plpgsql;
CREATE OR REPLACE FUNCTION touch_procurement_workflow_line_version() RETURNS TRIGGER AS $$
BEGIN
 IF TG_TABLE_NAME='proc_requisition_lines' THEN UPDATE proc_requisitions SET updated_at=updated_at WHERE id=CASE WHEN TG_OP='DELETE' THEN OLD.requisition_id ELSE NEW.requisition_id END;
 ELSE UPDATE proc_purchase_orders SET updated_at=updated_at WHERE id=CASE WHEN TG_OP='DELETE' THEN OLD.purchase_order_id ELSE NEW.purchase_order_id END;END IF;
 RETURN NULL;
END;$$ LANGUAGE plpgsql;
DROP TRIGGER IF EXISTS proc_requisition_lines_guard_lifecycle ON proc_requisition_lines;
CREATE TRIGGER proc_requisition_lines_guard_lifecycle BEFORE INSERT OR UPDATE OR DELETE ON proc_requisition_lines FOR EACH ROW EXECUTE FUNCTION guard_procurement_workflow_line();
DROP TRIGGER IF EXISTS proc_requisition_lines_touch_version ON proc_requisition_lines;
CREATE TRIGGER proc_requisition_lines_touch_version AFTER INSERT OR UPDATE OR DELETE ON proc_requisition_lines FOR EACH ROW EXECUTE FUNCTION touch_procurement_workflow_line_version();
DROP TRIGGER IF EXISTS proc_purchase_order_lines_guard_lifecycle ON proc_purchase_order_lines;
CREATE TRIGGER proc_purchase_order_lines_guard_lifecycle BEFORE INSERT OR UPDATE OR DELETE ON proc_purchase_order_lines FOR EACH ROW EXECUTE FUNCTION guard_procurement_workflow_line();
DROP TRIGGER IF EXISTS proc_purchase_order_lines_touch_version ON proc_purchase_order_lines;
CREATE TRIGGER proc_purchase_order_lines_touch_version AFTER INSERT OR UPDATE OR DELETE ON proc_purchase_order_lines FOR EACH ROW EXECUTE FUNCTION touch_procurement_workflow_line_version();
CREATE OR REPLACE FUNCTION guard_procurement_master_lifecycle() RETURNS TRIGGER AS $$
DECLARE old_business JSONB;new_business JSONB;
BEGIN
 IF TG_OP='DELETE' THEN RAISE EXCEPTION 'Archive procurement catalog records to retain identities and history' USING ERRCODE='23514';END IF;
 IF TG_OP='UPDATE' THEN
  IF (NEW.id,NEW.created_at) IS DISTINCT FROM (OLD.id,OLD.created_at) THEN RAISE EXCEPTION 'Procurement identity and provenance are immutable' USING ERRCODE='23514';END IF;
  old_business:=to_jsonb(OLD)-'active'-'updated_at'-'last_checked_at';new_business:=to_jsonb(NEW)-'active'-'updated_at'-'last_checked_at';
  IF (NOT OLD.active OR NOT NEW.active) AND (old_business IS DISTINCT FROM new_business OR (NOT OLD.active AND NOT NEW.active)) THEN
   RAISE EXCEPTION 'Separate procurement archive, restore and business edits; lifecycle preserves fields' USING ERRCODE='23514';END IF;
  IF OLD.active AND NOT NEW.active THEN
   IF TG_TABLE_NAME='proc_categories' AND EXISTS(SELECT 1 FROM proc_products WHERE category_id=OLD.id AND active) THEN RAISE EXCEPTION 'Active products block category archival' USING ERRCODE='23514';END IF;
   IF TG_TABLE_NAME='proc_suppliers' AND EXISTS(SELECT 1 FROM proc_purchase_orders WHERE supplier_id=OLD.id AND status NOT IN ('cancelled','received') AND NOT is_archived) THEN RAISE EXCEPTION 'Open orders block supplier archival' USING ERRCODE='23514';END IF;
   IF TG_TABLE_NAME='proc_products' THEN
    IF EXISTS(SELECT 1 FROM proc_purchase_order_lines l JOIN proc_purchase_orders o ON o.id=l.purchase_order_id WHERE l.product_id=OLD.id AND o.status NOT IN ('cancelled','received') AND NOT o.is_archived) OR EXISTS(SELECT 1 FROM proc_requisition_lines l JOIN proc_requisitions r ON r.id=l.requisition_id WHERE l.product_id=OLD.id AND r.status IN ('draft','submitted','approved') AND NOT r.is_archived) THEN RAISE EXCEPTION 'Open orders or requisitions block product archival' USING ERRCODE='23514';END IF;
   END IF;
  END IF;
  IF NOT OLD.active AND NEW.active AND TG_TABLE_NAME='proc_offers' THEN
   IF NOT EXISTS(SELECT 1 FROM proc_products WHERE id=NEW.product_id AND active) OR NOT EXISTS(SELECT 1 FROM proc_suppliers WHERE id=NEW.supplier_id AND active) THEN RAISE EXCEPTION 'Active existing parents required for offer restoration' USING ERRCODE='23514';END IF;
  END IF;
  NEW.updated_at:=GREATEST(clock_timestamp(),OLD.updated_at+INTERVAL '1 microsecond');
 ELSE
  IF NOT NEW.active THEN RAISE EXCEPTION 'Create active catalog records; archival is a separate workflow' USING ERRCODE='23514';END IF;
  NEW.updated_at:=clock_timestamp();
 END IF;
 IF TG_TABLE_NAME='proc_products' THEN
 IF NEW.active AND NEW.category_id IS NOT NULL THEN
  PERFORM 1 FROM proc_categories WHERE id=NEW.category_id AND active FOR SHARE;
  IF NOT FOUND THEN RAISE EXCEPTION 'Active existing category required for active product' USING ERRCODE='23514';END IF;
 END IF;
 END IF;
 RETURN NEW;
END;$$ LANGUAGE plpgsql;
DROP TRIGGER IF EXISTS proc_categories_guard_lifecycle_version ON proc_categories;
CREATE TRIGGER proc_categories_guard_lifecycle_version BEFORE INSERT OR UPDATE OR DELETE ON proc_categories FOR EACH ROW EXECUTE FUNCTION guard_procurement_master_lifecycle();
DROP TRIGGER IF EXISTS proc_suppliers_guard_lifecycle_version ON proc_suppliers;
CREATE TRIGGER proc_suppliers_guard_lifecycle_version BEFORE INSERT OR UPDATE OR DELETE ON proc_suppliers FOR EACH ROW EXECUTE FUNCTION guard_procurement_master_lifecycle();
DROP TRIGGER IF EXISTS proc_products_guard_lifecycle_version ON proc_products;
CREATE TRIGGER proc_products_guard_lifecycle_version BEFORE INSERT OR UPDATE OR DELETE ON proc_products FOR EACH ROW EXECUTE FUNCTION guard_procurement_master_lifecycle();
DROP TRIGGER IF EXISTS proc_offers_guard_lifecycle_version ON proc_offers;
CREATE TRIGGER proc_offers_guard_lifecycle_version BEFORE INSERT OR UPDATE OR DELETE ON proc_offers FOR EACH ROW EXECUTE FUNCTION guard_procurement_master_lifecycle();
