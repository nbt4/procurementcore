package api

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"procurementcore/internal/auth"
	"procurementcore/internal/models"

	"gorm.io/gorm"
)

func savedLegacyRequisitionDraft(tx *gorm.DB, r *http.Request, op string, id int64) (*models.IdempotencyRecord, error) {
	key := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if !validIdempotencyKey.MatchString(key) {
		return nil, &receiptFlowError{status: 428, code: "idempotency_key_required", message: "Valid idempotency key required"}
	}
	digest := sha256.Sum256([]byte(key))
	operation := "requisition_create"
	if op != "create" {
		operation = fmt.Sprintf("requisition_%s:%d", op, id)
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

func validateLegacyRequisitionDraft(stored *models.IdempotencyRecord, payload any, op string, id int64, uid uint) (json.RawMessage, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256(raw)
	if stored.RequestHash != hex.EncodeToString(digest[:]) {
		return nil, &receiptFlowError{status: 409, code: "idempotency_payload_conflict", message: "Previous requisition key belongs to a different exact request"}
	}
	var after models.Requisition
	status := 200
	if op == "create" {
		status = 201
	}
	if stored.StatusCode != status || json.Unmarshal(stored.Response, &after) != nil || after.ID == 0 || (op != "create" && int64(after.ID) != id) || (op == "create" && after.RequesterID != uid) {
		return nil, &receiptFlowError{status: 409, code: "invalid_saved_requisition", message: "Original successful requisition receipt required"}
	}
	return append(json.RawMessage(nil), stored.Response...), nil
}

func legacyRequisitionDraftReplay(tx *gorm.DB, r *http.Request, op string, input requisitionDraftRequest) (map[string]any, error) {
	stored, err := savedLegacyRequisitionDraft(tx, r, op, input.ID)
	if err != nil || stored == nil {
		return nil, err
	}
	if input.ExpectedContext != "" || input.AllowDuplicate {
		return nil, &receiptFlowError{status: 409, code: "idempotency_payload_conflict", message: "Saved old request does not contain a new context or duplicate override"}
	}
	var version time.Time
	if op != "create" {
		version, err = time.Parse(time.RFC3339Nano, input.ExpectedUpdatedAt)
		if err != nil {
			return nil, &receiptFlowError{status: 428, code: "original_version_required", message: "Copy the original saved request version"}
		}
	}
	var payload any
	if op == "submit" {
		if input.ConfirmationText != fmt.Sprintf("SUBMIT REQUISITION %d", input.ID) {
			return nil, &receiptFlowError{status: 428, code: "original_phrase_required", message: "Original successful submission phrase required"}
		}
		payload = requisitionSubmitInput{ExpectedUpdatedAt: &version}
	} else {
		var row models.Requisition
		if op == "update" {
			var after map[string]any
			if err = json.Unmarshal(stored.Response, &after); err != nil {
				return nil, err
			}
			var before string
			if err = tx.Raw(`SELECT old_values::text FROM audit_log WHERE entity_type='procurement_requisition' AND entity_id=? AND user_id=? AND action='requisition.updated' AND new_values#>>'{requisition,updatedAt}'=? ORDER BY id DESC LIMIT 1`, fmt.Sprint(input.ID), auth.CurrentUser(r).ID, after["updatedAt"]).Row().Scan(&before); err != nil {
				return nil, &receiptFlowError{status: 409, code: "original_audit_required", message: "Original draft audit required"}
			}
			var original models.Requisition
			if err = json.Unmarshal([]byte(before), &original); err != nil {
				return nil, err
			}
			row.Title, row.CostCenter, row.Justification, row.NeededBy = original.Title, original.CostCenter, original.Justification, original.NeededBy
			if row.NeededBy != nil {
				utc := row.NeededBy.UTC()
				row.NeededBy = &utc
			}
			if original.Status == "returned" {
				row.Status = "draft"
			}
			for _, l := range original.Lines {
				row.Lines = append(row.Lines, models.RequisitionLine{ProductID: l.ProductID, Description: l.Description, Quantity: l.Quantity, Unit: l.Unit, EstimatedPriceCents: l.EstimatedPriceCents, PreferredSupplierID: l.PreferredSupplierID, PurchaseURL: l.PurchaseURL})
			}
		}
		if err = applyRequisitionDraft(input, &row, true); err != nil {
			return nil, err
		}
		if msg := validateRequisition(&row); msg != "" {
			return nil, &receiptFlowError{status: 400, code: "invalid_original_draft", message: msg}
		}
		payload = row
		if op == "update" {
			payload = requisitionUpdateInput{Requisition: row, ExpectedUpdatedAt: &version}
		}
	}
	raw, err := validateLegacyRequisitionDraft(stored, payload, op, input.ID, auth.CurrentUser(r).ID)
	if err != nil {
		return nil, err
	}
	status := map[string]string{"create": "created", "update": "updated", "submit": "submitted"}[op]
	result := map[string]any{"operation_status": status, "requisition": raw, "replayed_from_legacy": true}
	if op == "create" {
		result["creation_status"] = "created"
	}
	return result, nil
}

// Browser workflows keep their native routes. Public MCP requests may only
// retrieve their exact previous success; new requests must review owner context.
func (h *Handler) onlyLegacyRequisitionDraftReplay(op string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !isMCPMutation(r) {
				next.ServeHTTP(w, r)
				return
			}
			if !signedProcurementUserDelegation(r, "cores:procurement:"+op) {
				writeJSON(w, 403, map[string]string{"error": "Signed requisition action required"})
				return
			}
			var id int64
			if op != "create" {
				nativeID, ok := pathID(w, r)
				if !ok {
					return
				}
				id = int64(nativeID)
			}
			d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 512<<10))
			d.DisallowUnknownFields()
			var payload any
			switch op {
			case "create":
				var row models.Requisition
				if err := d.Decode(&row); err != nil {
					badRequest(w, "Exact original draft required")
					return
				}
				if msg := validateRequisition(&row); msg != "" {
					badRequest(w, msg)
					return
				}
				payload = row
			case "update":
				var in requisitionUpdateInput
				if err := d.Decode(&in); err != nil {
					badRequest(w, "Exact original update required")
					return
				}
				if msg := validateRequisition(&in.Requisition); msg != "" {
					badRequest(w, msg)
					return
				}
				payload = in
			case "submit":
				var in requisitionSubmitInput
				if err := d.Decode(&in); err != nil {
					badRequest(w, "Exact original submission required")
					return
				}
				payload = in
			}
			if d.Decode(new(any)) != io.EOF {
				badRequest(w, "One original requisition object required")
				return
			}
			var response json.RawMessage
			status := 200
			if op == "create" {
				status = 201
			}
			err := h.db.WithContext(r.Context()).Transaction(func(tx *gorm.DB) error {
				if err := tx.Exec(`SET LOCAL lock_timeout='5s';SET LOCAL statement_timeout='20s';SET LOCAL TIME ZONE 'UTC'`).Error; err != nil {
					return err
				}
				if err := currentRequisitionDraftRights(tx, r, id); err != nil {
					return err
				}
				stored, err := savedLegacyRequisitionDraft(tx, r, op, id)
				if err != nil {
					return err
				}
				if stored == nil {
					return &receiptFlowError{status: 428, code: "context_required", message: "Prepare new requisition actions through the complete context-bound owner endpoint"}
				}
				response, err = validateLegacyRequisitionDraft(stored, payload, op, id, auth.CurrentUser(r).ID)
				return err
			})
			if err != nil {
				writeProcurementFlowError(w, err)
				return
			}
			writeJSON(w, status, response)
		})
	}
}
