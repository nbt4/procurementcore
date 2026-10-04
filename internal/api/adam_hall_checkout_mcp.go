package api

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"sort"
	"strings"
	"time"

	"procurementcore/internal/models"
	"procurementcore/internal/scraper"

	"gorm.io/gorm"
)

type adamHallCheckoutRecord struct {
	ID                 int64           `json:"id"`
	PurchaseOrderID    int64           `json:"purchase_order_id"`
	UserID             uint            `json:"user_id"`
	LocalContext       string          `json:"local_context"`
	AccountFingerprint string          `json:"account_fingerprint"`
	Status             string          `json:"status"`
	ReviewedRequest    json.RawMessage `json:"reviewed_request"`
	CartSnapshot       json.RawMessage `json:"cart_snapshot"`
	ContextCipher      []byte          `json:"-"`
	FailureCode        string          `json:"failure_code"`
	CreatedAt          time.Time       `json:"created_at"`
	UpdatedAt          time.Time       `json:"updated_at"`
}

func (adamHallCheckoutRecord) TableName() string { return "proc_adam_hall_checkouts" }

type adamHallCheckoutRequest struct {
	ID                int64  `json:"id"`
	CheckoutID        int64  `json:"checkout_id,omitempty"`
	ExpectedUpdatedAt string `json:"expected_updated_at,omitempty"`
	ExpectedContext   string `json:"expected_context,omitempty"`
	ConfirmationText  string `json:"confirmation_text,omitempty"`
	ConfirmChange     bool   `json:"confirm_change,omitempty"`
	Preview           bool   `json:"preview,omitempty"`
}

type adamHallOwnerDraft struct {
	Order                 models.PurchaseOrder
	Items                 []scraper.AdamHallItem
	ProductNumberByLineID map[uint]string
	Checkout              adamHallCheckoutRecord
	Cart                  scraper.AdamHallCart
}

