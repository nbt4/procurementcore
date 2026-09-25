package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"procurementcore/internal/models"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func (h *Handler) updateOrderDraft(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var input struct {
		models.PurchaseOrder
		ExpectedUpdatedAt *time.Time `json:"expectedUpdatedAt"`
	}
	if !decode(w, r, &input) {
		return
	}
	if input.Status != "" && input.Status != "draft" {
		badRequest(w, "Nur Bestellentwürfe können vollständig bearbeitet werden")
		return
	}
	input.Status = "draft"
	if msg := validateOrder(&input.PurchaseOrder); msg != "" {
		badRequest(w, msg)
		return
	}
	var row models.PurchaseOrder
	err := h.db.Transaction(func(tx *gorm.DB) error {
		idempotency, replay, err := beginIdempotentMutation(tx, r, fmt.Sprintf("order_draft_update:%d", id), input)
		if err != nil {
			return err
		}
		if replay != nil {
			return json.Unmarshal(replay, &row)
		}
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Preload("Lines").First(&row, id).Error; err != nil {
			return err
		}
		if row.Status != "draft" {
			return &receiptFlowError{status: http.StatusConflict, code: "order_not_draft", message: "Nur Bestellentwürfe können vollständig geändert werden"}
		}
		if err := validateExpectedUpdate(row.UpdatedAt, input.ExpectedUpdatedAt, isMCPMutation(r)); err != nil {
			return err
		}
		if err := validateOrderReferences(tx, &input.PurchaseOrder); err != nil {
			return err
		}
		if input.SupplierOrderNumber != "" {
			var duplicates int64
			if err := tx.Model(&models.PurchaseOrder{}).Where("id<>? AND supplier_id=? AND lower(supplier_order_number)=lower(?)", id, input.SupplierID, input.SupplierOrderNumber).Count(&duplicates).Error; err != nil {
				return err
			}
			if duplicates > 0 {
				return &receiptFlowError{status: http.StatusConflict, code: "duplicate_supplier_order_number", message: "Lieferanten-Bestellnummer ist für diesen Lieferanten bereits vorhanden"}
			}
		}
		before := row
		row.SupplierID, row.SupplierOrderNumber, row.Currency, row.TotalCents = input.SupplierID, input.SupplierOrderNumber, input.Currency, input.TotalCents
		row.OrderDate, row.ExpectedDelivery, row.Notes = input.OrderDate, input.ExpectedDelivery, strings.TrimSpace(input.Notes)
		row.Lines = nil
		row.Supplier = nil
		row.UpdatedAt = time.Time{}
		if err := tx.Save(&row).Error; err != nil {
			return err
		}
		if err := tx.Where("purchase_order_id=?", id).Delete(&models.PurchaseOrderLine{}).Error; err != nil {
			return err
		}
		for i := range input.Lines {
			input.Lines[i].ID, input.Lines[i].PurchaseOrderID = 0, id
			input.Lines[i].Product = nil
			input.Lines[i].ReceivedQuantity = 0
		}
		if err := tx.Create(&input.Lines).Error; err != nil {
			return err
		}
		if err := tx.Preload("Lines").First(&row, id).Error; err != nil {
			return err
		}
		if err := auditOrderMutation(tx, r, "draft_updated", before, row); err != nil {
			return err
		}
		return completeIdempotentMutation(tx, idempotency, http.StatusOK, row)
	})
	if err != nil {
		writeProcurementFlowError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, row)
}
