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

	"procurementcore/internal/auth"
	"procurementcore/internal/models"

	"github.com/go-chi/chi/v5"
	"github.com/golang-jwt/jwt/v5"
	commonjwt "github.com/nbt4/cores-common/pkg/jwt"
	"gorm.io/gorm"
)

// A signed MCP token remains delegated even when a caller omits the optional
// origin header; it cannot use native UI routes to evade guided action scopes.
func hasSignedMCPDelegation(r *http.Request) bool {
	raw := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if cookie, err := r.Cookie("cores_token"); err == nil {
		raw = cookie.Value
	}
	if raw == "" {
		return false
	}
	var claims struct {
		UID   uint   `json:"uid"`
		Scope string `json:"mcp_scope"`
		jwt.RegisteredClaims
	}
	token, err := jwt.ParseWithClaims(raw, &claims, func(*jwt.Token) (any, error) { return commonjwt.JWTSecret(), nil }, jwt.WithValidMethods([]string{"HS256"}), jwt.WithExpirationRequired())
	return err == nil && token.Valid && claims.UID == auth.CurrentUser(r).ID && strings.TrimSpace(claims.Scope) != ""
}

func signedProcurementUserDelegation(r *http.Request, scope string) bool {
	user := auth.CurrentUser(r)
	if user.ID == 0 || !isMCPMutation(r) {
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

func (h *Handler) workflowLifecycleMCP(w http.ResponseWriter, r *http.Request) {
	entity, operation := chi.URLParam(r, "entity"), chi.URLParam(r, "operation")
	if (entity != "requisitions" && entity != "orders") || (operation != "archive" && operation != "restore") {
		notFound(w)
		return
	}
	if !signedProcurementUserDelegation(r, "cores:procurement:archive") {
		writeJSON(w, 403, map[string]string{"error": "Signed real-user archive delegation required"})
		return
	}
	var input masterLifecycleRequest
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil || decoder.Decode(new(any)) != io.EOF || input.ID < 1 || input.ID > math.MaxInt32 {
		badRequest(w, "One bounded exact workflow lifecycle object required")
		return
	}
	preview := input.Preview || !input.ConfirmChange
	result := map[string]any{}
	err := h.db.WithContext(r.Context()).Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec(`SET LOCAL lock_timeout='5s';SET LOCAL statement_timeout='20s';SET LOCAL TIME ZONE 'UTC'`).Error; err != nil {
			return err
		}
		user := auth.CurrentUser(r)
		var role struct{ Active, Admin bool }
		if err := tx.Raw("SELECT is_active AS active,is_admin AS admin FROM users WHERE userid=? FOR SHARE", user.ID).Scan(&role).Error; err != nil || !role.Active {
			return &receiptFlowError{status: 403, code: "current_user_required", message: "Current active workflow user required"}
		}
		if entity == "orders" && !role.Admin {
			return &receiptFlowError{status: 403, code: "current_administrator_required", message: "Current order administrator required"}
		}
		if entity == "requisitions" && !role.Admin {
			var requester uint
			if err := tx.Raw("SELECT requester_id FROM proc_requisitions WHERE id=?", input.ID).Row().Scan(&requester); err != nil {
				return gorm.ErrRecordNotFound
			}
			if requester != user.ID {
				return &receiptFlowError{status: 403, code: "requester_required", message: "Only the original requester or an administrator may archive/restore a requisition"}
			}
		}
		kind := map[string]string{"orders": "order", "requisitions": "requisition"}[entity]
		var receipt *models.IdempotencyRecord
		if !preview {
			if len(input.ExpectedContext) != 64 {
				return &receiptFlowError{status: 428, code: "context_required", message: "Copy exact final workflow context"}
			}
			var replay json.RawMessage
			var err error
			receipt, replay, err = beginIdempotentMutation(tx, r, fmt.Sprintf("%s_%s:%d", kind, operation, input.ID), input)
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
		// Warehouse task updates must not race an order archive preview.
		var taskTable bool
		if err := tx.Raw("SELECT to_regclass('warehouse_tasks') IS NOT NULL").Scan(&taskTable).Error; err != nil {
			return err
		}
		if taskTable {
			if err := tx.Exec("LOCK TABLE warehouse_tasks IN SHARE ROW EXCLUSIVE MODE").Error; err != nil {
				return err
			}
		}
		p, err := prepareProcurementWorkflowLifecycle(tx, entity, operation, input)
		if err != nil {
			return err
		}
		result = p
		if preview || p["ready_to_execute"] != true {
			return errLifecyclePreview
		}
		if input.ConfirmationText != p["required_confirmation_text"] {
			return &receiptFlowError{status: 428, code: "confirmation_phrase_required", message: "Copy exact record/context-bound workflow phrase"}
		}
		table := map[string]string{"orders": "proc_purchase_orders", "requisitions": "proc_requisitions"}[entity]
		if err := tx.Exec("UPDATE "+table+" SET is_archived=? WHERE id=?", operation == "archive", input.ID).Error; err != nil {
			return err
		}
		after, err := procurementWorkflowRecord(tx, entity, input.ID)
		if err != nil {
			return err
		}
		beforeJSON, err := json.Marshal(p["current"])
		if err != nil {
			return err
		}
		afterJSON, err := json.Marshal(map[string]any{"origin": "MCP/AI", "after": after, "updated_at": after["updatedAt"]})
		if err != nil {
			return err
		}
		entityKind := map[string]string{"orders": "procurement_order", "requisitions": "procurement_requisition"}[entity]
		if err := tx.Exec(`INSERT INTO audit_log(user_id,action,entity_type,entity_id,old_values,new_values,ip_address,user_agent) VALUES(?,?,?,?,?::jsonb,?::jsonb,?,?)`, user.ID, kind+"."+operation, entityKind, fmt.Sprint(input.ID), string(beforeJSON), string(afterJSON), requestIP(r), r.UserAgent()).Error; err != nil {
			return err
		}
		if err := tx.Create(&models.Activity{EntityType: map[string]string{"orders": "purchase_order", "requisitions": "requisition"}[entity], EntityID: uint(input.ID), Action: operation, UserID: user.ID, Username: user.Username, Details: "origin=MCP/AI retained workflow lifecycle"}).Error; err != nil {
			return err
		}
		status := "archived"
		if operation == "restore" {
			status = "restored"
		}
		result = map[string]any{"operation_status": status, "record": after, "effects": p["effects"]}
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

func procurementWorkflowRecord(tx *gorm.DB, entity string, id int64) (map[string]any, error) {
	var record any
	if entity == "orders" {
		var row models.PurchaseOrder
		if err := tx.Preload("Lines", func(db *gorm.DB) *gorm.DB { return db.Order("id").Limit(1001) }).First(&row, id).Error; err != nil {
			return nil, err
		}
		record = row
	} else {
		var row models.Requisition
		if err := tx.Preload("Lines", func(db *gorm.DB) *gorm.DB { return db.Order("id").Limit(1001) }).First(&row, id).Error; err != nil {
			return nil, err
		}
		record = row
	}
	raw, err := json.Marshal(record)
	if err != nil {
		return nil, err
	}
	if len(raw) > 512<<10 {
		return nil, &receiptFlowError{status: 413, code: "bounded_workflow_record", message: "Complete workflow record exceeds the bounded MCP preview size"}
	}
	out := map[string]any{}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	table := map[string]string{"orders": "proc_purchase_orders", "requisitions": "proc_requisitions"}[entity]
	var version string
	if err := tx.Raw("SELECT to_char(updated_at AT TIME ZONE 'UTC','YYYY-MM-DD\"T\"HH24:MI:SS.US\"Z\"') FROM "+table+" WHERE id=?", id).Row().Scan(&version); err != nil {
		return nil, err
	}
	out["updatedAt"] = version
	if _, ok := out["isArchived"]; !ok {
		out["isArchived"] = false
	}
	return out, nil
}

func prepareProcurementWorkflowLifecycle(tx *gorm.DB, entity, operation string, input masterLifecycleRequest) (map[string]any, error) {
	current, err := procurementWorkflowRecord(tx, entity, input.ID)
	if err != nil {
		return nil, err
	}
	required := []string{}
	if lines, ok := current["lines"].([]any); ok && len(lines) > 1000 {
		required = append(required, "bounded_workflow_lines")
	}
	restore := operation == "restore"
	if current["isArchived"] != restore {
		required = append(required, "lifecycle_state")
	}
	status, _ := current["status"].(string)
	dependencies := map[string]any{}
	query := func(key, q string, args ...any) error {
		rows, err := receiptSnapshot(tx, q, args...)
		if err != nil {
			return err
		}
		dependencies[key] = rows
		if len(rows) > 1000 {
			required = append(required, "bounded_workflow_context")
		}
		return nil
	}
	if entity == "orders" {
		if err := query("supplier", "SELECT id,name,active,updated_at FROM proc_suppliers WHERE id=?", current["supplierId"]); err != nil {
			return nil, err
		}
		if err := query("requisition", "SELECT id,number,status,is_archived,updated_at FROM proc_requisitions WHERE id=?", current["requisitionId"]); err != nil {
			return nil, err
		}
		if err := query("products", `SELECT p.id,p.sku,p.name,p.active,p.updated_at FROM proc_products p JOIN proc_purchase_order_lines l ON l.product_id=p.id WHERE l.purchase_order_id=? ORDER BY p.id`, input.ID); err != nil {
			return nil, err
		}
		if err := query("receipts", `SELECT id,purchase_order_line_id,quantity,warehouse_product_id,putaway_task_id,received_at FROM proc_receipts WHERE purchase_order_id=? ORDER BY id`, input.ID); err != nil {
			return nil, err
		}
		var tasksExist bool
		if err := tx.Raw("SELECT to_regclass('warehouse_tasks') IS NOT NULL").Scan(&tasksExist).Error; err != nil {
			return nil, err
		}
		if tasksExist {
			if err := query("putaway_tasks", `SELECT t.task_id,t.task_type,t.status,t.is_archived,t.updated_at FROM warehouse_tasks t JOIN proc_receipts r ON r.putaway_task_id=t.task_id WHERE r.purchase_order_id=? ORDER BY t.task_id`, input.ID); err != nil {
				return nil, err
			}
		}
		if !restore {
			if status != "draft" && status != "received" && status != "cancelled" {
				required = append(required, "resolved_order_status")
			}
			if tasks, ok := dependencies["putaway_tasks"].([]map[string]any); ok {
				for _, task := range tasks {
					if task["status"] != "done" && task["status"] != "cancelled" && task["is_archived"] != true {
						required = append(required, "completed_putaway_tasks")
						break
					}
				}
			}
		} else {
			suppliers := dependencies["supplier"].([]map[string]any)
			if len(suppliers) != 1 || suppliers[0]["active"] != true {
				required = append(required, "active_supplier")
			}
			if current["requisitionId"] != nil {
				parents := dependencies["requisition"].([]map[string]any)
				if len(parents) != 1 || parents[0]["is_archived"] == true {
					required = append(required, "active_original_requisition")
				}
			}
		}
	} else {
		if err := query("orders", `SELECT id,number,status,is_archived,updated_at FROM proc_purchase_orders WHERE requisition_id=? ORDER BY id`, input.ID); err != nil {
			return nil, err
		}
		if err := query("products", `SELECT p.id,p.sku,p.name,p.active,p.updated_at FROM proc_products p JOIN proc_requisition_lines l ON l.product_id=p.id WHERE l.requisition_id=? ORDER BY p.id`, input.ID); err != nil {
			return nil, err
		}
		if err := query("suppliers", `SELECT s.id,s.name,s.active,s.updated_at FROM proc_suppliers s JOIN proc_requisition_lines l ON l.preferred_supplier_id=s.id WHERE l.requisition_id=? ORDER BY s.id`, input.ID); err != nil {
			return nil, err
		}
		if !restore {
			if status != "draft" && status != "returned" && status != "rejected" && status != "ordered" {
				required = append(required, "resolved_requisition_status")
			}
			for _, order := range dependencies["orders"].([]map[string]any) {
				if order["is_archived"] != true && order["status"] != "received" && order["status"] != "cancelled" {
					required = append(required, "resolved_related_orders")
					break
				}
			}
		} else {
			for _, supplier := range dependencies["suppliers"].([]map[string]any) {
				if supplier["active"] != true {
					required = append(required, "active_suppliers")
					break
				}
			}
		}
	}
	if restore {
		original, _ := json.Marshal(current)
		message := ""
		if entity == "orders" {
			var order models.PurchaseOrder
			if err := json.Unmarshal(original, &order); err != nil {
				return nil, err
			}
			order.Status = "draft" // Validate retained fields without changing the original lifecycle status.
			message = validateOrder(&order)
		} else {
			var requisition models.Requisition
			if err := json.Unmarshal(original, &requisition); err != nil {
				return nil, err
			}
			message = validateRequisition(&requisition)
		}
		if message != "" {
			required = append(required, "valid_retained_fields")
		}
		for _, product := range dependencies["products"].([]map[string]any) {
			if product["active"] != true {
				required = append(required, "active_products")
				break
			}
		}
	}
	if restore && entity == "requisitions" {
		parents := map[float64]bool{}
		for _, supplier := range dependencies["suppliers"].([]map[string]any) {
			id, _ := supplier["id"].(float64)
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
	}
	// Missing retained references are failures too; compare raw line IDs, not just joins.
	if restore {
		lines, _ := current["lines"].([]any)
		products := map[float64]bool{}
		for _, product := range dependencies["products"].([]map[string]any) {
			id, _ := product["id"].(float64)
			products[id] = true
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
	raw, err := json.Marshal(map[string]any{"operation": operation, "entity": entity, "record": current, "dependencies": dependencies})
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256(raw)
	fingerprint := hex.EncodeToString(digest[:])
	if input.ExpectedContext != "" && input.ExpectedContext != fingerprint {
		required = append(required, "expected_context")
	}
	kind := map[string]string{"orders": "ORDER", "requisitions": "REQUISITION"}[entity]
	phrase := fmt.Sprintf("%s PROCUREMENT %s %d %s", strings.ToUpper(operation), kind, input.ID, fingerprint[:16])
	state := "confirmation_required"
	if len(required) > 0 {
		state = "needs_input"
	}
	return map[string]any{"operation_status": state, "preview": true, "ready_to_execute": len(required) == 0, "current": current, "dependencies": dependencies, "expected_updated_at": current["updatedAt"], "expected_context": fingerprint, "required_confirmation_text": phrase, "required_fields": required, "effects": map[string]any{"history_preserved": true, "original_status": status, "lines_preserved": true, "stock_movements": false, "external_messages": false, "prices_changed": false}}, nil
}
