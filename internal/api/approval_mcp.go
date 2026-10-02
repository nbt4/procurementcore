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

	"github.com/go-chi/chi/v5"
	"gorm.io/gorm"
)

// These shapes retain the original native request hash for saved successes.
type requisitionDecisionInput struct {
	Decision          string     `json:"decision"`
	Note              string     `json:"note"`
	ExpectedUpdatedAt *time.Time `json:"expectedUpdatedAt"`
}
type orderStatusInput struct {
	Status              string     `json:"status"`
	SupplierOrderNumber string     `json:"supplierOrderNumber"`
	ExpectedDelivery    *time.Time `json:"expectedDelivery"`
	Notes               string     `json:"notes"`
	ExpectedUpdatedAt   *time.Time `json:"expectedUpdatedAt"`
}
type procurementApprovalRequest struct {
	ID                int64  `json:"id"`
	Decision          string `json:"decision,omitempty"`
	Note              string `json:"note,omitempty"`
	Status            string `json:"status,omitempty"`
	Reason            string `json:"reason,omitempty"`
	ExpectedUpdatedAt string `json:"expected_updated_at,omitempty"`
	ExpectedContext   string `json:"expected_context,omitempty"`
	ConfirmationText  string `json:"confirmation_text,omitempty"`
	ConfirmChange     bool   `json:"confirm_change,omitempty"`
	Preview           bool   `json:"preview,omitempty"`
}

func (h *Handler) approvalMCP(w http.ResponseWriter, r *http.Request) {
	entity := chi.URLParam(r, "entity")
	if entity != "requisitions" && entity != "orders" {
		notFound(w)
		return
	}
	if !signedProcurementUserDelegation(r, "cores:procurement:approve") {
		writeJSON(w, 403, map[string]string{"error": "Signed real-user approval delegation required"})
		return
	}
	var input procurementApprovalRequest
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16384))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil || decoder.Decode(new(any)) != io.EOF || input.ID < 1 || input.ID > math.MaxInt32 || len(input.Note) > 4000 || len(input.Reason) > 4000 || (entity == "requisitions" && (input.Status != "" || input.Reason != "")) || (entity == "orders" && (input.Decision != "" || input.Note != "")) {
		badRequest(w, "One bounded exact approval object required")
		return
	}
	input.Decision = strings.ToLower(strings.TrimSpace(input.Decision))
	input.Status = strings.ToLower(strings.TrimSpace(input.Status))
	input.Note = strings.TrimSpace(input.Note)
	input.Reason = strings.TrimSpace(input.Reason)
	preview := input.Preview || !input.ConfirmChange
	result := map[string]any{}
	err := h.db.WithContext(r.Context()).Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec(`SET LOCAL lock_timeout='5s';SET LOCAL statement_timeout='20s';SET LOCAL TIME ZONE 'UTC'`).Error; err != nil {
			return err
		}
		if err := currentProcurementApprovalRights(tx, r, entity, input.ID); err != nil {
			return err
		}
		var receipt *models.IdempotencyRecord
		if !preview {
			legacy, err := legacyProcurementApprovalReplay(tx, r, entity, input)
			if err != nil {
				return err
			}
			if legacy != nil {
				result = legacy
				return nil
			}
			if len(input.ExpectedContext) != 64 {
				return &receiptFlowError{status: 428, code: "context_required", message: "Copy exact final approval context"}
			}
			kind := map[string]string{"orders": "order_transition", "requisitions": "requisition_decide"}[entity]
			var replay json.RawMessage
			receipt, replay, err = beginIdempotentMutation(tx, r, fmt.Sprintf("mcp_%s:%d", kind, input.ID), input)
			if err != nil {
				return err
			}
			if replay != nil {
				return json.Unmarshal(replay, &result)
			}
		}
		if err := tx.Exec(`LOCK TABLE proc_suppliers,proc_products,proc_offers,proc_categories,core_product_links,proc_purchase_orders,proc_purchase_order_lines,proc_requisitions,proc_requisition_lines,proc_receipts IN SHARE ROW EXCLUSIVE MODE`).Error; err != nil {
			return err
		}
		p, err := prepareProcurementApproval(tx, entity, input)
		if err != nil {
			return err
		}
		result = p
		if preview || p["ready_to_execute"] != true {
			return errLifecyclePreview
		}
		if input.ConfirmationText != p["required_confirmation_text"] {
			return &receiptFlowError{status: 428, code: "confirmation_phrase_required", message: "Copy exact record/context-bound approval phrase"}
		}
		before := p["current"]
		user := auth.CurrentUser(r)
		if entity == "requisitions" {
			now := time.Now()
			if err := tx.Model(&models.Requisition{}).Where("id=?", input.ID).Updates(map[string]any{"status": input.Decision, "approved_by": user.ID, "approved_by_name": user.Username, "decision_note": input.Note, "decided_at": now}).Error; err != nil {
				return err
			}
			var after models.Requisition
			if err := tx.Preload("Lines").First(&after, input.ID).Error; err != nil {
				return err
			}
			if err := auditRequisitionMutation(tx, r, input.Decision, before, after); err != nil {
				return err
			}
			result = map[string]any{"operation_status": "decided", "requisition": after, "previous": before, "effects": p["effects"]}
		} else {
			updates := map[string]any{"status": input.Status}
			current := before.(map[string]any)
			if input.Status == "sent" && current["orderDate"] == nil {
				updates["order_date"] = time.Now()
			}
			if input.Status == "cancelled" {
				notes, _ := current["notes"].(string)
				if notes != "" {
					notes += "\n"
				}
				updates["notes"] = notes + "Cancellation: " + input.Reason
			}
			if err := tx.Model(&models.PurchaseOrder{}).Where("id=?", input.ID).Updates(updates).Error; err != nil {
				return err
			}
			var after models.PurchaseOrder
			if err := tx.Preload("Lines").First(&after, input.ID).Error; err != nil {
				return err
			}
			if err := auditOrderMutation(tx, r, input.Status, before, after); err != nil {
				return err
			}
			result = map[string]any{"operation_status": "transitioned", "purchase_order": after, "diff": p["diff"], "effects": p["effects"]}
		}
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

