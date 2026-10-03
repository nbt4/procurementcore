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
	"reflect"
	"sort"
	"strings"
	"time"

	"procurementcore/internal/auth"
	"procurementcore/internal/models"

	"github.com/go-chi/chi/v5"
	"gorm.io/gorm"
)

type orderDraftUpdateInput struct {
	models.PurchaseOrder
	ExpectedUpdatedAt *time.Time `json:"expectedUpdatedAt"`
}
type orderDraftLine struct {
	LineID         uint    `json:"line_id,omitempty"`
	ProductID      *uint   `json:"product_id,omitempty"`
	Description    string  `json:"description,omitempty"`
	Quantity       float64 `json:"quantity,omitempty"`
	Unit           string  `json:"unit,omitempty"`
	UnitPriceCents int64   `json:"unit_price_cents,omitempty"`
	PurchaseURL    string  `json:"purchase_url,omitempty"`
}
type orderDraftRequest struct {
	ID                  int64             `json:"id,omitempty"`
	SupplierID          *uint             `json:"supplier_id,omitempty"`
	SupplierQuery       string            `json:"supplier_query,omitempty"`
	Status              string            `json:"status,omitempty"`
	SupplierOrderNumber *string           `json:"supplier_order_number,omitempty"`
	Currency            *string           `json:"currency,omitempty"`
	OrderDate           *string           `json:"order_date,omitempty"`
	ExpectedDelivery    *string           `json:"expected_delivery,omitempty"`
	Notes               *string           `json:"notes,omitempty"`
	Lines               *[]orderDraftLine `json:"lines,omitempty"`
	ExpectedUpdatedAt   string            `json:"expected_updated_at,omitempty"`
	ExpectedContext     string            `json:"expected_context,omitempty"`
	ConfirmationText    string            `json:"confirmation_text,omitempty"`
	ConfirmChange       bool              `json:"confirm_change,omitempty"`
	Preview             bool              `json:"preview,omitempty"`
}

