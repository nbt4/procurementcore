CREATE TABLE IF NOT EXISTS proc_order_submissions (
 id BIGSERIAL PRIMARY KEY,
 purchase_order_id BIGINT NOT NULL UNIQUE REFERENCES proc_purchase_orders(id),
 provider TEXT NOT NULL CHECK(provider IN ('amazon','adam_hall')),
 user_id BIGINT NOT NULL,
 payload_id TEXT NOT NULL UNIQUE,
 expected_context TEXT NOT NULL,
 status TEXT NOT NULL CHECK(status IN ('pending','accepted','unknown')),
 outcome JSONB NOT NULL DEFAULT '{}'::jsonb,
 reviewed_payload JSONB NOT NULL,
 created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
 updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE OR REPLACE FUNCTION guard_procurement_order_submission_identity() RETURNS TRIGGER AS $$
BEGIN
 IF TG_OP='DELETE' THEN RAISE EXCEPTION 'Retain supplier submission identity and outcome' USING ERRCODE='23514';END IF;
 IF ROW(OLD.id,OLD.purchase_order_id,OLD.provider,OLD.user_id,OLD.payload_id,OLD.expected_context,OLD.reviewed_payload,OLD.created_at) IS DISTINCT FROM ROW(NEW.id,NEW.purchase_order_id,NEW.provider,NEW.user_id,NEW.payload_id,NEW.expected_context,NEW.reviewed_payload,NEW.created_at) THEN RAISE EXCEPTION 'Supplier submission identity is immutable' USING ERRCODE='23514';END IF;
 IF OLD.status<>'pending' AND ROW(OLD.status,OLD.outcome) IS DISTINCT FROM ROW(NEW.status,NEW.outcome) THEN RAISE EXCEPTION 'Retain the original external outcome' USING ERRCODE='23514';END IF;
 NEW.updated_at:=clock_timestamp() AT TIME ZONE 'UTC';RETURN NEW;
END;$$ LANGUAGE plpgsql;
DROP TRIGGER IF EXISTS proc_order_submissions_guard_identity ON proc_order_submissions;
CREATE TRIGGER proc_order_submissions_guard_identity BEFORE UPDATE OR DELETE ON proc_order_submissions FOR EACH ROW EXECUTE FUNCTION guard_procurement_order_submission_identity();

CREATE OR REPLACE FUNCTION guard_procurement_submitted_order() RETURNS TRIGGER AS $$
DECLARE submission RECORD; parent_id BIGINT;
BEGIN
 IF TG_TABLE_NAME='proc_purchase_orders' THEN
  SELECT * INTO submission FROM proc_order_submissions WHERE purchase_order_id=OLD.id FOR SHARE;
  IF submission.id IS NULL THEN RETURN NEW;END IF;
  IF NEW.status='draft' OR NEW.status='submitting' AND OLD.status<>'draft' AND OLD.status<>'submitting' THEN RAISE EXCEPTION 'Supplier claim forbids automatic resubmission; reconcile the external outcome' USING ERRCODE='23514';END IF;
  IF ROW(OLD.number,OLD.supplier_id,OLD.requisition_id,OLD.amazon_punchout_session_id,OLD.currency,OLD.total_cents) IS DISTINCT FROM ROW(NEW.number,NEW.supplier_id,NEW.requisition_id,NEW.amazon_punchout_session_id,NEW.currency,NEW.total_cents) THEN RAISE EXCEPTION 'Retain externally submitted order contents' USING ERRCODE='23514';END IF;
  IF OLD.amazon_payload_id<>'' AND OLD.amazon_payload_id IS DISTINCT FROM NEW.amazon_payload_id THEN RAISE EXCEPTION 'Retain supplier payload identity' USING ERRCODE='23514';END IF;
  IF submission.provider='amazon' AND NEW.amazon_payload_id IS DISTINCT FROM submission.payload_id THEN RAISE EXCEPTION 'Supplier payload must match the durable claim' USING ERRCODE='23514';END IF;
  RETURN NEW;
 END IF;
 parent_id:=CASE WHEN TG_OP='INSERT' THEN NEW.purchase_order_id ELSE OLD.purchase_order_id END;
 IF EXISTS(SELECT 1 FROM proc_order_submissions WHERE purchase_order_id=parent_id) OR TG_OP='UPDATE' AND EXISTS(SELECT 1 FROM proc_order_submissions WHERE purchase_order_id=NEW.purchase_order_id) THEN
  IF TG_OP<>'UPDATE' OR (to_jsonb(OLD)-'received_quantity') IS DISTINCT FROM (to_jsonb(NEW)-'received_quantity') THEN RAISE EXCEPTION 'Retain submitted commercial lines; receiving remains a separate workflow' USING ERRCODE='23514';END IF;
 END IF;
 IF TG_OP='DELETE' THEN RETURN OLD;END IF;RETURN NEW;
END;$$ LANGUAGE plpgsql;
DROP TRIGGER IF EXISTS proc_purchase_orders_guard_submission ON proc_purchase_orders;
CREATE TRIGGER proc_purchase_orders_guard_submission BEFORE UPDATE ON proc_purchase_orders FOR EACH ROW EXECUTE FUNCTION guard_procurement_submitted_order();
DROP TRIGGER IF EXISTS proc_purchase_order_lines_guard_submission ON proc_purchase_order_lines;
CREATE TRIGGER proc_purchase_order_lines_guard_submission BEFORE INSERT OR UPDATE OR DELETE ON proc_purchase_order_lines FOR EACH ROW EXECUTE FUNCTION guard_procurement_submitted_order();
