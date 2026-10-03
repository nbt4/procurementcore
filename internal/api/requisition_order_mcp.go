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
	"procurementcore/internal/service"

	"gorm.io/gorm"
)

type requisitionOrderRequest struct {
	ID                int64      `json:"id"`
	SupplierID        uint       `json:"supplier_id"`
	ExpectedDelivery  *time.Time `json:"expected_delivery,omitempty"`
	ExpectedUpdatedAt string     `json:"expected_updated_at,omitempty"`
	ExpectedContext   string     `json:"expected_context,omitempty"`
	ConfirmationText  string     `json:"confirmation_text,omitempty"`
	ConfirmChange     bool       `json:"confirm_change,omitempty"`
	Preview           bool       `json:"preview,omitempty"`
}

func lockRequisitionOrderContext(tx *gorm.DB) error {
	return tx.Exec(`LOCK TABLE proc_suppliers,proc_products,proc_offers,proc_categories,core_product_links,proc_purchase_orders,proc_purchase_order_lines,proc_requisitions,proc_requisition_lines,proc_receipts IN SHARE ROW EXCLUSIVE MODE`).Error
}

func (h *Handler) requisitionOrderMCP(w http.ResponseWriter, r *http.Request) {
	if !signedProcurementUserDelegation(r, "cores:procurement:create") {
		writeJSON(w, 403, map[string]string{"error": "Signed real-user order creation required"})
		return
	}
	var input requisitionOrderRequest
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16384))
	d.DisallowUnknownFields()
	if err := d.Decode(&input); err != nil || d.Decode(new(any)) != io.EOF || input.ID < 1 || input.ID > math.MaxInt32 || input.SupplierID == 0 || input.SupplierID > math.MaxInt32 {
		badRequest(w, "One bounded exact requisition and supplier required")
		return
	}
	preview := input.Preview || !input.ConfirmChange
	result := map[string]any{}
	err := h.db.WithContext(r.Context()).Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec(`SET LOCAL lock_timeout='5s';SET LOCAL statement_timeout='20s';SET LOCAL TIME ZONE 'UTC'`).Error; err != nil {
			return err
		}
		// Every replay, including a cached MCP result, requires current owner rights.
		if err := currentProcurementApprovalRights(tx, r, "orders", 0); err != nil {
			return err
		}
		var receipt *models.IdempotencyRecord
		if !preview {
			if len(input.ExpectedContext) != 64 {
				return &receiptFlowError{status: 428, code: "context_required", message: "Copy complete final requisition order context"}
			}
			var replay json.RawMessage
			var err error
			receipt, replay, err = beginIdempotentMutation(tx, r, fmt.Sprintf("mcp_requisition_order:%d", input.ID), input)
			if err != nil {
				return err
			}
			if replay != nil {
				return json.Unmarshal(replay, &result)
			}
		}
		if err := lockRequisitionOrderContext(tx); err != nil {
			return err
		}
		p, order, err := prepareRequisitionOrder(tx, r, input, true)
		if err != nil {
			return err
		}
		result = p
		if preview || p["ready_to_execute"] != true {
			return errLifecyclePreview
		}
		if input.ConfirmationText != p["required_confirmation_text"] {
			return &receiptFlowError{status: 428, code: "confirmation_phrase_required", message: "Copy exact requisition/order/context-bound phrase"}
		}
		result, err = commitRequisitionOrder(tx, r, input.ID, p["current"], order)
		if err != nil {
			return err
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

// Both UI and MCP derive the same order while holding the same context locks.
// The requisition status is rechecked inside the transaction, never beforehand.
func prepareRequisitionOrder(tx *gorm.DB, r *http.Request, input requisitionOrderRequest, exact bool) (map[string]any, models.PurchaseOrder, error) {
	current, err := procurementWorkflowRecord(tx, "requisitions", input.ID)
	if err != nil {
		return nil, models.PurchaseOrder{}, err
	}
	var req models.Requisition
	raw, _ := json.Marshal(current)
	if err = json.Unmarshal(raw, &req); err != nil {
		return nil, models.PurchaseOrder{}, err
	}
	required := []string{}
	if req.IsArchived {
		required = append(required, "active_requisition")
	}
	if req.Status != "approved" {
		required = append(required, "approved_requisition")
	}
	if len(req.Lines) == 0 || len(req.Lines) > 100 {
		required = append(required, "bounded_requisition_lines")
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
			required = append(required, "bounded_order_context")
		}
		return nil
	}
	if err = query("suppliers", `SELECT id,name,code,active,updated_at FROM proc_suppliers WHERE id=?`, input.SupplierID); err != nil {
		return nil, models.PurchaseOrder{}, err
	}
	suppliers := deps["suppliers"].([]map[string]any)
	if len(suppliers) != 1 || suppliers[0]["active"] != true {
		required = append(required, "active_supplier")
	}
	if req.AmazonPunchoutSessionID != nil && (len(suppliers) != 1 || suppliers[0]["code"] != "AMAZON-BUSINESS") {
		required = append(required, "original_amazon_supplier")
	}
	if err = query("products", `SELECT DISTINCT p.id,p.sku,p.name,p.active,p.updated_at FROM proc_products p JOIN proc_requisition_lines l ON l.product_id=p.id WHERE l.requisition_id=? ORDER BY p.id`, input.ID); err != nil {
		return nil, models.PurchaseOrder{}, err
	}
	if err = query("preferred_suppliers", `SELECT DISTINCT s.id,s.name,s.code,s.active,s.updated_at FROM proc_suppliers s JOIN proc_requisition_lines l ON l.preferred_supplier_id=s.id WHERE l.requisition_id=? ORDER BY s.id`, input.ID); err != nil {
		return nil, models.PurchaseOrder{}, err
	}
	for _, key := range []string{"products", "preferred_suppliers"} {
		for _, row := range deps[key].([]map[string]any) {
			if row["active"] != true {
				required = append(required, "active_"+key)
			}
		}
	}
	if err = query("existing_orders", `SELECT id,number,status,is_archived,supplier_id,updated_at FROM proc_purchase_orders WHERE requisition_id=? ORDER BY id`, input.ID); err != nil {
		return nil, models.PurchaseOrder{}, err
	}
	if len(deps["existing_orders"].([]map[string]any)) > 0 {
		required = append(required, "unconverted_requisition")
	}
	// Bind every offer candidate, including native edits and changed cheapest ties.
	if err = query("offers", `SELECT o.id,o.product_id,o.supplier_id,o.supplier_sku,o.price_cents,o.currency,o.minimum_quantity,o.pack_size,o.lead_days,o.purchase_url,o.valid_until,o.active,o.updated_at FROM proc_offers o WHERE o.supplier_id=? AND o.product_id IN (SELECT product_id FROM proc_requisition_lines WHERE requisition_id=?) ORDER BY o.product_id,o.price_cents,o.id`, input.SupplierID, input.ID); err != nil {
		return nil, models.PurchaseOrder{}, err
	}
	order := models.PurchaseOrder{SupplierID: input.SupplierID, RequisitionID: &req.ID, Status: "draft", Currency: "EUR", OrderedBy: auth.CurrentUser(r).ID, OrderedByName: auth.CurrentUser(r).Username, ExpectedDelivery: input.ExpectedDelivery, AmazonPunchoutSessionID: req.AmazonPunchoutSessionID}
	lineSources := []map[string]any{}
	for i, line := range req.Lines {
		price, link := line.EstimatedPriceCents, line.PurchaseURL
		var offerID any
		if line.ProductID != nil && req.AmazonPunchoutSessionID == nil {
			var offer models.Offer
			err := tx.Where("product_id=? AND supplier_id=? AND active", *line.ProductID, input.SupplierID).Order("price_cents,id").First(&offer).Error
			if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
				return nil, order, err
			}
			if err == nil {
				offerID = offer.ID
				if line.PreferredSupplierID == nil || *line.PreferredSupplierID != input.SupplierID {
					price = offer.PriceCents
					if !strings.EqualFold(offer.Currency, "EUR") {
						required = append(required, fmt.Sprintf("lines[%d].offer_currency", i))
					}
				}
				if link == "" {
					link = offer.PurchaseURL
				}
				// Never round demand or silently reinterpret expired/pack prices.
				if offer.ValidUntil != nil && offer.ValidUntil.Before(time.Now()) {
					required = append(required, fmt.Sprintf("lines[%d].current_offer", i))
				}
				if line.Quantity < offer.MinimumQuantity {
					required = append(required, fmt.Sprintf("lines[%d].offer_minimum_quantity", i))
				}
				if offer.PackSize > 0 && math.Abs(line.Quantity/offer.PackSize-math.Round(line.Quantity/offer.PackSize)) > 1e-9 {
					required = append(required, fmt.Sprintf("lines[%d].offer_pack_size", i))
				}
			}
		}
		order.Lines = append(order.Lines, models.PurchaseOrderLine{ProductID: line.ProductID, SupplierPartID: line.SupplierPartID, SupplierPartAuxiliaryID: line.SupplierPartAuxiliaryID, Description: line.Description, Quantity: line.Quantity, Unit: line.Unit, UnitPriceCents: price, PurchaseURL: link})
		lineSources = append(lineSources, map[string]any{"requisition_line_id": line.ID, "selected_offer_id": offerID, "estimated_price_cents": line.EstimatedPriceCents, "order_price_cents": price})
	}
	order.TotalCents = service.PurchaseOrderTotal(order.Lines)
	// Validate on a clone; retain every original line field in the actual draft.
	validated := order
	validated.Lines = append([]models.PurchaseOrderLine(nil), order.Lines...)
	if msg := validateOrder(&validated); msg != "" {
		required = append(required, "valid_order_fields")
	}
	effects := map[string]any{"requisition_status_after": "ordered", "order_status_after": "draft", "external_messages": false, "physical_stock_changed": false, "original_requisition_lines_retained": true}
	body, err := json.Marshal(map[string]any{"current": current, "draft": order, "dependencies": deps, "line_sources": lineSources, "effects": effects})
	if err != nil {
		return nil, order, err
	}
	if len(body) > 1<<20 {
		return nil, order, &receiptFlowError{status: 413, code: "bounded_order_context", message: "Complete conversion context exceeds bounded preview"}
	}
	digest := sha256.Sum256(body)
	fingerprint := hex.EncodeToString(digest[:])
	if input.ExpectedContext != "" && input.ExpectedContext != fingerprint {
		required = append(required, "expected_context")
	}
	return map[string]any{"operation_status": "confirmation_required", "preview": true, "current": current, "draft": order, "dependencies": deps, "line_sources": lineSources, "effects": effects, "diff": map[string]any{"requisition_status": map[string]any{"before": req.Status, "after": "ordered"}, "new_order": order}, "ready_to_execute": len(required) == 0, "required_fields": required, "expected_updated_at": current["updatedAt"], "expected_context": fingerprint, "required_confirmation_text": fmt.Sprintf("CREATE ORDER FROM REQUISITION %d SUPPLIER %d %s", input.ID, input.SupplierID, fingerprint[:16])}, order, nil
}