func currentProcurementApprovalRights(tx *gorm.DB, r *http.Request, entity string, id int64) error {
	user := auth.CurrentUser(r)
	var role struct{ Active, Admin bool }
	if err := tx.Raw("SELECT is_active AS active,is_admin AS admin FROM users WHERE userid=? FOR SHARE", user.ID).Scan(&role).Error; err != nil {
		return err
	}
	if !role.Active || !role.Admin {
		return &receiptFlowError{status: 403, code: "current_administrator_required", message: "Current active approval administrator required"}
	}
	if entity == "requisitions" {
		var requester uint
		if err := tx.Raw("SELECT requester_id FROM proc_requisitions WHERE id=?", id).Row().Scan(&requester); err != nil {
			return gorm.ErrRecordNotFound
		}
		if requester == user.ID {
			return &receiptFlowError{status: 403, code: "separation_of_duties", message: "A different administrator must decide the requisition"}
		}
	}
	return nil
}

func prepareProcurementApproval(tx *gorm.DB, entity string, input procurementApprovalRequest) (map[string]any, error) {
	current, err := procurementWorkflowRecord(tx, entity, input.ID)
	if err != nil {
		return nil, err
	}
	required := []string{}
	if current["isArchived"] == true {
		required = append(required, "active_workflow")
	}
	if lines, ok := current["lines"].([]any); ok && len(lines) > 1000 {
		required = append(required, "bounded_workflow_lines")
	}
	deps := map[string]any{}
	query := func(key, q string, args ...any) error {
		rows, err := receiptSnapshot(tx, q, args...)
		if err != nil {
			return err
		}
		deps[key] = rows
		if len(rows) > 1000 {
			required = append(required, "bounded_approval_context")
		}
		return nil
	}
	status, _ := current["status"].(string)
	action := input.Decision
	if entity == "requisitions" {
		if status != "submitted" {
			required = append(required, "submitted_requisition")
		}
		if action != "approved" && action != "rejected" && action != "returned" {
			required = append(required, "decision")
		}
		if action == "returned" && input.Note == "" {
			required = append(required, "note")
		}
		if err := query("products", `SELECT p.id,p.sku,p.name,p.active,p.updated_at FROM proc_products p JOIN proc_requisition_lines l ON l.product_id=p.id WHERE l.requisition_id=? ORDER BY p.id`, input.ID); err != nil {
			return nil, err
		}
		if err := query("suppliers", `SELECT s.id,s.name,s.active,s.updated_at FROM proc_suppliers s JOIN proc_requisition_lines l ON l.preferred_supplier_id=s.id WHERE l.requisition_id=? ORDER BY s.id`, input.ID); err != nil {
			return nil, err
		}
	} else {
		action = input.Status
		allowed := map[string][]string{"draft": {"sent", "cancelled"}, "sent": {"confirmed", "cancelled"}, "confirmed": {"cancelled"}, "partially_confirmed": {"cancelled"}, "partially_received": {"cancelled"}}
		found := false
		for _, next := range allowed[status] {
			if next == action {
				found = true
			}
		}
		if !found {
			required = append(required, "allowed_transition")
		}
		if action == "cancelled" && input.Reason == "" {
			required = append(required, "reason")
		}
		if action == "sent" && current["amazonPunchoutSessionId"] != nil {
			required = append(required, "native_amazon_punchout_required")
		}
		if err := query("supplier", `SELECT id,name,active,updated_at FROM proc_suppliers WHERE id=?`, current["supplierId"]); err != nil {
			return nil, err
		}
		if err := query("products", `SELECT p.id,p.sku,p.name,p.active,p.updated_at FROM proc_products p JOIN proc_purchase_order_lines l ON l.product_id=p.id WHERE l.purchase_order_id=? ORDER BY p.id`, input.ID); err != nil {
			return nil, err
		}
		if err := query("requisition", `SELECT id,number,status,is_archived,updated_at FROM proc_requisitions WHERE id=?`, current["requisitionId"]); err != nil {
			return nil, err
		}
		if err := query("receipts", `SELECT id,purchase_order_line_id,quantity,warehouse_product_id,putaway_task_id,received_at FROM proc_receipts WHERE purchase_order_id=? ORDER BY id`, input.ID); err != nil {
			return nil, err
		}
	}
	if action == "approved" || action == "sent" || action == "confirmed" {
		rawCurrent, err := json.Marshal(current)
		if err != nil {
			return nil, err
		}
		message := ""
		if entity == "requisitions" {
			var retained models.Requisition
			if err := json.Unmarshal(rawCurrent, &retained); err != nil {
				return nil, err
			}
			message = validateRequisition(&retained)
			parents := map[float64]bool{}
			for _, row := range deps["suppliers"].([]map[string]any) {
				id, _ := row["id"].(float64)
				parents[id] = true
			}
			if lines, ok := current["lines"].([]any); ok {
				for _, value := range lines {
					line, _ := value.(map[string]any)
					if id, ok := line["preferredSupplierId"].(float64); ok && !parents[id] {
						required = append(required, "existing_suppliers")
						break
					}
				}
			}
		} else {
			var retained models.PurchaseOrder
			if err := json.Unmarshal(rawCurrent, &retained); err != nil {
				return nil, err
			}
			retained.Status = "draft"
			message = validateOrder(&retained)
			if current["requisitionId"] != nil {
				parents := deps["requisition"].([]map[string]any)
				if len(parents) != 1 || parents[0]["is_archived"] == true {
					required = append(required, "active_original_requisition")
				}
			}
		}
		if message != "" {
			required = append(required, "valid_retained_fields")
		}
		for _, key := range []string{"products", "suppliers", "supplier"} {
			if rows, ok := deps[key].([]map[string]any); ok {
				for _, row := range rows {
					if row["active"] != true {
						required = append(required, "active_"+key)
						break
					}
				}
			}
		}
		if entity == "orders" && len(deps["supplier"].([]map[string]any)) != 1 {
			required = append(required, "active_supplier")
		}
		products := map[float64]bool{}
		for _, row := range deps["products"].([]map[string]any) {
			id, _ := row["id"].(float64)
			products[id] = true
		}
		lines, _ := current["lines"].([]any)
		if len(lines) == 0 {
			required = append(required, "workflow_lines")
		}
		for _, value := range lines {
			line, _ := value.(map[string]any)
			if id, ok := line["productId"].(float64); ok && !products[id] {
				required = append(required, "existing_products")
				break
			}
		}
	}
	if input.ExpectedUpdatedAt != "" && input.ExpectedUpdatedAt != current["updatedAt"] || input.ConfirmChange && !input.Preview && input.ExpectedUpdatedAt == "" {
		required = append(required, "expected_updated_at")
	}
	raw, err := json.Marshal(map[string]any{"entity": entity, "action": action, "note": input.Note, "reason": input.Reason, "current": current, "dependencies": deps})
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256(raw)
	fingerprint := hex.EncodeToString(digest[:])
	if input.ExpectedContext != "" && input.ExpectedContext != fingerprint {
		required = append(required, "expected_context")
	}
	verb := map[string]string{"approved": "APPROVE", "rejected": "REJECT", "returned": "RETURN", "sent": "SEND", "confirmed": "CONFIRM", "cancelled": "CANCEL"}[action]
	kind := map[string]string{"requisitions": "REQUISITION", "orders": "ORDER"}[entity]
	phrase := fmt.Sprintf("%s %s %d %s", verb, kind, input.ID, fingerprint[:16])
	diff := map[string]any{"status": map[string]any{"before": status, "after": action}}
	if entity == "requisitions" {
		diff["decisionNote"] = map[string]any{"before": current["decisionNote"], "after": input.Note}
	}
	if action == "cancelled" {
		notes, _ := current["notes"].(string)
		if notes != "" {
			notes += "\n"
		}
		diff["notes"] = map[string]any{"before": current["notes"], "after": notes + "Cancellation: " + input.Reason}
	}
	operationStatus := "confirmation_required"
	if len(required) > 0 {
		operationStatus = "needs_input"
	}
	return map[string]any{"operation_status": operationStatus, "preview": true, "current": current, "dependencies": deps, "diff": diff, "ready_to_execute": len(required) == 0, "required_fields": required, "expected_updated_at": current["updatedAt"], "expected_context": fingerprint, "required_confirmation_text": phrase, "effects": map[string]any{"external_messages": false, "physical_stock_changed": false, "existing_receipts_retained": true, "prices_changed": false}}, nil
}

