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

func savedLegacyOrderDraft(tx *gorm.DB, r *http.Request, op string, id int64) (*models.IdempotencyRecord, error) {
	key := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if !validIdempotencyKey.MatchString(key) {
		return nil, &receiptFlowError{status: 428, code: "idempotency_key_required", message: "Valid idempotency key required"}
	}
	digest := sha256.Sum256([]byte(key))
	operation := "order_create"
	if op == "update" {
		operation = fmt.Sprintf("order_draft_update:%d", id)
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
func validateLegacyOrderDraft(stored *models.IdempotencyRecord, payload any, op string, id int64, uid uint) (json.RawMessage, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256(raw)
	if stored.RequestHash != hex.EncodeToString(digest[:]) {
		return nil, &receiptFlowError{status: 409, code: "idempotency_payload_conflict", message: "Previous order key belongs to a different exact native request"}
	}
	var row models.PurchaseOrder
	status := 200
	if op == "create" {
		status = 201
	}
	if stored.StatusCode != status || json.Unmarshal(stored.Response, &row) != nil || row.ID == 0 || op == "update" && int64(row.ID) != id || op == "create" && row.OrderedBy != uid {
		return nil, &receiptFlowError{status: 409, code: "invalid_saved_order", message: "Original successful native order draft receipt required"}
	}
	return append(json.RawMessage(nil), stored.Response...), nil
}
func legacyOrderDraftReplay(tx *gorm.DB, r *http.Request, op string, input orderDraftRequest) (map[string]any, error) {
	stored, err := savedLegacyOrderDraft(tx, r, op, input.ID)
	if err != nil || stored == nil {
		return nil, err
	}
	if input.ExpectedContext != "" {
		return nil, &receiptFlowError{status: 409, code: "idempotency_payload_conflict", message: "Old saved request has no new context"}
	}
	var after models.PurchaseOrder
	if err = json.Unmarshal(stored.Response, &after); err != nil {
		return nil, err
	}
	row := models.PurchaseOrder{Status: "draft"}
	if op == "create" {
		if input.SupplierID == nil {
			var supplier models.Supplier
			if err := tx.First(&supplier, after.SupplierID).Error; err != nil {
				return nil, err
			}
			q := strings.ToLower(strings.TrimSpace(input.SupplierQuery))
			if q == "" || !strings.Contains(strings.ToLower(supplier.Name), q) && !strings.Contains(strings.ToLower(supplier.Code), q) {
				return nil, &receiptFlowError{status: 428, code: "original_supplier_id_required", message: "Copy the original saved supplier ID when its query no longer resolves"}
			}
			originalID := after.SupplierID
			input.SupplierID = &originalID
		}
	} else {
		var saved map[string]any
		if err = json.Unmarshal(stored.Response, &saved); err != nil {
			return nil, err
		}
		var before string
		if err = tx.Raw(`SELECT old_values::text FROM audit_log WHERE entity_type='procurement_order' AND entity_id=? AND user_id=? AND action='order.draft_updated' AND new_values#>>'{purchase_order,updatedAt}'=? ORDER BY id DESC LIMIT 1`, fmt.Sprint(input.ID), auth.CurrentUser(r).ID, saved["updatedAt"]).Row().Scan(&before); err != nil {
			return nil, &receiptFlowError{status: 409, code: "original_audit_required", message: "Original order draft audit required"}
		}
		var original models.PurchaseOrder
		if err = json.Unmarshal([]byte(before), &original); err != nil {
			return nil, err
		}
		row.SupplierID, row.SupplierOrderNumber, row.Currency, row.OrderDate, row.ExpectedDelivery, row.Notes = original.SupplierID, original.SupplierOrderNumber, original.Currency, original.OrderDate, original.ExpectedDelivery, original.Notes
		for _, dst := range []**time.Time{&row.OrderDate, &row.ExpectedDelivery} {
			if *dst != nil {
				utc := (*dst).UTC()
				*dst = &utc
			}
		}
		for _, l := range original.Lines {
			row.Lines = append(row.Lines, models.PurchaseOrderLine{ProductID: l.ProductID, Description: l.Description, Quantity: l.Quantity, Unit: l.Unit, UnitPriceCents: l.UnitPriceCents, PurchaseURL: l.PurchaseURL})
		}
	}
	if err = applyOrderOwnerDraft(input, &row, op == "create", true); err != nil {
		return nil, err
	}
	if op == "create" {
		// Product-name defaults belong to the original saved business draft, even
		// after a later catalog rename. Explicit fields still reconstruct the hash.
		for i := range row.Lines {
			l := &row.Lines[i]
			if strings.TrimSpace(l.Description) == "" && l.ProductID != nil && i < len(after.Lines) && after.Lines[i].ProductID != nil && *l.ProductID == *after.Lines[i].ProductID {
				l.Description = after.Lines[i].Description
			}
		}
	}
	if msg := validateOrder(&row); msg != "" {
		return nil, &receiptFlowError{status: 400, code: "invalid_original_order", message: msg}
	}
	var payload any = row
	if op == "update" {
		version, err := time.Parse(time.RFC3339Nano, input.ExpectedUpdatedAt)
		if err != nil {
			return nil, &receiptFlowError{status: 428, code: "original_version_required", message: "Original saved order version required"}
		}
		payload = orderDraftUpdateInput{PurchaseOrder: row, ExpectedUpdatedAt: &version}
	}
	raw, err := validateLegacyOrderDraft(stored, payload, op, input.ID, auth.CurrentUser(r).ID)
	if err != nil {
		return nil, err
	}
	status := "created"
	if op == "update" {
		status = "updated"
	}
	return map[string]any{"operation_status": status, "purchase_order": raw, "replayed_from_legacy": true}, nil
}

func (h *Handler) onlyLegacyOrderDraftReplay(op string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !isMCPMutation(r) {
				next.ServeHTTP(w, r)
				return
			}
			if !signedProcurementUserDelegation(r, "cores:procurement:"+op) {
				writeJSON(w, 403, map[string]string{"error": "Signed order draft action required"})
				return
			}
			var id int64
			if op == "update" {
				nativeID, ok := pathID(w, r)
				if !ok {
					return
				}
				id = int64(nativeID)
			}
			d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 512<<10))
			d.DisallowUnknownFields()
			var payload any
			if op == "create" {
				var row models.PurchaseOrder
				if err := d.Decode(&row); err != nil {
					badRequest(w, "Exact original draft required")
					return
				}
				if msg := validateOrder(&row); msg != "" {
					badRequest(w, msg)
					return
				}
				payload = row
			} else {
				var in orderDraftUpdateInput
				if err := d.Decode(&in); err != nil {
					badRequest(w, "Exact original update required")
					return
				}
				if in.Status != "" && in.Status != "draft" {
					badRequest(w, "Draft status required")
					return
				}
				in.Status = "draft"
				if msg := validateOrder(&in.PurchaseOrder); msg != "" {
					badRequest(w, msg)
					return
				}
				payload = in
			}
			if d.Decode(new(any)) != io.EOF {
				badRequest(w, "One original order object required")
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
				if err := currentProcurementApprovalRights(tx, r, "orders", id); err != nil {
					return err
				}
				stored, err := savedLegacyOrderDraft(tx, r, op, id)
				if err != nil {
					return err
				}
				if stored == nil {
					return &receiptFlowError{status: 428, code: "context_required", message: "Prepare new order drafts through the complete context-bound owner endpoint"}
				}
				response, err = validateLegacyOrderDraft(stored, payload, op, id, auth.CurrentUser(r).ID)
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
