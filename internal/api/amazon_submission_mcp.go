package api

import (
	"context"
	"crypto/rand"
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

	"procurementcore/internal/amazon"
	"procurementcore/internal/auth"
	"procurementcore/internal/models"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type amazonSubmissionRequest struct {
	ID                int64  `json:"id"`
	ExpectedUpdatedAt string `json:"expected_updated_at,omitempty"`
	ExpectedContext   string `json:"expected_context,omitempty"`
	ConfirmationText  string `json:"confirmation_text,omitempty"`
	ConfirmChange     bool   `json:"confirm_change,omitempty"`
	Preview           bool   `json:"preview,omitempty"`
}
type orderSubmissionRecord struct {
	ID              int64           `json:"id"`
	PurchaseOrderID int64           `json:"purchase_order_id"`
	Provider        string          `json:"provider"`
	UserID          uint            `json:"user_id"`
	PayloadID       string          `json:"payload_id"`
	ExpectedContext string          `json:"expected_context"`
	Status          string          `json:"status"`
	Outcome         json.RawMessage `json:"outcome"`
	ReviewedPayload json.RawMessage `json:"reviewed_payload"`
	CreatedAt       time.Time       `json:"created_at"`
	UpdatedAt       time.Time       `json:"updated_at"`
}

func (orderSubmissionRecord) TableName() string { return "proc_order_submissions" }

func (h *Handler) amazonSubmissionMCP(w http.ResponseWriter, r *http.Request) {
	if !signedProcurementUserDelegation(r, "cores:procurement:send") {
		writeJSON(w, 403, map[string]string{"error": "Explicit signed real-user supplier send scope required"})
		return
	}
	var input amazonSubmissionRequest
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16384))
	d.DisallowUnknownFields()
	if err := d.Decode(&input); err != nil || d.Decode(new(any)) != io.EOF || input.ID < 1 || input.ID > math.MaxInt32 {
		badRequest(w, "One bounded exact Amazon order required")
		return
	}
	h.runAmazonSubmission(w, r, input, false)
}

