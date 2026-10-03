CREATE OR REPLACE FUNCTION guard_procurement_workflow_catalog_references() RETURNS TRIGGER AS $$
DECLARE parent_status TEXT;check_product BOOLEAN:=false;check_supplier BOOLEAN:=false;found_active BOOLEAN;reference_id BIGINT;
BEGIN
 IF TG_TABLE_NAME='proc_requisitions' THEN
  IF TG_OP='INSERT' OR NEW.is_archived THEN RETURN NEW;END IF;
  IF NOT (OLD.is_archived AND NOT NEW.is_archived) AND (NEW.status NOT IN ('submitted','approved') OR NEW.status=OLD.status) THEN RETURN NEW;END IF;
  FOR reference_id IN SELECT DISTINCT product_id FROM proc_requisition_lines WHERE requisition_id=NEW.id AND product_id IS NOT NULL ORDER BY product_id LOOP
   SELECT active INTO found_active FROM proc_products WHERE id=reference_id FOR SHARE;
   IF found_active IS DISTINCT FROM true THEN RAISE EXCEPTION 'Active retained products required for requisition submission/approval/restore' USING ERRCODE='23514';END IF;
  END LOOP;
  FOR reference_id IN SELECT DISTINCT preferred_supplier_id FROM proc_requisition_lines WHERE requisition_id=NEW.id AND preferred_supplier_id IS NOT NULL ORDER BY preferred_supplier_id LOOP
   SELECT active INTO found_active FROM proc_suppliers WHERE id=reference_id FOR SHARE;
   IF found_active IS DISTINCT FROM true THEN RAISE EXCEPTION 'Active retained suppliers required for requisition submission/approval/restore' USING ERRCODE='23514';END IF;
  END LOOP;
  RETURN NEW;
 END IF;
 IF TG_TABLE_NAME='proc_purchase_orders' THEN
  IF NEW.is_archived THEN RETURN NEW;END IF;
  IF TG_OP='UPDATE' AND NOT OLD.is_archived AND NEW.status IN ('received','cancelled') AND (NEW.supplier_id,NEW.requisition_id) IS NOT DISTINCT FROM (OLD.supplier_id,OLD.requisition_id) THEN RETURN NEW;END IF;
  SELECT active INTO found_active FROM proc_suppliers WHERE id=NEW.supplier_id FOR SHARE;
  IF found_active IS DISTINCT FROM true THEN RAISE EXCEPTION 'Active existing supplier required for purchase orders' USING ERRCODE='23514';END IF;
  IF NEW.requisition_id IS NOT NULL THEN
   SELECT NOT is_archived INTO found_active FROM proc_requisitions WHERE id=NEW.requisition_id FOR SHARE;
   IF found_active IS DISTINCT FROM true THEN RAISE EXCEPTION 'Active existing requisition required for purchase orders' USING ERRCODE='23514';END IF;
  END IF;
  IF TG_OP='UPDATE' AND OLD.is_archived THEN
   FOR reference_id IN SELECT DISTINCT product_id FROM proc_purchase_order_lines WHERE purchase_order_id=NEW.id AND product_id IS NOT NULL ORDER BY product_id LOOP
    SELECT active INTO found_active FROM proc_products WHERE id=reference_id FOR SHARE;
    IF found_active IS DISTINCT FROM true THEN RAISE EXCEPTION 'Active retained products required for order restore' USING ERRCODE='23514';END IF;
   END LOOP;
  END IF;
  RETURN NEW;
 END IF;
 IF TG_TABLE_NAME='proc_requisition_lines' THEN
  SELECT status INTO parent_status FROM proc_requisitions WHERE id=NEW.requisition_id FOR SHARE;
  check_product:=TG_OP='INSERT' OR parent_status IN ('draft','returned','submitted','approved');
  check_supplier:=check_product;
  IF TG_OP='UPDATE' THEN
   check_product:=check_product OR NEW.product_id IS DISTINCT FROM OLD.product_id;
   check_supplier:=check_supplier OR NEW.preferred_supplier_id IS DISTINCT FROM OLD.preferred_supplier_id;
  END IF;
  IF check_supplier AND NEW.preferred_supplier_id IS NOT NULL THEN
   SELECT active INTO found_active FROM proc_suppliers WHERE id=NEW.preferred_supplier_id FOR SHARE;
   IF found_active IS DISTINCT FROM true THEN RAISE EXCEPTION 'Active existing preferred supplier required for requisition lines' USING ERRCODE='23514';END IF;
  END IF;
 ELSE
  SELECT status INTO parent_status FROM proc_purchase_orders WHERE id=NEW.purchase_order_id FOR SHARE;
  check_product:=TG_OP='INSERT' OR parent_status NOT IN ('received','cancelled');
  IF TG_OP='UPDATE' THEN check_product:=check_product OR NEW.product_id IS DISTINCT FROM OLD.product_id;END IF;
 END IF;
 IF check_product AND NEW.product_id IS NOT NULL THEN
  SELECT active INTO found_active FROM proc_products WHERE id=NEW.product_id FOR SHARE;
  IF found_active IS DISTINCT FROM true THEN RAISE EXCEPTION 'Active existing product required for procurement lines' USING ERRCODE='23514';END IF;
 END IF;
 RETURN NEW;
END;$$ LANGUAGE plpgsql;
DROP TRIGGER IF EXISTS proc_requisitions_guard_catalog_references ON proc_requisitions;
CREATE TRIGGER proc_requisitions_guard_catalog_references BEFORE INSERT OR UPDATE ON proc_requisitions FOR EACH ROW EXECUTE FUNCTION guard_procurement_workflow_catalog_references();
DROP TRIGGER IF EXISTS proc_purchase_orders_guard_catalog_references ON proc_purchase_orders;
CREATE TRIGGER proc_purchase_orders_guard_catalog_references BEFORE INSERT OR UPDATE ON proc_purchase_orders FOR EACH ROW EXECUTE FUNCTION guard_procurement_workflow_catalog_references();
DROP TRIGGER IF EXISTS proc_requisition_lines_guard_catalog_references ON proc_requisition_lines;
CREATE TRIGGER proc_requisition_lines_guard_catalog_references BEFORE INSERT OR UPDATE ON proc_requisition_lines FOR EACH ROW EXECUTE FUNCTION guard_procurement_workflow_catalog_references();
DROP TRIGGER IF EXISTS proc_purchase_order_lines_guard_catalog_references ON proc_purchase_order_lines;
CREATE TRIGGER proc_purchase_order_lines_guard_catalog_references BEFORE INSERT OR UPDATE ON proc_purchase_order_lines FOR EACH ROW EXECUTE FUNCTION guard_procurement_workflow_catalog_references();
