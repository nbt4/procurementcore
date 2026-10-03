package api

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"strings"
	"time"

	"procurementcore/internal/auth"
	"procurementcore/internal/models"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type submissionReconciliationRequest struct {
	ID                   int64  `json:"id"`
	Resolution           string `json:"resolution"`
	SupplierOrderNumber  string `json:"supplier_order_number,omitempty"`
	VerificationEvidence string `json:"verification_evidence,omitempty"`
	HumanVerified        bool   `json:"human_verified,omitempty"`
	ExpectedUpdatedAt    string `json:"expected_updated_at,omitempty"`
	ExpectedContext      string `json:"expected_context,omitempty"`
	ConfirmationText     string `json:"confirmation_text,omitempty"`
	ConfirmChange        bool   `json:"confirm_change,omitempty"`
	Preview              bool   `json:"preview,omitempty"`
}

type submissionReconciliationRecord struct {
	ID                   int64           `json:"id"`
	SubmissionID         int64           `json:"submission_id"`
	UserID               uint            `json:"user_id"`
	Resolution           string          `json:"resolution"`
	SupplierOrderNumber  string          `json:"supplier_order_number"`
	VerificationEvidence string          `json:"verification_evidence"`
	ExpectedContext      string          `json:"expected_context"`
	ReviewedPayload      json.RawMessage `json:"reviewed_payload"`
	CreatedAt            time.Time       `json:"created_at"`
}

func (submissionReconciliationRecord) TableName() string { return "proc_submission_reconciliations" }

func (h *Handler) submissionReconciliationMCP(w http.ResponseWriter, r *http.Request) {
	if !signedProcurementUserDelegation(r, "cores:procurement:send") {
		writeJSON(w, 403, map[string]string{"error": "Explicit signed real-user supplier send scope required"})
		return
	}
	var input submissionReconciliationRequest
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16384))
	d.DisallowUnknownFields()
	if err := d.Decode(&input); err != nil || d.Decode(new(any)) != io.EOF || input.ID < 1 || input.ID > math.MaxInt32 || len(input.SupplierOrderNumber) > 120 || len(input.VerificationEvidence) > 2000 || (input.Resolution != "found_order" && input.Resolution != "confirmed_not_sent") {
		badRequest(w, "One bounded exact human supplier verification required")
		return
	}
	input.SupplierOrderNumber = strings.TrimSpace(input.SupplierOrderNumber)
	input.VerificationEvidence = strings.TrimSpace(input.VerificationEvidence)
	preview := input.Preview || !input.ConfirmChange
	result := map[string]any{}
	err := h.db.WithContext(r.Context()).Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec(`SET LOCAL lock_timeout='5s';SET LOCAL statement_timeout='20s';SET LOCAL TIME ZONE 'UTC'`).Error; err != nil {
			return err
		}
		if err := currentProcurementApprovalRights(tx, r, "orders", 0); err != nil {
			return err
		}
		var receipt *models.IdempotencyRecord
		if !preview {
			if len(input.ExpectedContext) != 64 {
				return &receiptFlowError{status: 428, code: "context_required", message: "Copy complete final human verification context"}
			}
			var replay json.RawMessage
			var err error
			receipt, replay, err = beginIdempotentMutation(tx, r, fmt.Sprintf("mcp_order_submission_reconcile:%d", input.ID), input)
			if err != nil {
				return err
			}
			if replay != nil {
				return json.Unmarshal(replay, &result)
			}
		}
		// Match finalization's claim-before-order lock order. Never lock an order
		// while waiting for a provider acknowledgement that needs its claim.
		var submission orderSubmissionRecord
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("purchase_order_id=?", input.ID).First(&submission).Error; err != nil {
			return err
		}
		if err := lockRequisitionOrderContext(tx); err != nil {
			return err
		}
		p, err := prepareSubmissionReconciliation(tx, input, submission)
		if err != nil {
			return err
		}
		result = p
		if preview || p["ready_to_execute"] != true {
			return errLifecyclePreview
		}
		if input.ConfirmationText != p["required_confirmation_text"] {
			return &receiptFlowError{status: 428, code: "confirmation_phrase_required", message: "Copy exact order, resolution and human-verification phrase"}
		}
		raw, err := json.Marshal(p)
		if err != nil {
			return err
		}
		record := submissionReconciliationRecord{SubmissionID: submission.ID, UserID: auth.CurrentUser(r).ID, Resolution: input.Resolution, SupplierOrderNumber: input.SupplierOrderNumber, VerificationEvidence: input.VerificationEvidence, ExpectedContext: p["expected_context"].(string), ReviewedPayload: json.RawMessage(raw), CreatedAt: time.Now().UTC()}
		if err := tx.Create(&record).Error; err != nil {
			return err
		}
		status := "sent"
		updates := map[string]any{"status": status, "supplier_order_number": input.SupplierOrderNumber}
		if input.Resolution == "confirmed_not_sent" {
			status = "cancelled"
			updates["status"] = status
		} else {
			updates["order_date"] = time.Now()
		}
		if err := tx.Model(&models.PurchaseOrder{}).Where("id=?", input.ID).Updates(updates).Error; err != nil {
			return err
		}
		var order models.PurchaseOrder
		if err := tx.Preload("Lines").First(&order, input.ID).Error; err != nil {
			return err
		}
		before := map[string]any{"purchase_order": p["current"], "submission": submission}
		after := map[string]any{"origin": "MCP/AI", "purchase_order": order, "submission": submission, "reconciliation": record}
		oldJSON, _ := json.Marshal(before)
		newJSON, _ := json.Marshal(after)
		if err := tx.Exec(`INSERT INTO audit_log(user_id,action,entity_type,entity_id,old_values,new_values,ip_address,user_agent) VALUES(?,'order.submission_reconciled','procurement_order',?,?::jsonb,?::jsonb,?,?)`, record.UserID, fmt.Sprint(input.ID), string(oldJSON), string(newJSON), requestIP(r), r.UserAgent()).Error; err != nil {
			return err
		}
		if err := tx.Create(&models.Activity{EntityType: "purchase_order", EntityID: order.ID, Action: "submission_reconciled", UserID: record.UserID, Username: auth.CurrentUser(r).Username, Details: "origin=MCP/AI resolution=" + input.Resolution}).Error; err != nil {
			return err
		}
		result = map[string]any{"operation_status": "reconciled", "purchase_order": order, "submission_id": submission.ID, "reconciliation_id": record.ID, "resolution": input.Resolution, "effects": p["effects"], "message": "Human supplier verification recorded. Original acknowledgement, claim and commercial lines remain retained. This order can never be sent again; any new demand requires a separate approved supplier cart."}
		return completeIdempotentMutation(tx, receipt, 200, result)
	})
	if errors.Is(err, errLifecyclePreview) {
		writeJSON(w, 200, result)
		return
	}
	if err != nil {
		writeProcurementFlowError(w, err)
		return
	}
	writeJSON(w, 200, result)
}

