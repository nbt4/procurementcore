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

	"github.com/golang-jwt/jwt/v5"
	commonjwt "github.com/nbt4/cores-common/pkg/jwt"
	"gorm.io/gorm"
)

type goodsReceiptMCPRequest struct {
	OrderID           int64    `json:"order_id"`
	LineID            int64    `json:"line_id"`
	Quantity          float64  `json:"quantity"`
	Note              string   `json:"note"`
	SerialNumbers     []string `json:"serial_numbers"`
	TargetZoneID      *int64   `json:"target_zone_id"`
	AllowOverdelivery bool     `json:"allow_overdelivery"`
	ExpectedUpdatedAt string   `json:"expected_updated_at"`
	ExpectedContext   string   `json:"expected_context"`
	ConfirmationText  string   `json:"confirmation_text"`
	ConfirmReceipt    bool     `json:"confirm_receipt"`
	Preview           bool     `json:"preview"`
}

func signedProcurementDelegation(r *http.Request, scope string) bool {
	user := auth.CurrentUser(r)
	if user.ID == 0 || !user.IsAdmin || !isMCPMutation(r) {
		return false
	}
	var claims struct {
		UID   uint   `json:"uid"`
		Scope string `json:"mcp_scope"`
		jwt.RegisteredClaims
	}
	raw := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if cookie, err := r.Cookie("cores_token"); err == nil {
		raw = cookie.Value
	}
	token, err := jwt.ParseWithClaims(raw, &claims, func(*jwt.Token) (any, error) { return commonjwt.JWTSecret(), nil }, jwt.WithValidMethods([]string{"HS256"}), jwt.WithExpirationRequired())
	return err == nil && token.Valid && claims.UID == user.ID && claims.Scope == scope
}

func receiptNativeInput(input goodsReceiptMCPRequest) (goodsReceiptInput, error) {
	var version *time.Time
	if input.ExpectedUpdatedAt != "" {
		parsed, err := time.Parse(time.RFC3339Nano, input.ExpectedUpdatedAt)
		if err != nil {
			return goodsReceiptInput{}, err
		}
		version = &parsed
	}
	return goodsReceiptInput{LineID: uint(input.LineID), Quantity: input.Quantity, Note: strings.TrimSpace(input.Note), ExpectedUpdatedAt: version, SerialNumbers: input.SerialNumbers, TargetZoneID: input.TargetZoneID, AllowOverdelivery: input.AllowOverdelivery}, nil
}

// Existing successful receipts remain usable after introducing context-bound
// execution. Reconstruct exactly the old native request instead of booking again.
func legacyGoodsReceiptReplay(tx *gorm.DB, r *http.Request, input goodsReceiptMCPRequest) (json.RawMessage, error) {
	key := r.Header.Get("Idempotency-Key")
	if !validIdempotencyKey.MatchString(key) {
		return nil, &receiptFlowError{status: 428, code: "idempotency_key_required", message: "Valid idempotency key required"}
	}
	digest := sha256.Sum256([]byte(key))
	var stored models.IdempotencyRecord
	err := tx.Where("user_id=? AND operation='purchase_order_receipt' AND key_hash=?", auth.CurrentUser(r).ID, hex.EncodeToString(digest[:])).First(&stored).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	native, err := receiptNativeInput(input)
	if err != nil {
		return nil, &receiptFlowError{status: 400, code: "invalid_version", message: "Invalid exact order version"}
	}
	raw, err := json.Marshal(map[string]any{"id": uint(input.OrderID), "input": native})
	if err != nil {
		return nil, err
	}
	requestDigest := sha256.Sum256(raw)
	if stored.RequestHash != hex.EncodeToString(requestDigest[:]) {
		return nil, &receiptFlowError{status: 409, code: "idempotency_payload_conflict", message: "Previous receipt key belongs to a different exact request"}
	}
	var receipt models.Receipt
	if stored.StatusCode != http.StatusCreated || json.Unmarshal(stored.Response, &receipt) != nil || receipt.ID == 0 || receipt.PurchaseOrderID != uint(input.OrderID) || receipt.PurchaseOrderLineID != uint(input.LineID) {
		return nil, &receiptFlowError{status: 409, code: "invalid_saved_receipt", message: "Saved goods receipt is incomplete"}
	}
	return stored.Response, nil
}