func savedLegacyApproval(tx *gorm.DB, r *http.Request, entity string, id int64) (*models.IdempotencyRecord, error) {
	key := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if !validIdempotencyKey.MatchString(key) {
		return nil, &receiptFlowError{status: 428, code: "idempotency_key_required", message: "Valid idempotency key required"}
	}
	digest := sha256.Sum256([]byte(key))
	operation := "requisition_decision"
	if entity == "orders" {
		operation = fmt.Sprintf("order_update:%d", id)
	}
	var stored models.IdempotencyRecord
	err := tx.Where("user_id=? AND operation=? AND key_hash=?", auth.CurrentUser(r).ID, operation, hex.EncodeToString(digest[:])).First(&stored).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &stored, nil
}
func validateSavedApproval(stored *models.IdempotencyRecord, payload any, id int64) (json.RawMessage, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256(raw)
	if stored.RequestHash != hex.EncodeToString(digest[:]) {
		return nil, &receiptFlowError{status: 409, code: "idempotency_payload_conflict", message: "Previous approval key belongs to a different exact request"}
	}
	var record struct {
		ID int64 `json:"id"`
	}
	if stored.StatusCode != 200 || json.Unmarshal(stored.Response, &record) != nil || record.ID != id {
		return nil, &receiptFlowError{status: 409, code: "invalid_saved_approval", message: "Saved approval is incomplete"}
	}
	return stored.Response, nil
}
func legacyProcurementApprovalReplay(tx *gorm.DB, r *http.Request, entity string, input procurementApprovalRequest) (map[string]any, error) {
	stored, err := savedLegacyApproval(tx, r, entity, input.ID)
	if err != nil || stored == nil {
		return nil, err
	}
	if input.ExpectedContext != "" {
		return nil, &receiptFlowError{status: 409, code: "idempotency_payload_conflict", message: "Saved legacy approval has a different confirmation context"}
	}
	version, err := time.Parse(time.RFC3339Nano, input.ExpectedUpdatedAt)
	if err != nil {
		return nil, &receiptFlowError{status: 400, code: "invalid_version", message: "Invalid original approval version"}
	}
	action := input.Decision
	kind := "REQUISITION"
	if entity == "orders" {
		action = input.Status
		kind = "ORDER"
	}
	verb := map[string]string{"approved": "APPROVE", "rejected": "REJECT", "returned": "RETURN", "sent": "SEND", "confirmed": "CONFIRM", "cancelled": "CANCEL"}[action]
	if verb == "" || input.ConfirmationText != fmt.Sprintf("%s %s %d", verb, kind, input.ID) {
		return nil, &receiptFlowError{status: 428, code: "confirmation_phrase_required", message: "Copy original successful approval phrase"}
	}
	var payload any
	if entity == "requisitions" {
		payload = map[string]any{"id": uint(input.ID), "input": requisitionDecisionInput{Decision: input.Decision, Note: input.Note, ExpectedUpdatedAt: &version}}
	} else {
		var after models.PurchaseOrder
		if err := json.Unmarshal(stored.Response, &after); err != nil {
			return nil, err
		}
		if after.Status != input.Status {
			return nil, &receiptFlowError{status: 409, code: "idempotency_payload_conflict", message: "Saved transition has a different status"}
		}
		notes := after.Notes
		if input.Status == "cancelled" {
			var body map[string]any
			if err := json.Unmarshal(stored.Response, &body); err != nil {
				return nil, err
			}
			var original string
			err := tx.Raw(`SELECT COALESCE(old_values->>'notes','') FROM audit_log WHERE entity_type='procurement_order' AND entity_id=? AND user_id=? AND action='order.cancelled' AND new_values#>>'{purchase_order,updatedAt}'=? ORDER BY id DESC LIMIT 1`, fmt.Sprint(input.ID), auth.CurrentUser(r).ID, body["updatedAt"]).Row().Scan(&original)
			if err != nil {
				return nil, &receiptFlowError{status: 409, code: "invalid_saved_approval", message: "Original cancellation audit required"}
			}
			notes = original
			if notes != "" {
				notes += "\n"
			}
			notes += "Cancellation: " + input.Reason
		}
		payload = orderStatusInput{Status: input.Status, SupplierOrderNumber: after.SupplierOrderNumber, ExpectedDelivery: after.ExpectedDelivery, Notes: notes, ExpectedUpdatedAt: &version}
	}
	raw, err := validateSavedApproval(stored, payload, input.ID)
	if err != nil {
		return nil, err
	}
	key := "requisition"
	status := "decided"
	if entity == "orders" {
		key = "purchase_order"
		status = "transitioned"
	}
	return map[string]any{"operation_status": status, key: raw, "replayed_from_legacy": true}, nil
}