func prepareSubmissionReconciliation(tx *gorm.DB, input submissionReconciliationRequest, submission orderSubmissionRecord) (map[string]any, error) {
	current, err := procurementWorkflowRecord(tx, "orders", input.ID)
	if err != nil {
		return nil, err
	}
	required := []string{}
	if current["isArchived"] == true || (current["status"] != "submitting" && current["status"] != "submission_unknown") {
		required = append(required, "active_uncertain_order")
	}
	if submission.Status != "pending" && submission.Status != "unknown" {
		required = append(required, "uncertain_supplier_outcome")
	}
	eligibleAfter := submission.CreatedAt.Add(15 * time.Minute)
	if time.Now().Before(eligibleAfter) {
		required = append(required, "supplier_attempt_settled_15_minutes")
	}
	if !input.HumanVerified || len(input.VerificationEvidence) < 10 {
		required = append(required, "completed_human_supplier_check_and_evidence")
	}
	if input.Resolution == "found_order" && input.SupplierOrderNumber == "" || input.Resolution == "confirmed_not_sent" && input.SupplierOrderNumber != "" {
		required = append(required, "matching_supplier_order_number")
	}
	if input.ExpectedUpdatedAt != "" && input.ExpectedUpdatedAt != current["updatedAt"] || input.ConfirmChange && !input.Preview && input.ExpectedUpdatedAt == "" {
		required = append(required, "expected_updated_at")
	}
	deps := map[string]any{}
	query := func(key, q string, args ...any) error {
		rows, err := receiptSnapshot(tx, q, args...)
		if err != nil {
			return err
		}
		deps[key] = rows
		if len(rows) > 1000 {
			required = append(required, "bounded_reconciliation_context")
		}
		return nil
	}
	if err := query("reconciliations", `SELECT id,submission_id,user_id,resolution,supplier_order_number,verification_evidence,expected_context,reviewed_payload,created_at FROM proc_submission_reconciliations WHERE submission_id=?`, submission.ID); err != nil {
		return nil, err
	}
	if len(deps["reconciliations"].([]map[string]any)) > 0 {
		required = append(required, "no_previous_human_reconciliation")
	}
	if err := query("receipts", `SELECT id,purchase_order_line_id,quantity,received_at FROM proc_receipts WHERE purchase_order_id=? ORDER BY id`, input.ID); err != nil {
		return nil, err
	}
	if err := query("confirmations", `SELECT id,purchase_order_line_id,accepted_quantity,rejected_quantity,updated_at FROM proc_amazon_line_confirmations WHERE purchase_order_id=? ORDER BY id`, input.ID); err != nil {
		return nil, err
	}
	if len(deps["receipts"].([]map[string]any)) > 0 || len(deps["confirmations"].([]map[string]any)) > 0 {
		required = append(required, "no_physical_receipt_or_supplier_confirmation")
	}
	if err := query("supplier", `SELECT id,name,code,active,updated_at FROM proc_suppliers WHERE id=?`, current["supplierId"]); err != nil {
		return nil, err
	}
	if err := query("products", `SELECT DISTINCT p.id,p.sku,p.name,p.active,p.updated_at FROM proc_products p JOIN proc_purchase_order_lines l ON l.product_id=p.id WHERE l.purchase_order_id=? ORDER BY p.id`, input.ID); err != nil {
		return nil, err
	}
	if input.Resolution == "found_order" {
		suppliers := deps["supplier"].([]map[string]any)
		if len(suppliers) != 1 || suppliers[0]["active"] != true {
			required = append(required, "active_supplier")
		}
	}
	if err := query("duplicate_supplier_orders", `SELECT id,number,status,supplier_order_number,updated_at FROM proc_purchase_orders WHERE id<>? AND supplier_id=? AND ?<>'' AND lower(btrim(supplier_order_number))=lower(btrim(?)) ORDER BY id`, input.ID, current["supplierId"], input.SupplierOrderNumber, input.SupplierOrderNumber); err != nil {
		return nil, err
	}
	if len(deps["duplicate_supplier_orders"].([]map[string]any)) > 0 {
		required = append(required, "unique_supplier_order_number")
	}
	if rid, ok := current["requisitionId"]; ok && rid != nil {
		row, err := procurementWorkflowRecord(tx, "requisitions", int64(rid.(float64)))
		if err != nil {
			return nil, err
		}
		deps["requisition"] = row
		if input.Resolution == "found_order" && row["isArchived"] == true {
			required = append(required, "active_source_requisition")
		}
	}
	effects := map[string]any{"supplier_requests": false, "physical_stock_changed": false, "original_claim_and_acknowledgement_retained": true, "automatic_resubmission": false, "order_status": map[string]string{"found_order": "sent", "confirmed_not_sent": "cancelled"}[input.Resolution], "new_demand_requires_separate_approved_cart": true}
	proposed := map[string]any{"resolution": input.Resolution, "supplier_order_number": input.SupplierOrderNumber, "verification_evidence": input.VerificationEvidence, "human_verified": input.HumanVerified}
	raw, err := json.Marshal(map[string]any{"current": current, "submission": submission, "dependencies": deps, "proposed": proposed, "effects": effects, "eligible_after": eligibleAfter})
	if err != nil {
		return nil, err
	}
	if len(raw) > 1<<20 {
		return nil, &receiptFlowError{status: 413, code: "bounded_reconciliation_context", message: "Complete human verification exceeds bounds"}
	}
	hash := sha256.Sum256(raw)
	fingerprint := hex.EncodeToString(hash[:])
	if input.ExpectedContext != "" && input.ExpectedContext != fingerprint {
		required = append(required, "expected_context")
	}
	return map[string]any{"operation_status": "confirmation_required", "preview": true, "current": current, "submission": submission, "dependencies": deps, "proposed": proposed, "effects": effects, "eligible_after": eligibleAfter, "ready_to_execute": len(required) == 0, "required_fields": required, "expected_updated_at": current["updatedAt"], "expected_context": fingerprint, "required_confirmation_text": fmt.Sprintf("RECONCILE %s ORDER %d %s HUMAN VERIFIED %s", strings.ToUpper(submission.Provider), input.ID, strings.ToUpper(input.Resolution), fingerprint[:16])}, nil
}