func (h *Handler) goodsReceiptMCP(w http.ResponseWriter, r *http.Request) {
	if !signedProcurementDelegation(r, "cores:procurement:receive") {
		writeJSON(w, 403, map[string]string{"error": "Signed real-user goods-receipt delegation and current administrator required"})
		return
	}
	var input goodsReceiptMCPRequest
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 131072))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil || decoder.Decode(new(any)) != io.EOF || input.OrderID < 1 || input.LineID < 1 || input.OrderID > math.MaxInt32 || input.LineID > math.MaxInt32 || len(input.SerialNumbers) > 1000 || len(input.Note) > 4000 || input.Quantity < 0 || input.Quantity > 1e9 {
		badRequest(w, "One bounded exact goods-receipt object required (at most 1000 serials)")
		return
	}
	preview := input.Preview || !input.ConfirmReceipt
	result := map[string]any{}
	err := h.db.WithContext(r.Context()).Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec(`SET LOCAL lock_timeout='5s';SET LOCAL statement_timeout='20s';SET LOCAL TIME ZONE 'UTC'`).Error; err != nil {
			return err
		}
		var administrator bool
		if err := tx.Raw("SELECT is_active AND is_admin FROM users WHERE userid=? FOR SHARE", auth.CurrentUser(r).ID).Row().Scan(&administrator); err != nil || !administrator {
			return &receiptFlowError{status: 403, code: "current_administrator_required", message: "Current active goods-receipt administrator required"}
		}
		var receiptRecord *models.IdempotencyRecord
		if !preview {
			legacy, err := legacyGoodsReceiptReplay(tx, r, input)
			if err != nil {
				return err
			}
			if legacy != nil {
				result = map[string]any{"operation_status": "received", "receipt": legacy, "replayed_from_legacy": true}
				return nil
			}
			if len(input.ExpectedContext) != 64 {
				return &receiptFlowError{status: 428, code: "context_required", message: "Copy exact final goods-receipt context"}
			}
			var replay json.RawMessage
			receiptRecord, replay, err = beginIdempotentMutation(tx, r, fmt.Sprintf("mcp_order_receive:%d", input.OrderID), input)
			if err != nil {
				return err
			}
			if replay != nil {
				return json.Unmarshal(replay, &result)
			}
		}
		if err := tx.Exec(`LOCK TABLE proc_suppliers,proc_products,core_product_links,proc_purchase_orders,proc_purchase_order_lines,proc_receipts,proc_amazon_line_confirmations,products,devices,product_locations,storage_zones,warehouse_tasks,warehouse_task_events IN SHARE ROW EXCLUSIVE MODE`).Error; err != nil {
			return err
		}
		p, err := prepareGoodsReceiptOwner(tx, input)
		if err != nil {
			return err
		}
		result = p
		if preview || p["ready_to_execute"] != true {
			return errLifecyclePreview
		}
		if input.ConfirmationText != p["required_confirmation_text"] {
			return &receiptFlowError{status: 428, code: "confirmation_phrase_required", message: "Copy exact context/quantity-bound receipt phrase"}
		}
		native, err := receiptNativeInput(input)
		if err != nil {
			return &receiptFlowError{status: 400, code: "invalid_version", message: "Invalid exact order version"}
		}
		user := auth.CurrentUser(r)
		receipt := models.Receipt{PurchaseOrderID: uint(input.OrderID), PurchaseOrderLineID: uint(input.LineID), Quantity: input.Quantity, ReceivedBy: user.ID, ReceivedByName: user.Username, Note: native.Note, ReceivedAt: time.Now()}
		if err := h.applyGoodsReceipt(tx, r, uint(input.OrderID), native, &receipt); err != nil {
			return err
		}
		if err := auditReceiptWarehouseEffects(tx, r, receipt, p["current"].(map[string]any)); err != nil {
			return err
		}
		result = map[string]any{"operation_status": "received", "receipt": receipt, "effects": p["effects"]}
		return completeIdempotentMutation(tx, receiptRecord, http.StatusOK, result)
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

func receiptSnapshot(tx *gorm.DB, query string, args ...any) ([]map[string]any, error) {
	var raw json.RawMessage
	if err := tx.Raw(`SELECT COALESCE(jsonb_agg(to_jsonb(s) ORDER BY to_jsonb(s)::text),'[]'::jsonb) FROM (`+query+` LIMIT 1001) s`, args...).Row().Scan(&raw); err != nil {
		return nil, err
	}
	var rows []map[string]any
	if err := json.Unmarshal(raw, &rows); err != nil {
		return nil, err
	}
	return rows, nil
}

