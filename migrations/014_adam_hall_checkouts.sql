CREATE TABLE IF NOT EXISTS proc_adam_hall_checkouts (
 id BIGSERIAL PRIMARY KEY,
 purchase_order_id BIGINT NOT NULL REFERENCES proc_purchase_orders(id),
 user_id BIGINT NOT NULL,
 local_context TEXT NOT NULL CHECK(local_context ~ '^[0-9a-f]{64}$'),
 account_fingerprint TEXT NOT NULL,
 status TEXT NOT NULL CHECK(status IN ('preparing','ready','failed')),
 reviewed_request JSONB NOT NULL,
 cart_snapshot JSONB NOT NULL DEFAULT '{}'::jsonb,
 context_cipher BYTEA,
 failure_code TEXT NOT NULL DEFAULT '',
 created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
 updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
 CHECK(status<>'ready' OR (context_cipher IS NOT NULL AND octet_length(context_cipher)>28 AND cart_snapshot<>'{}'::jsonb))
);
CREATE INDEX IF NOT EXISTS proc_adam_hall_checkouts_order ON proc_adam_hall_checkouts(purchase_order_id,id DESC);
CREATE OR REPLACE FUNCTION retain_procurement_adam_hall_checkout() RETURNS TRIGGER AS $$
BEGIN
 IF TG_OP='DELETE' THEN RAISE EXCEPTION 'Retain original supplier checkout preparation' USING ERRCODE='23514';END IF;
 IF ROW(OLD.id,OLD.purchase_order_id,OLD.user_id,OLD.local_context,OLD.account_fingerprint,OLD.reviewed_request,OLD.created_at) IS DISTINCT FROM ROW(NEW.id,NEW.purchase_order_id,NEW.user_id,NEW.local_context,NEW.account_fingerprint,NEW.reviewed_request,NEW.created_at) THEN RAISE EXCEPTION 'Retain supplier checkout identity and reviewed request' USING ERRCODE='23514';END IF;
 IF OLD.status<>'preparing' AND ROW(OLD.status,OLD.cart_snapshot,OLD.context_cipher,OLD.failure_code) IS DISTINCT FROM ROW(NEW.status,NEW.cart_snapshot,NEW.context_cipher,NEW.failure_code) THEN RAISE EXCEPTION 'Retain original supplier checkout outcome' USING ERRCODE='23514';END IF;
 NEW.updated_at:=clock_timestamp() AT TIME ZONE 'UTC';RETURN NEW;
END;$$ LANGUAGE plpgsql;
DROP TRIGGER IF EXISTS proc_adam_hall_checkouts_guard_retention ON proc_adam_hall_checkouts;
CREATE TRIGGER proc_adam_hall_checkouts_guard_retention BEFORE UPDATE OR DELETE ON proc_adam_hall_checkouts FOR EACH ROW EXECUTE FUNCTION retain_procurement_adam_hall_checkout();