func prepareAmazonSubmission(tx *gorm.DB, r *http.Request, client *amazon.Client, input amazonSubmissionRequest, exact bool) (map[string]any, models.PurchaseOrder, amazon.Order, error) {
	current, err := procurementWorkflowRecord(tx, "orders", input.ID)
	if err != nil {
		return nil, models.PurchaseOrder{}, amazon.Order{}, err
	}
	var order models.PurchaseOrder
	raw, _ := json.Marshal(current)
	if err = json.Unmarshal(raw, &order); err != nil {
		return nil, order, amazon.Order{}, err
	}
	required := []string{}
	if order.IsArchived {
		required = append(required, "active_order")
	}
	if order.Status != "draft" || order.SupplierOrderNumber != "" || order.AmazonPayloadID != "" {
		required = append(required, "unsubmitted_draft")
	}
	if order.AmazonPunchoutSessionID == nil {
		required = append(required, "original_amazon_punchout")
	}
	if input.ExpectedUpdatedAt != "" && input.ExpectedUpdatedAt != current["updatedAt"] || exact && input.ConfirmChange && !input.Preview && input.ExpectedUpdatedAt == "" {
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
			required = append(required, "bounded_submission_context")
		}
		return nil
	}
	if err = query("supplier", `SELECT id,name,code,active,updated_at FROM proc_suppliers WHERE id=?`, order.SupplierID); err != nil {
		return nil, order, amazon.Order{}, err
	}
	s := deps["supplier"].([]map[string]any)
	if len(s) != 1 || s[0]["active"] != true || s[0]["code"] != "AMAZON-BUSINESS" {
		required = append(required, "active_amazon_supplier")
	}
	if err = query("products", `SELECT DISTINCT p.id,p.sku,p.name,p.active,p.updated_at FROM proc_products p JOIN proc_purchase_order_lines l ON l.product_id=p.id WHERE l.purchase_order_id=? ORDER BY p.id`, input.ID); err != nil {
		return nil, order, amazon.Order{}, err
	}
	for _, row := range deps["products"].([]map[string]any) {
		if row["active"] != true {
			required = append(required, "active_products")
		}
	}
	if err = query("receipts", `SELECT id,purchase_order_line_id,quantity,received_at FROM proc_receipts WHERE purchase_order_id=? ORDER BY id`, input.ID); err != nil {
		return nil, order, amazon.Order{}, err
	}
	if len(deps["receipts"].([]map[string]any)) > 0 {
		required = append(required, "unreceived_order")
	}
	if err = query("confirmations", `SELECT id,purchase_order_line_id,accepted_quantity,rejected_quantity,updated_at FROM proc_amazon_line_confirmations WHERE purchase_order_id=? ORDER BY id`, input.ID); err != nil {
		return nil, order, amazon.Order{}, err
	}
	if len(deps["confirmations"].([]map[string]any)) > 0 {
		required = append(required, "unconfirmed_order")
	}
	if err = query("submissions", `SELECT id,provider,status,created_at,updated_at FROM proc_order_submissions WHERE purchase_order_id=? ORDER BY id`, input.ID); err != nil {
		return nil, order, amazon.Order{}, err
	}
	if len(deps["submissions"].([]map[string]any)) > 0 {
		required = append(required, "no_previous_supplier_claim")
	}
	if order.RequisitionID == nil {
		required = append(required, "original_approved_requisition")
	}
	if order.RequisitionID != nil {
		row, err := procurementWorkflowRecord(tx, "requisitions", int64(*order.RequisitionID))
		if err != nil {
			return nil, order, amazon.Order{}, err
		}
		deps["requisition"] = row
		var source models.Requisition
		raw, _ := json.Marshal(row)
		if err := json.Unmarshal(raw, &source); err != nil {
			return nil, order, amazon.Order{}, err
		}
		if len(source.Lines) != len(order.Lines) {
			required = append(required, "unchanged_approved_cart")
		} else {
			for i, l := range source.Lines {
				p := order.Lines[i]
				if l.SupplierPartID != p.SupplierPartID || l.SupplierPartAuxiliaryID != p.SupplierPartAuxiliaryID || l.Quantity != p.Quantity || l.EstimatedPriceCents != p.UnitPriceCents || l.Unit != p.Unit || l.Description != p.Description {
					required = append(required, "unchanged_approved_cart")
					break
				}
			}
		}
		if row["isArchived"] == true || row["approvedBy"] == nil || row["approvedBy"] == row["requesterId"] || row["decidedAt"] == nil || row["status"] != "ordered" || row["amazonPunchoutSessionId"] != current["amazonPunchoutSessionId"] {
			required = append(required, "original_approved_requisition")
		}
	}
	remote := amazon.Order{Number: order.Number}
	var total int64
	if len(order.Lines) < 1 || len(order.Lines) > 50 {
		required = append(required, "bounded_amazon_lines")
	}
	for i, l := range order.Lines {
		if math.Trunc(l.Quantity) != l.Quantity || l.Quantity < 1 || l.Quantity > 999 || strings.TrimSpace(l.SupplierPartID) == "" || strings.TrimSpace(l.SupplierPartAuxiliaryID) == "" || strings.TrimSpace(l.Description) == "" || strings.TrimSpace(l.Unit) == "" || l.UnitPriceCents < 0 || l.UnitPriceCents > 1000000000 || l.ReceivedQuantity != 0 {
			required = append(required, fmt.Sprintf("lines[%d].amazon_line", i))
			continue
		}
		remote.Lines = append(remote.Lines, amazon.OrderLine{SupplierPartID: l.SupplierPartID, SupplierPartAuxiliaryID: l.SupplierPartAuxiliaryID, Description: l.Description, Quantity: int(l.Quantity), Unit: l.Unit, UnitPriceCents: l.UnitPriceCents})
		total += int64(l.Quantity) * l.UnitPriceCents
	}
	if !strings.EqualFold(order.Currency, "EUR") || total != order.TotalCents {
		required = append(required, "exact_eur_total")
	}
	var config any
	if client == nil || !client.ReadyToOrder() {
		required = append(required, "configured_amazon_ordering")
	} else {
		config = client.SubmissionReview()
	}
	effects := map[string]any{"external_order_will_be_sent": true, "supplier": "Amazon Business", "total_cents": total, "currency": "EUR", "physical_stock_changed": false, "uncertain_submission_never_automatically_retried": true}
	body, err := json.Marshal(map[string]any{"current": current, "dependencies": deps, "supplier_order": remote, "configuration": config, "effects": effects})
	if err != nil {
		return nil, order, remote, err
	}
	if len(body) > 1<<20 {
		return nil, order, remote, &receiptFlowError{status: 413, code: "bounded_submission_context", message: "Complete supplier preview exceeds bounds"}
	}
	digest := sha256.Sum256(body)
	fingerprint := hex.EncodeToString(digest[:])
	if input.ExpectedContext != "" && input.ExpectedContext != fingerprint {
		required = append(required, "expected_context")
	}
	return map[string]any{"operation_status": "confirmation_required", "preview": true, "current": current, "supplier_order": remote, "configuration": config, "dependencies": deps, "effects": effects, "ready_to_execute": len(required) == 0, "required_fields": required, "expected_updated_at": current["updatedAt"], "expected_context": fingerprint, "required_confirmation_text": fmt.Sprintf("SEND AMAZON ORDER %d EUR %d %s", input.ID, total, fingerprint[:16])}, order, remote, nil
}