func (h *Handler) prepareAdamHallOwner(tx *gorm.DB, r *http.Request, operation string, input adamHallCheckoutRequest) (map[string]any, adamHallOwnerDraft, error) {
	current, err := procurementWorkflowRecord(tx, "orders", input.ID)
	if err != nil {
		return nil, adamHallOwnerDraft{}, err
	}
	var draft adamHallOwnerDraft
	raw, _ := json.Marshal(current)
	if err := json.Unmarshal(raw, &draft.Order); err != nil {
		return nil, draft, err
	}
	required := []string{}
	if draft.Order.IsArchived || draft.Order.Status != "draft" || draft.Order.SupplierOrderNumber != "" || draft.Order.AmazonPayloadID != "" || draft.Order.AmazonPunchoutSessionID != nil {
		required = append(required, "active_unsubmitted_adam_hall_draft")
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
			required = append(required, "bounded_checkout_context")
		}
		return nil
	}
	if err := query("supplier", `SELECT id,name,code,website,active,updated_at FROM proc_suppliers WHERE id=?`, draft.Order.SupplierID); err != nil {
		return nil, draft, err
	}
	suppliers := deps["supplier"].([]map[string]any)
	if len(suppliers) != 1 || suppliers[0]["active"] != true {
		required = append(required, "active_supplier")
	} else {
		s := models.Supplier{Name: fmt.Sprint(suppliers[0]["name"]), Code: fmt.Sprint(suppliers[0]["code"]), Website: fmt.Sprint(suppliers[0]["website"])}
		if !isAdamHallSupplier(s) {
			required = append(required, "adam_hall_supplier")
		}
	}
	if err := query("products", `SELECT DISTINCT p.id,p.sku,p.name,p.unit,p.category_id,p.active,p.updated_at FROM proc_products p JOIN proc_purchase_order_lines l ON l.product_id=p.id WHERE l.purchase_order_id=? ORDER BY p.id`, input.ID); err != nil {
		return nil, draft, err
	}
	if err := query("offers", `SELECT o.id,o.product_id,o.supplier_sku,o.price_cents,o.currency,o.minimum_quantity,o.pack_size,o.valid_until,o.active,o.updated_at FROM proc_offers o WHERE o.supplier_id=? AND o.product_id IN(SELECT product_id FROM proc_purchase_order_lines WHERE purchase_order_id=?) ORDER BY o.product_id,o.price_cents,o.id`, draft.Order.SupplierID, input.ID); err != nil {
		return nil, draft, err
	}
	if err := query("categories", `SELECT DISTINCT c.id,c.active,c.updated_at FROM proc_categories c JOIN proc_products p ON p.category_id=c.id JOIN proc_purchase_order_lines l ON l.product_id=p.id WHERE l.purchase_order_id=? ORDER BY c.id`, input.ID); err != nil {
		return nil, draft, err
	}
	for _, category := range deps["categories"].([]map[string]any) {
		if category["active"] != true {
			required = append(required, "active_product_categories")
		}
	}
	if err := query("receipts", `SELECT id,purchase_order_line_id,quantity,received_at FROM proc_receipts WHERE purchase_order_id=? ORDER BY id`, input.ID); err != nil {
		return nil, draft, err
	}
	if err := query("confirmations", `SELECT id,purchase_order_line_id,accepted_quantity,rejected_quantity,updated_at FROM proc_amazon_line_confirmations WHERE purchase_order_id=? ORDER BY id`, input.ID); err != nil {
		return nil, draft, err
	}
	if err := query("submissions", `SELECT id,provider,status,created_at,updated_at FROM proc_order_submissions WHERE purchase_order_id=? ORDER BY id`, input.ID); err != nil {
		return nil, draft, err
	}
	if len(deps["receipts"].([]map[string]any)) > 0 || len(deps["confirmations"].([]map[string]any)) > 0 || len(deps["submissions"].([]map[string]any)) > 0 {
		required = append(required, "no_previous_receipt_confirmation_or_supplier_claim")
	}
	if draft.Order.RequisitionID != nil {
		source, err := procurementWorkflowRecord(tx, "requisitions", int64(*draft.Order.RequisitionID))
		if err != nil {
			return nil, draft, err
		}
		deps["requisition"] = source
		if source["isArchived"] == true || source["status"] != "ordered" || source["approvedBy"] == nil || source["approvedBy"] == source["requesterId"] || source["decidedAt"] == nil {
			required = append(required, "distinctly_approved_source_requisition")
		}
	}
	draft.ProductNumberByLineID = map[uint]string{}
	combined := map[string]int{}
	if len(draft.Order.Lines) < 1 || len(draft.Order.Lines) > 100 {
		required = append(required, "bounded_order_lines")
	}
	for _, line := range draft.Order.Lines {
		if line.ProductID == nil || math.Trunc(line.Quantity) != line.Quantity || line.Quantity < 1 || line.Quantity > 999 || line.ReceivedQuantity != 0 {
			required = append(required, "exact_active_product_integer_quantity")
			continue
		}
		var product models.Product
		if err := tx.First(&product, *line.ProductID).Error; err != nil {
			return nil, draft, err
		}
		if !product.Active {
			required = append(required, "active_products")
		}
		var offer models.Offer
		result := tx.Where("product_id=? AND supplier_id=? AND active", *line.ProductID, draft.Order.SupplierID).Order("price_cents,id").First(&offer)
		sku := strings.TrimSpace(product.SKU)
		if result.Error != nil && result.Error != gorm.ErrRecordNotFound {
			return nil, draft, result.Error
		}
		if result.Error == nil {
			if strings.TrimSpace(offer.SupplierSKU) != "" {
				sku = strings.TrimSpace(offer.SupplierSKU)
			}
			if !strings.EqualFold(offer.Currency, "EUR") || offer.ValidUntil != nil && offer.ValidUntil.Before(time.Now()) || offer.MinimumQuantity > line.Quantity || offer.PackSize > 0 && math.Abs(line.Quantity/offer.PackSize-math.Round(line.Quantity/offer.PackSize)) > 1e-8 {
				required = append(required, "compatible_active_supplier_offer")
			}
		}
		sku = strings.ToUpper(sku)
		if sku == "" || len(sku) > 120 {
			required = append(required, "bounded_supplier_product_number")
			continue
		}
		combined[sku] += int(line.Quantity)
		draft.ProductNumberByLineID[line.ID] = sku
	}
	numbers := make([]string, 0, len(combined))
	for sku := range combined {
		numbers = append(numbers, sku)
	}
	sort.Strings(numbers)
	for _, sku := range numbers {
		if combined[sku] > 999 {
			required = append(required, "bounded_combined_supplier_quantity")
		}
		draft.Items = append(draft.Items, scraper.AdamHallItem{ProductNumber: sku, Quantity: combined[sku]})
	}
	account := ""
	if h.scraper == nil || !h.scraper.AdamHallConfigured() {
		required = append(required, "configured_adam_hall_account")
	} else {
		account = h.scraper.AdamHallAccountFingerprint()
	}
	baseRaw, err := json.Marshal(map[string]any{"current": current, "dependencies": deps, "supplier_items": draft.Items, "account_fingerprint": account})
	if err != nil {
		return nil, draft, err
	}
	baseHash := sha256.Sum256(baseRaw)
	localContext := hex.EncodeToString(baseHash[:])
	if err := query("checkouts", `SELECT id,purchase_order_id,user_id,local_context,account_fingerprint,status,failure_code,created_at,updated_at FROM proc_adam_hall_checkouts WHERE purchase_order_id=? ORDER BY id`, input.ID); err != nil {
		return nil, draft, err
	}
	effects := map[string]any{"supplier": "Adam Hall", "account_fingerprint": account, "physical_stock_changed": false, "existing_unrelated_cart_contents_never_deleted": true, "remote_cart_preparation": operation == "cart", "paid_supplier_order": operation == "send", "automatic_resubmission": false}
	proposed := draft.Order
	if operation == "cart" {
		for _, row := range deps["checkouts"].([]map[string]any) {
			if row["status"] == "preparing" {
				var recent bool
				if err := tx.Raw("SELECT created_at>clock_timestamp() AT TIME ZONE 'UTC'-interval '15 minutes' FROM proc_adam_hall_checkouts WHERE id=?", row["id"]).Row().Scan(&recent); err != nil {
					return nil, draft, err
				}
				if recent {
					required = append(required, "no_live_remote_cart_preparation")
				}
			}
		}
	} else {
		q := tx.Where("purchase_order_id=? AND status='ready'", input.ID)
		if input.CheckoutID > 0 {
			q = q.Where("id=?", input.CheckoutID)
		}
		err := q.Order("id DESC").First(&draft.Checkout).Error
		if err != nil && err != gorm.ErrRecordNotFound {
			return nil, draft, err
		}
		if err == gorm.ErrRecordNotFound {
			required = append(required, "ready_reviewed_checkout")
		} else {
			if draft.Checkout.LocalContext != localContext || draft.Checkout.AccountFingerprint != account {
				required = append(required, "unchanged_order_catalog_and_supplier_account")
			}
			if time.Now().After(draft.Checkout.CreatedAt.Add(15 * time.Minute)) {
				required = append(required, "fresh_supplier_checkout")
			}
			if input.ConfirmChange && !input.Preview && input.CheckoutID == 0 {
				required = append(required, "exact_checkout_id")
			}
			if err := json.Unmarshal(draft.Checkout.CartSnapshot, &draft.Cart); err != nil {
				return nil, draft, err
			}
			prices := map[string]int64{}
			for _, line := range draft.Cart.Lines {
				prices[strings.ToUpper(strings.TrimSpace(line.ProductNumber))] = line.UnitPriceCents
			}
			proposed.Lines = append([]models.PurchaseOrderLine(nil), draft.Order.Lines...)
			for i := range proposed.Lines {
				price, ok := prices[draft.ProductNumberByLineID[proposed.Lines[i].ID]]
				if !ok {
					required = append(required, "complete_reviewed_supplier_prices")
				}
				proposed.Lines[i].UnitPriceCents = price
			}
			proposed.Currency = draft.Cart.Currency
			proposed.TotalCents = draft.Cart.TotalCents
		}
	}
	preview := map[string]any{"current": current, "dependencies": deps, "supplier_items": draft.Items, "local_context": localContext, "effects": effects, "proposed_order": proposed}
	if operation == "send" {
		preview["checkout"] = draft.Checkout
		preview["checkout_id"] = draft.Checkout.ID
		preview["supplier_cart"] = draft.Cart
	}
	raw, err = json.Marshal(preview)
	if err != nil {
		return nil, draft, err
	}
	if len(raw) > 1<<20 {
		return nil, draft, &receiptFlowError{status: 413, code: "bounded_checkout_context", message: "Complete supplier checkout exceeds bounds"}
	}
	hash := sha256.Sum256(raw)
	fingerprint := hex.EncodeToString(hash[:])
	if input.ExpectedContext != "" && input.ExpectedContext != fingerprint {
		required = append(required, "expected_context")
	}
	phrase := fmt.Sprintf("BUILD ADAM HALL CART ORDER %d %s", input.ID, fingerprint[:16])
	if operation == "send" {
		phrase = fmt.Sprintf("SEND ADAM HALL ORDER %d EUR %d CHECKOUT %d %s", input.ID, draft.Cart.TotalCents, draft.Checkout.ID, fingerprint[:16])
	}
	preview["operation_status"] = "confirmation_required"
	preview["preview"] = true
	preview["ready_to_execute"] = len(required) == 0
	preview["required_fields"] = required
	preview["expected_updated_at"] = current["updatedAt"]
	preview["expected_context"] = fingerprint
	preview["required_confirmation_text"] = phrase
	return preview, draft, nil
}