func commitRequisitionOrder(tx *gorm.DB, r *http.Request, id int64, before any, order models.PurchaseOrder) (map[string]any, error) {
	order.Number = nextNumber("PO")
	if err := tx.Create(&order).Error; err != nil {
		return nil, err
	}
	// Do not Save the preloaded requisition: that could upsert original lines.
	change := tx.Model(&models.Requisition{}).Where("id=? AND status='approved' AND NOT is_archived", id).Update("status", "ordered")
	if change.Error != nil {
		return nil, change.Error
	}
	if change.RowsAffected != 1 {
		return nil, &receiptFlowError{status: 409, code: "approved_requisition_required", message: "Original requisition changed"}
	}
	var after models.Requisition
	if err := tx.Preload("Lines", func(db *gorm.DB) *gorm.DB { return db.Order("id") }).First(&after, id).Error; err != nil {
		return nil, err
	}
	if err := tx.Preload("Lines", func(db *gorm.DB) *gorm.DB { return db.Order("id") }).First(&order, order.ID).Error; err != nil {
		return nil, err
	}
	if err := auditOrderMutation(tx, r, "created_from_requisition", nil, order); err != nil {
		return nil, err
	}
	if err := auditRequisitionMutation(tx, r, "ordered", before, after); err != nil {
		return nil, err
	}
	return map[string]any{"operation_status": "created", "purchase_order": order, "requisition": after, "effects": map[string]any{"external_messages": false, "physical_stock_changed": false}}, nil
}