func prepareGoodsReceiptOwner(tx *gorm.DB, input goodsReceiptMCPRequest) (map[string]any, error) {
	queries := []struct {
		key, query string
		args       []any
	}{
		{"order", `SELECT id,number,status,supplier_id,amazon_punchout_session_id,to_char(updated_at AT TIME ZONE 'UTC','YYYY-MM-DD"T"HH24:MI:SS.US"Z"') AS updated_at FROM proc_purchase_orders WHERE id=?`, []any{input.OrderID}},
		{"lines", `SELECT id,purchase_order_id,product_id,description,quantity,received_quantity,unit FROM proc_purchase_order_lines WHERE purchase_order_id=? ORDER BY id`, []any{input.OrderID}},
		{"supplier", `SELECT s.id,s.name,s.active,s.updated_at FROM proc_suppliers s JOIN proc_purchase_orders o ON o.supplier_id=s.id WHERE o.id=?`, []any{input.OrderID}},
		{"procurement_product", `SELECT p.id,p.sku,p.name,p.active,p.updated_at FROM proc_products p JOIN proc_purchase_order_lines l ON l.product_id=p.id WHERE l.id=? AND l.purchase_order_id=?`, []any{input.LineID, input.OrderID}},
		{"warehouse_link", `SELECT c.id,c.procurement_product_id,c.warehouse_product_id,c.updated_at FROM core_product_links c JOIN proc_purchase_order_lines l ON l.product_id=c.procurement_product_id WHERE l.id=? AND l.purchase_order_id=?`, []any{input.LineID, input.OrderID}},
		{"warehouse_product", `SELECT p.productid,p.name,p.product_code,p.lifecycle_status,p.tracking_mode,p.stock_quantity,p.updated_at,(SELECT count(*) FROM devices d WHERE d.productid=p.productid) AS device_count FROM products p JOIN core_product_links c ON c.warehouse_product_id=p.productid JOIN proc_purchase_order_lines l ON l.product_id=c.procurement_product_id WHERE l.id=? AND l.purchase_order_id=?`, []any{input.LineID, input.OrderID}},
		{"stock_locations", `SELECT pl.location_id AS id,pl.product_id,pl.zone_id,pl.quantity,pl.updated_at FROM product_locations pl JOIN core_product_links c ON c.warehouse_product_id=pl.product_id JOIN proc_purchase_order_lines l ON l.product_id=c.procurement_product_id WHERE l.id=? AND l.purchase_order_id=? ORDER BY pl.location_id`, []any{input.LineID, input.OrderID}},
		{"amazon_confirmations", `SELECT id,purchase_order_id,purchase_order_line_id,accepted_quantity,rejected_quantity,expected_delivery,notice_date,updated_at FROM proc_amazon_line_confirmations WHERE purchase_order_line_id=? AND purchase_order_id=?`, []any{input.LineID, input.OrderID}},
	}
	snapshot := map[string]any{}
	required := []string{}
	for _, q := range queries {
		rows, err := receiptSnapshot(tx, q.query, q.args...)
		if err != nil {
			return nil, err
		}
		snapshot[q.key] = rows
		if len(rows) > 1000 {
			required = append(required, "bounded_receipt_context")
		}
	}
	orders := snapshot["order"].([]map[string]any)
	if len(orders) != 1 {
		return nil, gorm.ErrRecordNotFound
	}
	order := orders[0]
	var line map[string]any
	for _, candidate := range snapshot["lines"].([]map[string]any) {
		if candidate["id"] == float64(input.LineID) {
			line = candidate
			break
		}
	}
	if line == nil {
		return nil, gorm.ErrRecordNotFound
	}
	quantity := func(v any) float64 { value, _ := v.(float64); return value }
	status, _ := order["status"].(string)
	if status != "sent" && status != "confirmed" && status != "partially_confirmed" && status != "partially_received" {
		required = append(required, "receivable_order_status")
	}
	suppliers := snapshot["supplier"].([]map[string]any)
	if len(suppliers) != 1 || suppliers[0]["active"] != true {
		required = append(required, "active_supplier")
	}
	if input.Quantity <= 0 {
		required = append(required, "positive_quantity")
	}
	outstanding := quantity(line["quantity"]) - quantity(line["received_quantity"])
	overdelivery := input.Quantity > outstanding
	if overdelivery && !input.AllowOverdelivery {
		required = append(required, "allow_overdelivery")
	}
	if (!input.Preview && input.ConfirmReceipt && input.ExpectedUpdatedAt == "") || input.ExpectedUpdatedAt != "" && input.ExpectedUpdatedAt != order["updated_at"] {
		required = append(required, "expected_updated_at")
	}
	if order["amazon_punchout_session_id"] != nil {
		accepted := 0.0
		confirmations := snapshot["amazon_confirmations"].([]map[string]any)
		for _, c := range confirmations {
			accepted += quantity(c["accepted_quantity"])
		}
		if len(confirmations) > 0 && quantity(line["received_quantity"])+input.Quantity > accepted+0.000001 {
			required = append(required, "amazon_confirmed_quantity")
		}
	}
	mode := ""
	warehouseID := any(nil)
	if line["product_id"] != nil {
		parents := snapshot["procurement_product"].([]map[string]any)
		if len(parents) != 1 || parents[0]["active"] != true {
			required = append(required, "active_procurement_product")
		}
		products := snapshot["warehouse_product"].([]map[string]any)
		if len(products) != 1 || products[0]["lifecycle_status"] != "active" {
			required = append(required, "active_warehouse_mapping")
		} else {
			mode, _ = products[0]["tracking_mode"].(string)
			warehouseID = products[0]["productid"]
		}
		if mode != "" && mode != "individual" && mode != "quantity" && mode != "none" {
			required = append(required, "supported_tracking_mode")
		}
		if mode == "quantity" && len(products) == 1 && quantity(products[0]["stock_quantity"])+input.Quantity > 9999999.999 {
			required = append(required, "warehouse_stock_capacity")
		}
		if input.Quantity > 999999999.999 || math.Abs(input.Quantity*1000-math.Round(input.Quantity*1000)) > 0.000001 {
			required = append(required, "warehouse_quantity_precision")
		}
	}
	serials := []string{}
	seen := map[string]bool{}
	for _, value := range input.SerialNumbers {
		value = strings.TrimSpace(value)
		if value == "" || len(value) > 255 || seen[strings.ToLower(value)] {
			required = append(required, "unique_valid_serial_numbers")
		}
		seen[strings.ToLower(value)] = true
		serials = append(serials, value)
	}
	if mode == "individual" && (math.Trunc(input.Quantity) != input.Quantity || input.Quantity > 1000 || len(serials) != int(input.Quantity)) {
		required = append(required, "exact_bounded_device_serials")
	}
	if mode != "individual" && len(serials) > 0 {
		required = append(required, "serials_only_for_individual_tracking")
	}
	rawSerials, _ := json.Marshal(serials)
	conflicts, err := receiptSnapshot(tx, `SELECT deviceid,serialnumber,productid,updated_at FROM devices WHERE lower(trim(serialnumber)) IN (SELECT lower(value) FROM jsonb_array_elements_text(?::jsonb))`, string(rawSerials))
	if err != nil {
		return nil, err
	}
	snapshot["serial_conflicts"] = conflicts
	if len(conflicts) > 0 {
		required = append(required, "unused_serial_numbers")
	}
	zones := []map[string]any{}
	if input.TargetZoneID != nil {
		var err error
		zones, err = receiptSnapshot(tx, `SELECT zone_id,code,name,is_active,is_storable,operational_status,updated_at FROM storage_zones WHERE zone_id=?`, *input.TargetZoneID)
		if err != nil {
			return nil, err
		}
		if len(zones) != 1 || zones[0]["is_active"] != true || zones[0]["is_storable"] != true || zones[0]["operational_status"] != "available" {
			required = append(required, "available_storable_target_zone")
		}
		if warehouseID == nil {
			required = append(required, "target_zone_requires_stock_mapping")
		}
	}
	snapshot["target_zone"] = zones
	draft := map[string]any{"order_id": input.OrderID, "line_id": input.LineID, "quantity": input.Quantity, "note": strings.TrimSpace(input.Note), "serial_numbers": serials, "target_zone_id": input.TargetZoneID, "allow_overdelivery": input.AllowOverdelivery}
	raw, err := json.Marshal(map[string]any{"operation": "goods_receipt", "snapshot": snapshot, "draft": draft})
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256(raw)
	fingerprint := hex.EncodeToString(digest[:])
	if input.ExpectedContext != "" && input.ExpectedContext != fingerprint {
		required = append(required, "expected_context")
	}
	verb := "RECEIVE"
	if overdelivery {
		verb = "RECEIVE OVERDELIVERY"
	}
	phrase := fmt.Sprintf("%s ORDER %d LINE %d QUANTITY %g %s", verb, input.OrderID, input.LineID, input.Quantity, fingerprint[:16])
	effects := map[string]any{"overdelivery": overdelivery, "overdelivery_quantity": math.Max(0, input.Quantity-outstanding), "line_received_before": line["received_quantity"], "line_received_after": quantity(line["received_quantity"]) + input.Quantity, "tracking_mode": mode, "warehouse_product_id": warehouseID, "created_device_count": 0, "quantity_stock_delta": 0, "putaway_task_created": warehouseID != nil, "external_messages_sent": false, "prices_changed": false}
	if mode == "individual" {
		effects["created_device_count"] = input.Quantity
	}
	if mode == "quantity" {
		effects["quantity_stock_delta"] = input.Quantity
	}
	operation := "confirmation_required"
	if len(required) > 0 {
		operation = "needs_input"
	}
	return map[string]any{"operation_status": operation, "preview": true, "ready_to_execute": len(required) == 0, "required_fields": required, "draft": draft, "current": snapshot, "effects": effects, "expected_updated_at": order["updated_at"], "expected_context": fingerprint, "required_confirmation_text": phrase}, nil
}