func (h *Handler) runAmazonSubmission(w http.ResponseWriter, r *http.Request, input amazonSubmissionRequest, native bool) {
	preview := !native && (input.Preview || !input.ConfirmChange)
	result := map[string]any{}
	var claimed orderSubmissionRecord
	var remote amazon.Order
	var receipt *models.IdempotencyRecord
	err := h.db.WithContext(r.Context()).Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec(`SET LOCAL lock_timeout='5s';SET LOCAL statement_timeout='20s';SET LOCAL TIME ZONE 'UTC'`).Error; err != nil {
			return err
		}
		if err := currentProcurementApprovalRights(tx, r, "orders", 0); err != nil {
			return err
		}
		if !preview && !native {
			if len(input.ExpectedContext) != 64 {
				return &receiptFlowError{status: 428, code: "context_required", message: "Copy complete final supplier submission context"}
			}
			var replay json.RawMessage
			var err error
			receipt, replay, err = beginIdempotentMutation(tx, r, fmt.Sprintf("mcp_order_amazon_send:%d", input.ID), input)
			if err != nil {
				return err
			}
			if replay != nil {
				if err := json.Unmarshal(replay, &result); err != nil {
					return err
				}
				if result["operation_status"] == "pending" {
					var saved orderSubmissionRecord
					if err := tx.Where("purchase_order_id=?", input.ID).First(&saved).Error; err != nil {
						return err
					}
					if saved.Status != "pending" {
						result, err = finalizeAmazonSubmission(tx, r, saved, receipt)
						return err
					}
				}
				return nil
			}
		}
		if err := lockRequisitionOrderContext(tx); err != nil {
			return err
		}
		p, order, out, err := prepareAmazonSubmission(tx, r, h.amazon, input, !native)
		if err != nil {
			return err
		}
		result = p
		remote = out
		if native && p["ready_to_execute"] != true {
			return &receiptFlowError{status: 409, code: "amazon_submission_blocked", message: fmt.Sprint(p["required_fields"])}
		}
		if preview || p["ready_to_execute"] != true {
			return errLifecyclePreview
		}
		if !native && input.ConfirmationText != p["required_confirmation_text"] {
			return &receiptFlowError{status: 428, code: "confirmation_phrase_required", message: "Copy exact paid supplier order/context-bound phrase"}
		}
		var random [16]byte
		if _, err := rand.Read(random[:]); err != nil {
			return err
		}
		reviewedPayload, err := json.Marshal(p)
		if err != nil {
			return err
		}
		claimed = orderSubmissionRecord{PurchaseOrderID: input.ID, Provider: "amazon", UserID: auth.CurrentUser(r).ID, PayloadID: hex.EncodeToString(random[:]) + "@procurementcore", ExpectedContext: p["expected_context"].(string), Status: "pending", Outcome: json.RawMessage(`{}`), ReviewedPayload: json.RawMessage(reviewedPayload), CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
		if err := tx.Create(&claimed).Error; err != nil {
			return err
		}
		if err := tx.Model(&models.PurchaseOrder{}).Where("id=?", order.ID).Updates(map[string]any{"status": "submitting", "amazon_payload_id": claimed.PayloadID}).Error; err != nil {
			return err
		}
		if err := tx.Preload("Lines").First(&order, order.ID).Error; err != nil {
			return err
		}
		if err := auditOrderMutation(tx, r, "amazon_submission_requested", p["current"], order); err != nil {
			return err
		}
		result = map[string]any{"operation_status": "pending", "submission_id": claimed.ID, "purchase_order": order, "effects": map[string]any{"external_outcome": "unresolved", "automatic_resubmission": false, "physical_stock_changed": false}, "message": "A durable supplier claim exists. Check the external outcome; never order again automatically."}
		return completeIdempotentMutation(tx, receipt, 202, result)
	})
	if errors.Is(err, errLifecyclePreview) {
		writeJSON(w, 200, result)
		return
	}
	if err != nil {
		writeProcurementFlowError(w, err)
		return
	}
	if claimed.ID == 0 {
		writeJSON(w, 200, result)
		return
	}
	// The unique committed claim permits exactly this process to send once.
	// A retry/restart returns the claim or finalizes a saved outcome, never sends.
	sendErr := h.amazon.Submit(r.Context(), remote, claimed.PayloadID)
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 30*time.Second)
	defer cancel()
	completionRequest := r.WithContext(ctx)
	status := "accepted"
	outcome := map[string]any{"acknowledged": true}
	if sendErr != nil {
		status = "unknown"
		outcome = map[string]any{"acknowledged": false, "error_code": "supplier_response_uncertain"}
	}
	err = h.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&claimed, claimed.ID).Error; err != nil {
			return err
		}
		before := claimed
		raw, _ := json.Marshal(outcome)
		if err := tx.Model(&claimed).Updates(map[string]any{"status": status, "outcome": json.RawMessage(raw)}).Error; err != nil {
			return err
		}
		if err := tx.First(&claimed, claimed.ID).Error; err != nil {
			return err
		}
		return auditSupplierOutcome(tx, completionRequest, before, claimed)
	})
	if err != nil {
		writeProcurementFlowError(w, err)
		return
	}
	err = h.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var err error
		result, err = finalizeAmazonSubmission(tx, completionRequest, claimed, receipt)
		return err
	})
	if err != nil {
		writeProcurementFlowError(w, err)
		return
	}
	if native {
		if status == "unknown" {
			writeJSON(w, 502, map[string]any{"error": "Amazon-Bestellstatus unklar. Im Amazon Business Konto prüfen und nicht erneut bestellen.", "order": result["purchase_order"]})
			return
		}
		writeJSON(w, 200, result["purchase_order"])
		return
	}
	writeJSON(w, 200, result)
}