// Existing UI routes remain available; MCP requests can only replay their
// original durable successes. Every new approval uses the bound owner API.
func (h *Handler) onlyLegacyApprovalReplay(entity string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !isMCPMutation(r) {
				next.ServeHTTP(w, r)
				return
			}
			if !signedProcurementUserDelegation(r, "cores:procurement:approve") {
				writeJSON(w, 403, map[string]string{"error": "Signed approval delegation required"})
				return
			}
			id, ok := pathID(w, r)
			if !ok {
				return
			}
			var payload any
			decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16384))
			decoder.DisallowUnknownFields()
			if entity == "requisitions" {
				var input requisitionDecisionInput
				if err := decoder.Decode(&input); err != nil {
					badRequest(w, "Exact original decision required")
					return
				}
				payload = map[string]any{"id": id, "input": input}
			} else {
				var input orderStatusInput
				if err := decoder.Decode(&input); err != nil {
					badRequest(w, "Exact original transition required")
					return
				}
				payload = input
			}
			if decoder.Decode(new(any)) != io.EOF {
				badRequest(w, "One original approval object required")
				return
			}
			var response json.RawMessage
			err := h.db.WithContext(r.Context()).Transaction(func(tx *gorm.DB) error {
				if err := currentProcurementApprovalRights(tx, r, entity, int64(id)); err != nil {
					return err
				}
				stored, err := savedLegacyApproval(tx, r, entity, int64(id))
				if err != nil {
					return err
				}
				if stored == nil {
					return &receiptFlowError{status: 428, code: "context_required", message: "Prepare new approvals through the exact context-bound MCP owner endpoint"}
				}
				response, err = validateSavedApproval(stored, payload, int64(id))
				return err
			})
			if err != nil {
				writeProcurementFlowError(w, err)
				return
			}
			writeJSON(w, 200, response)
		})
	}
}