func (h *Handler) orderDraftMCP(w http.ResponseWriter, r *http.Request) {
	op := chi.URLParam(r, "operation")
	if op != "create" && op != "update" {
		notFound(w)
		return
	}
	if !signedProcurementUserDelegation(r, "cores:procurement:"+op) {
		writeJSON(w, 403, map[string]string{"error": "Signed real-user order draft action required"})
		return
	}
	var input orderDraftRequest
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 512<<10))
	d.DisallowUnknownFields()
	if err := d.Decode(&input); err != nil || d.Decode(new(any)) != io.EOF || input.ID < 0 || input.ID > math.MaxInt32 || (op == "create" && input.ID != 0) || (op == "update" && input.ID == 0) || (input.Status != "" && input.Status != "draft") || (op == "update" && input.Status != "") || len(input.SupplierQuery) > 240 || (op == "update" && input.SupplierQuery != "") || (input.Notes != nil && len(*input.Notes) > 4000) || (input.Lines != nil && len(*input.Lines) > 100) || (input.SupplierID != nil && (*input.SupplierID == 0 || *input.SupplierID > math.MaxInt32)) {
		badRequest(w, "One bounded exact order draft action required")
		return
	}
	preview := input.Preview || !input.ConfirmChange
	result := map[string]any{}
	err := h.db.WithContext(r.Context()).Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec(`SET LOCAL lock_timeout='5s';SET LOCAL statement_timeout='20s';SET LOCAL TIME ZONE 'UTC'`).Error; err != nil {
			return err
		}
		if err := currentProcurementApprovalRights(tx, r, "orders", input.ID); err != nil {
			return err
		}
		var receipt *models.IdempotencyRecord
		if !preview {
			legacy, err := legacyOrderDraftReplay(tx, r, op, input)
			if err != nil {
				return err
			}
			if legacy != nil {
				result = legacy
				return nil
			}
			if len(input.ExpectedContext) != 64 {
				return &receiptFlowError{status: 428, code: "context_required", message: "Copy the complete final order draft context"}
			}
			var replay json.RawMessage
			receipt, replay, err = beginIdempotentMutation(tx, r, fmt.Sprintf("mcp_order_draft_%s:%d", op, input.ID), input)
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
		p, draft, err := prepareOrderOwnerDraft(tx, r, op, input)
		if err != nil {
			return err
		}
		result = p
		if preview || p["ready_to_execute"] != true {
			return errLifecyclePreview
		}
		if input.ConfirmationText != p["required_confirmation_text"] {
			return &receiptFlowError{status: 428, code: "confirmation_phrase_required", message: "Copy exact order draft/context-bound phrase"}
		}
		if op == "create" {
			draft.Number = nextNumber("PO")
			if err := tx.Create(&draft).Error; err != nil {
				return err
			}
		} else {
			if err := tx.Model(&models.PurchaseOrder{}).Where("id=?", input.ID).Updates(map[string]any{"supplier_id": draft.SupplierID, "supplier_order_number": draft.SupplierOrderNumber, "currency": draft.Currency, "order_date": draft.OrderDate, "expected_delivery": draft.ExpectedDelivery, "notes": draft.Notes, "total_cents": draft.TotalCents}).Error; err != nil {
				return err
			}
			if input.Lines != nil {
				keep := []uint{}
				for _, l := range draft.Lines {
					if l.ID != 0 {
						keep = append(keep, l.ID)
					}
				}
				q := tx.Where("purchase_order_id=?", input.ID)
				if len(keep) > 0 {
					q = q.Where("id NOT IN ?", keep)
				}
				if err := q.Delete(&models.PurchaseOrderLine{}).Error; err != nil {
					return err
				}
				for _, l := range draft.Lines {
					if l.ID == 0 {
						if err := tx.Create(&l).Error; err != nil {
							return err
						}
					} else if err := tx.Model(&models.PurchaseOrderLine{}).Where("id=? AND purchase_order_id=?", l.ID, input.ID).Updates(map[string]any{"product_id": l.ProductID, "description": l.Description, "quantity": l.Quantity, "unit": l.Unit, "unit_price_cents": l.UnitPriceCents, "purchase_url": l.PurchaseURL}).Error; err != nil {
						return err
					}
				}
			}
		}
		var after models.PurchaseOrder
		if err := tx.Preload("Lines", func(db *gorm.DB) *gorm.DB { return db.Order("id") }).First(&after, draft.ID).Error; err != nil {
			return err
		}
		action := "created"
		if op == "update" {
			action = "draft_updated"
		}
		if err := auditOrderMutation(tx, r, action, p["current"], after); err != nil {
			return err
		}
		status := "created"
		if op == "update" {
			status = "updated"
		}
		result = map[string]any{"operation_status": status, "purchase_order": after, "diff": p["diff"], "effects": p["effects"]}
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

func applyOrderOwnerDraft(input orderDraftRequest, row *models.PurchaseOrder, create, legacy bool) error {
	if input.SupplierID != nil {
		row.SupplierID = *input.SupplierID
	}
	for _, f := range []struct {
		dst *string
		src *string
	}{{&row.SupplierOrderNumber, input.SupplierOrderNumber}, {&row.Currency, input.Currency}, {&row.Notes, input.Notes}} {
		if f.src != nil {
			*f.dst = strings.TrimSpace(*f.src)
		}
	}
	for _, f := range []struct {
		dst **time.Time
		src *string
	}{{&row.OrderDate, input.OrderDate}, {&row.ExpectedDelivery, input.ExpectedDelivery}} {
		if f.src == nil {
			continue
		}
		*f.dst = nil
		if raw := strings.TrimSpace(*f.src); raw != "" {
			layout := time.RFC3339Nano
			if create {
				layout = "2006-01-02"
			}
			parsed, err := time.Parse(layout, raw)
			if err != nil {
				return &receiptFlowError{status: 400, code: "invalid_order_date", message: "Create uses YYYY-MM-DD; update uses RFC3339 timestamps"}
			}
			parsed = parsed.UTC()
			*f.dst = &parsed
		}
	}
	if input.Lines != nil {
		old := map[uint]models.PurchaseOrderLine{}
		for _, l := range row.Lines {
			old[l.ID] = l
		}
		seen := map[uint]bool{}
		lines := make([]models.PurchaseOrderLine, 0, len(*input.Lines))
		for _, l := range *input.Lines {
			if l.LineID > math.MaxInt32 || l.ProductID != nil && (*l.ProductID == 0 || *l.ProductID > math.MaxInt32) {
				return &receiptFlowError{status: 400, code: "invalid_line_identity", message: "Positive bounded existing product/line IDs required"}
			}
			line := models.PurchaseOrderLine{}
			if l.LineID != 0 {
				original, ok := old[l.LineID]
				if legacy || !ok || seen[l.LineID] {
					return &receiptFlowError{status: 409, code: "invalid_line_identity", message: "Unique retained line IDs must belong to this original draft"}
				}
				line = original
				seen[l.LineID] = true
			}
			line.PurchaseOrderID = row.ID
			line.ProductID = l.ProductID
			line.Product = nil
			line.Description = l.Description
			line.Quantity = l.Quantity
			line.Unit = l.Unit
			line.UnitPriceCents = l.UnitPriceCents
			line.PurchaseURL = l.PurchaseURL
			lines = append(lines, line)
		}
		row.Lines = lines
	}
	return nil
}

func prepareOrderOwnerDraft(tx *gorm.DB, r *http.Request, op string, input orderDraftRequest) (map[string]any, models.PurchaseOrder, error) {
	var current map[string]any
	draft := models.PurchaseOrder{Status: "draft", Currency: "EUR", OrderedBy: auth.CurrentUser(r).ID, OrderedByName: auth.CurrentUser(r).Username}
	required := []string{}
	deps := map[string]any{}
	if op == "update" {
		var err error
		current, err = procurementWorkflowRecord(tx, "orders", input.ID)
		if err != nil {
			return nil, draft, err
		}
		raw, _ := json.Marshal(current)
		if err = json.Unmarshal(raw, &draft); err != nil {
			return nil, draft, err
		}
		if draft.IsArchived {
			required = append(required, "active_order")
		}
		if draft.Status != "draft" {
			required = append(required, "editable_draft")
		}
		if draft.AmazonPunchoutSessionID != nil {
			required = append(required, "native_amazon_punchout_required")
		}
		if input.ExpectedUpdatedAt != "" && input.ExpectedUpdatedAt != current["updatedAt"] || input.ConfirmChange && !input.Preview && input.ExpectedUpdatedAt == "" {
			required = append(required, "expected_updated_at")
		}
		rows, err := receiptSnapshot(tx, `SELECT id,purchase_order_line_id,quantity,received_at FROM proc_receipts WHERE purchase_order_id=? ORDER BY id`, input.ID)
		if err != nil {
			return nil, draft, err
		}
		deps["receipts"] = rows
		if len(rows) != 0 {
			required = append(required, "unreceived_draft")
		}
		rows, err = receiptSnapshot(tx, `SELECT id,purchase_order_line_id,accepted_quantity,rejected_quantity,updated_at FROM proc_amazon_line_confirmations WHERE purchase_order_id=? ORDER BY id`, input.ID)
		if err != nil {
			return nil, draft, err
		}
		deps["supplier_confirmations"] = rows
		if len(rows) != 0 {
			required = append(required, "unconfirmed_draft")
		}
		for _, l := range draft.Lines {
			if l.ReceivedQuantity != 0 || len(l.AmazonConfirmations) != 0 {
				required = append(required, "unreceived_draft")
				break
			}
		}
		if draft.RequisitionID != nil {
			rows, err := receiptSnapshot(tx, `SELECT id,number,status,is_archived,updated_at FROM proc_requisitions WHERE id=?`, *draft.RequisitionID)
			if err != nil {
				return nil, draft, err
			}
			deps["requisition"] = rows
			if len(rows) != 1 || rows[0]["is_archived"] == true {
				required = append(required, "active_original_requisition")
			}
		}
	}
	if err := applyOrderOwnerDraft(input, &draft, op == "create", false); err != nil {
		return nil, draft, err
	}
	if op == "create" && input.SupplierID == nil {
		query := strings.ToLower(strings.TrimSpace(input.SupplierQuery))
		rows, err := receiptSnapshot(tx, `SELECT id,name,code,active,updated_at FROM proc_suppliers WHERE active AND (?='' OR strpos(lower(name),?)>0 OR strpos(lower(code),?)>0) ORDER BY id`, query, query, query)
		if err != nil {
			return nil, draft, err
		}
		deps["supplier_candidates"] = rows
		exact := []map[string]any{}
		for _, row := range rows {
			if query != "" && (strings.EqualFold(strings.TrimSpace(fmt.Sprint(row["name"])), query) || strings.EqualFold(strings.TrimSpace(fmt.Sprint(row["code"])), query)) {
				exact = append(exact, row)
			}
		}
		selected := exact
		if len(exact) == 0 && query != "" {
			selected = rows
		}
		if len(rows) > 100 {
			required = append(required, "bounded_supplier_candidates")
		} else if len(selected) == 1 {
			draft.SupplierID = uint(selected[0]["id"].(float64))
		} else {
			required = append(required, "supplier_id")
		}
	}
	rows, err := receiptSnapshot(tx, `SELECT id,name,code,active,updated_at FROM proc_suppliers WHERE id=?`, draft.SupplierID)
	if err != nil {
		return nil, draft, err
	}
	deps["suppliers"] = rows
	if len(rows) != 1 || rows[0]["active"] != true {
		required = append(required, "active_supplier")
	}
	products := []map[string]any{}
	names := map[uint]string{}
	seen := map[uint]bool{}
	for i, l := range draft.Lines {
		if l.ProductID == nil || seen[*l.ProductID] {
			continue
		}
		seen[*l.ProductID] = true
		rows, err := receiptSnapshot(tx, `SELECT id,sku,name,active,updated_at FROM proc_products WHERE id=?`, *l.ProductID)
		if err != nil {
			return nil, draft, err
		}
		products = append(products, rows...)
		if len(rows) != 1 || rows[0]["active"] != true {
			required = append(required, fmt.Sprintf("lines[%d].active_product", i))
		} else {
			names[*l.ProductID] = fmt.Sprint(rows[0]["name"])
		}
	}
	deps["products"] = products
	if op == "create" {
		for i := range draft.Lines {
			l := &draft.Lines[i]
			if strings.TrimSpace(l.Description) == "" && l.ProductID != nil {
				l.Description = names[*l.ProductID]
			}
		}
	}
	retained := draft.Lines
	draft.Lines = append([]models.PurchaseOrderLine(nil), draft.Lines...)
	if msg := validateOrder(&draft); msg != "" {
		required = append(required, "valid_draft_fields")
	}
	if input.Lines == nil && op == "update" {
		draft.Lines = retained
	}
	if len(draft.Lines) > 100 {
		required = append(required, "bounded_draft_lines")
	}
	sort.SliceStable(draft.Lines, func(i, j int) bool {
		if draft.Lines[i].ID == 0 {
			return false
		}
		return draft.Lines[j].ID == 0 || draft.Lines[i].ID < draft.Lines[j].ID
	})
	duplicates := []map[string]any{}
	if draft.SupplierOrderNumber != "" {
		duplicates, err = receiptSnapshot(tx, `SELECT id,number,status,is_archived,supplier_order_number,updated_at FROM proc_purchase_orders WHERE id<>? AND supplier_id=? AND lower(btrim(supplier_order_number))=lower(btrim(?)) ORDER BY id`, input.ID, draft.SupplierID, draft.SupplierOrderNumber)
		if err != nil {
			return nil, draft, err
		}
		if len(duplicates) > 0 {
			required = append(required, "duplicate_supplier_order_number")
		}
	}
	raw, _ := json.Marshal(draft)
	draftMap := map[string]any{}
	if err = json.Unmarshal(raw, &draftMap); err != nil {
		return nil, draft, err
	}
	diff := map[string]any{}
	if op == "update" {
		for _, key := range []string{"supplierId", "supplierOrderNumber", "currency", "orderDate", "expectedDelivery", "notes", "lines", "totalCents"} {
			if !reflect.DeepEqual(current[key], draftMap[key]) {
				diff[key] = map[string]any{"before": current[key], "after": draftMap[key]}
			}
		}
		if len(diff) == 0 {
			required = append(required, "changes")
		}
	}
	body, err := json.Marshal(map[string]any{"operation": op, "current": current, "draft": draftMap, "dependencies": deps, "duplicates": duplicates})
	if err != nil {
		return nil, draft, err
	}
	if len(body) > 1<<20 {
		return nil, draft, &receiptFlowError{status: 413, code: "bounded_draft_context", message: "Complete order draft exceeds the bounded context size"}
	}
	digest := sha256.Sum256(body)
	fingerprint := hex.EncodeToString(digest[:])
	if input.ExpectedContext != "" && input.ExpectedContext != fingerprint {
		required = append(required, "expected_context")
	}
	version := ""
	if current != nil {
		version, _ = current["updatedAt"].(string)
	}
	status := "confirmation_required"
	if len(required) > 0 {
		status = "needs_input"
	}
	return map[string]any{"operation_status": status, "preview": true, "current": current, "draft": draftMap, "dependencies": deps, "duplicates": duplicates, "diff": diff, "ready_to_execute": len(required) == 0, "required_fields": required, "expected_updated_at": version, "expected_context": fingerprint, "required_confirmation_text": fmt.Sprintf("%s ORDER DRAFT %d %s", strings.ToUpper(op), input.ID, fingerprint[:16]), "effects": map[string]any{"external_messages": false, "physical_stock_changed": false, "omitted_lines_retained": true, "status_after": "draft"}}, draft, nil
}
