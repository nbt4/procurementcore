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
   IF TG_TABLE_NAME='proc_suppliers' AND EXISTS(SELECT 1 FROM proc_purchase_orders WHERE supplier_id=OLD.id AND status NOT IN ('cancelled','received')) THEN RAISE EXCEPTION 'Open orders block supplier archival' USING ERRCODE='23514';END IF;
   IF TG_TABLE_NAME='proc_products' THEN
    IF EXISTS(SELECT 1 FROM proc_purchase_order_lines l JOIN proc_purchase_orders o ON o.id=l.purchase_order_id WHERE l.product_id=OLD.id AND o.status NOT IN ('cancelled','received')) OR EXISTS(SELECT 1 FROM proc_requisition_lines l JOIN proc_requisitions r ON r.id=l.requisition_id WHERE l.product_id=OLD.id AND r.status IN ('draft','submitted','approved')) THEN RAISE EXCEPTION 'Open orders or requisitions block product archival' USING ERRCODE='23514';END IF;
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
 RETURN NEW;
END;$$ LANGUAGE plpgsql;
DROP TRIGGER IF EXISTS proc_suppliers_guard_lifecycle_version ON proc_suppliers;
CREATE TRIGGER proc_suppliers_guard_lifecycle_version BEFORE INSERT OR UPDATE OR DELETE ON proc_suppliers FOR EACH ROW EXECUTE FUNCTION guard_procurement_master_lifecycle();
DROP TRIGGER IF EXISTS proc_products_guard_lifecycle_version ON proc_products;
CREATE TRIGGER proc_products_guard_lifecycle_version BEFORE INSERT OR UPDATE OR DELETE ON proc_products FOR EACH ROW EXECUTE FUNCTION guard_procurement_master_lifecycle();
DROP TRIGGER IF EXISTS proc_offers_guard_lifecycle_version ON proc_offers;
CREATE TRIGGER proc_offers_guard_lifecycle_version BEFORE INSERT OR UPDATE OR DELETE ON proc_offers FOR EACH ROW EXECUTE FUNCTION guard_procurement_master_lifecycle();