func auditReceiptWarehouseEffects(tx *gorm.DB, r *http.Request, receipt models.Receipt, snapshot map[string]any) error {
	if receipt.WarehouseProductID == nil {
		return nil
	}
	user := auth.CurrentUser(r)
	type auditTarget struct {
		kind, id, query string
		before          any
	}
	products := snapshot["warehouse_product"].([]map[string]any)
	targets := []auditTarget{{"product", fmt.Sprint(*receipt.WarehouseProductID), `SELECT productid,name,product_code,lifecycle_status,tracking_mode,stock_quantity,updated_at,(SELECT count(*) FROM devices d WHERE d.productid=p.productid) AS device_count FROM products p WHERE productid=?`, products[0]}}
	for _, id := range receipt.CreatedDeviceIDs {
		targets = append(targets, auditTarget{"device", id, `SELECT deviceid,productid,serialnumber,status,condition_status,current_location,updated_at FROM devices WHERE deviceid=?`, nil})
	}
	if receipt.PutawayTaskID != nil {
		if err := tx.Exec(`INSERT INTO warehouse_task_events(task_id,event_type,to_status,actor_id) VALUES(?,'mcp_create','open',?)`, *receipt.PutawayTaskID, user.ID).Error; err != nil {
			return err
		}
		targets = append(targets, auditTarget{"warehouse_task", fmt.Sprint(*receipt.PutawayTaskID), `SELECT task_id,task_type,status,priority,to_zone_id,product_id,quantity,is_archived,updated_at FROM warehouse_tasks WHERE task_id=?`, nil})
	}
	for _, target := range targets {
		rows, err := receiptSnapshot(tx, target.query, target.id)
		if err != nil {
			return err
		}
		if len(rows) != 1 {
			return errors.New("goods receipt audit target missing")
		}
		before, err := json.Marshal(target.before)
		if err != nil {
			return err
		}
		after, err := json.Marshal(map[string]any{"origin": "MCP/AI", "after": rows[0], "updated_at": rows[0]["updated_at"], "receipt_id": receipt.ID, "order_id": receipt.PurchaseOrderID, "line_id": receipt.PurchaseOrderLineID})
		if err != nil {
			return err
		}
		if err := tx.Exec(`INSERT INTO audit_log(user_id,action,entity_type,entity_id,old_values,new_values,ip_address,user_agent) VALUES(?,'goods_receipt.create',?,?,?::jsonb,?::jsonb,?,?)`, user.ID, target.kind, target.id, string(before), string(after), requestIP(r), r.UserAgent()).Error; err != nil {
			return err
		}
	}
	return nil
}