func auditSupplierOutcome(tx *gorm.DB, r *http.Request, before, after orderSubmissionRecord) error {
	action := "amazon_submission_outcome"
	if after.Provider == "adam_hall" {
		action = "adam_hall_submission_outcome"
	}
	oldJSON, _ := json.Marshal(before)
	newJSON, _ := json.Marshal(map[string]any{"origin": func() string {
		if isMCPMutation(r) {
			return "MCP/AI"
		}
		return "UI"
	}(), "submission": after})
	if err := tx.Exec(`INSERT INTO audit_log(user_id,action,entity_type,entity_id,old_values,new_values,ip_address,user_agent) VALUES(?,?,'procurement_order',?,?::jsonb,?::jsonb,?,?)`, after.UserID, "order."+action, fmt.Sprint(after.PurchaseOrderID), string(oldJSON), string(newJSON), requestIP(r), r.UserAgent()).Error; err != nil {
		return err
	}
	return tx.Create(&models.Activity{EntityType: "purchase_order", EntityID: uint(after.PurchaseOrderID), Action: action, UserID: after.UserID, Username: auth.CurrentUser(r).Username, Details: after.Status}).Error
}

func finalizeAmazonSubmission(tx *gorm.DB, r *http.Request, submission orderSubmissionRecord, receipt *models.IdempotencyRecord) (map[string]any, error) {
	if receipt != nil {
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(receipt, receipt.ID).Error; err != nil {
			return nil, err
		}
	}
	if err := tx.Clauses(clause.Locking{Strength: "SHARE"}).First(&submission, submission.ID).Error; err != nil {
		return nil, err
	}
	var order models.PurchaseOrder
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Preload("Lines").First(&order, submission.PurchaseOrderID).Error; err != nil {
		return nil, err
	}
	before := order
	status, action := "sent", "ordered_at_amazon"
	if submission.Provider == "adam_hall" {
		action = "ordered_at_adam_hall"
	}
	if submission.Status != "accepted" {
		status, action = "submission_unknown", "amazon_submission_unknown"
		if submission.Provider == "adam_hall" {
			action = "adam_hall_submission_unknown"
		}
	}
	if order.Status == "submitting" {
		updates := map[string]any{"status": status}
		if status == "sent" && order.OrderDate == nil {
			updates["order_date"] = time.Now()
		}
		if status == "sent" && submission.Provider == "adam_hall" {
			var outcome struct {
				SupplierOrderNumber string `json:"supplier_order_number"`
			}
			if err := json.Unmarshal(submission.Outcome, &outcome); err != nil {
				return nil, err
			}
			if outcome.SupplierOrderNumber == "" {
				return nil, errors.New("Saved supplier acknowledgement lacks its order reference")
			}
			updates["supplier_order_number"] = outcome.SupplierOrderNumber
		}
		if err := tx.Model(&models.PurchaseOrder{}).Where("id=?", order.ID).Updates(updates).Error; err != nil {
			return nil, err
		}
		if err := tx.Preload("Lines").First(&order, order.ID).Error; err != nil {
			return nil, err
		}
		if err := auditOrderMutation(tx, r, action, before, order); err != nil {
			return nil, err
		}
	}
	result := map[string]any{"operation_status": status, "purchase_order": order, "submission_id": submission.ID, "supplier_outcome": submission.Status, "effects": map[string]any{"external_order_may_exist": true, "automatic_resubmission": false, "physical_stock_changed": false}}
	if submission.Provider == "adam_hall" {
		var reviewed map[string]json.RawMessage
		if err := json.Unmarshal(submission.ReviewedPayload, &reviewed); err != nil {
			return nil, err
		}
		result["supplier_cart"] = reviewed["supplier_cart"]
		result["checkout_id"] = reviewed["checkout_id"]
	}
	if submission.Status != "accepted" {
		result["message"] = "Supplier outcome is uncertain. Reconcile in Amazon Business; never resend automatically."
		if submission.Provider == "adam_hall" {
			result["message"] = "Supplier outcome is uncertain. Check the Adam Hall account and original order; never resend automatically."
		}
	}
	if err := completeIdempotentMutation(tx, receipt, 200, result); err != nil {
		return nil, err
	}
	return result, nil
}
