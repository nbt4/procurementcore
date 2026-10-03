CREATE TABLE IF NOT EXISTS proc_submission_reconciliations (
 id BIGSERIAL PRIMARY KEY,
 submission_id BIGINT NOT NULL UNIQUE REFERENCES proc_order_submissions(id),
 user_id BIGINT NOT NULL,
 resolution TEXT NOT NULL CHECK(resolution IN ('found_order','confirmed_not_sent')),
 supplier_order_number TEXT NOT NULL DEFAULT '',
 verification_evidence TEXT NOT NULL CHECK(char_length(btrim(verification_evidence)) BETWEEN 10 AND 2000),
 expected_context TEXT NOT NULL CHECK(expected_context ~ '^[0-9a-f]{64}$'),
 reviewed_payload JSONB NOT NULL,
 created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
 CHECK((resolution='found_order' AND char_length(btrim(supplier_order_number)) BETWEEN 1 AND 120) OR (resolution='confirmed_not_sent' AND supplier_order_number=''))
);
CREATE OR REPLACE FUNCTION retain_procurement_submission_reconciliation() RETURNS TRIGGER AS $$
BEGIN RAISE EXCEPTION 'Retain original human supplier verification' USING ERRCODE='23514';END;$$ LANGUAGE plpgsql;
DROP TRIGGER IF EXISTS proc_submission_reconciliations_guard_retention ON proc_submission_reconciliations;
CREATE TRIGGER proc_submission_reconciliations_guard_retention BEFORE UPDATE OR DELETE ON proc_submission_reconciliations FOR EACH ROW EXECUTE FUNCTION retain_procurement_submission_reconciliation();
