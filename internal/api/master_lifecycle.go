package api

import (
	"crypto/sha256"
	"database/sql"
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

type masterLifecycleRequest struct {
	ID                int64  `json:"id"`
	ExpectedUpdatedAt string `json:"expected_updated_at"`
	ExpectedContext   string `json:"expected_context"`
	ConfirmationText  string `json:"confirmation_text"`
	ConfirmChange     bool   `json:"confirm_change"`
	Preview           bool   `json:"preview"`
}

var masterLifecycleRecords = map[string]struct{ table, kind, record string }{
	"categories": {"proc_categories", "category", `jsonb_build_object('id',id,'name',name,'description',description,'parameterSchema',parameter_schema,'active',active,'createdAt',created_at,'updatedAt',to_char(updated_at AT TIME ZONE 'UTC','YYYY-MM-DD"T"HH24:MI:SS.US"Z"'))`},
	"suppliers":  {"proc_suppliers", "supplier", `jsonb_build_object('id',id,'name',name,'code',code,'website',website,'contactName',contact_name,'email',email,'phone',phone,'paymentTerms',payment_terms,'defaultLeadDays',default_lead_days,'rating',rating,'preferred',preferred,'active',active,'riskLevel',risk_level,'notes',notes,'createdAt',created_at,'updatedAt',to_char(updated_at AT TIME ZONE 'UTC','YYYY-MM-DD"T"HH24:MI:SS.US"Z"'))`},
	"products":   {"proc_products", "product", `jsonb_build_object('id',id,'sku',sku,'name',name,'description',description,'categoryId',category_id,'unit',unit,'manufacturer',manufacturer,'model',model,'parameters',parameters,'attributes',attributes,'active',active,'reorderPoint',reorder_point,'targetStock',target_stock,'createdAt',created_at,'updatedAt',to_char(updated_at AT TIME ZONE 'UTC','YYYY-MM-DD"T"HH24:MI:SS.US"Z"'))`},
	"offers":     {"proc_offers", "offer", `jsonb_build_object('id',id,'productId',product_id,'supplierId',supplier_id,'supplierSku',supplier_sku,'priceCents',price_cents,'currency',currency,'minimumQuantity',minimum_quantity,'packSize',pack_size,'leadDays',lead_days,'purchaseUrl',purchase_url,'validUntil',valid_until,'active',active,'lastCheckedAt',last_checked_at,'createdAt',created_at,'updatedAt',to_char(updated_at AT TIME ZONE 'UTC','YYYY-MM-DD"T"HH24:MI:SS.US"Z"'))`},
}

func lifecycleJSON(tx *gorm.DB, query string, args ...any) (map[string]any, error) {
	var raw json.RawMessage
	if err := tx.Raw(query, args...).Row().Scan(&raw); err != nil {
		return nil, err
	}
	var result map[string]any
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil, err
	}
	return result, nil
}

func (h *Handler) masterLifecycle(w http.ResponseWriter, r *http.Request) {
	entity, operation := chi.URLParam(r, "entity"), chi.URLParam(r, "operation")
	spec, supported := masterLifecycleRecords[entity]
	if !supported || operation != "archive" && operation != "restore" {
		notFound(w)
		return
	}
	user := auth.CurrentUser(r)
	if user.ID == 0 || !user.IsAdmin || !isMCPMutation(r) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "Real procurement administrator and MCP origin required"})
		return
	}
	var claims struct {
		UID   uint   `json:"uid"`
		Scope string `json:"mcp_scope"`
		jwt.RegisteredClaims
	}
	rawToken := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if cookie, err := r.Cookie("cores_token"); err == nil {
		rawToken = cookie.Value
	}
	tok, err := jwt.ParseWithClaims(rawToken, &claims, func(*jwt.Token) (any, error) { return commonjwt.JWTSecret(), nil }, jwt.WithValidMethods([]string{"HS256"}), jwt.WithExpirationRequired())
	if err != nil || !tok.Valid || claims.UID != user.ID || claims.Scope != "cores:procurement:archive" {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "Signed real-user procurement archive delegation required"})
		return
	}
	var input masterLifecycleRequest
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil || decoder.Decode(new(any)) != io.EOF || decoder.InputOffset() > 8192 || input.ID < 1 || input.ID > math.MaxInt32 {
		badRequest(w, "One bounded lifecycle object and exact positive ID required")
		return
	}
	preview := input.Preview || !input.ConfirmChange
	result := map[string]any{}
	err = h.db.WithContext(r.Context()).Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec(`SET LOCAL lock_timeout='5s';SET LOCAL statement_timeout='20s';SET LOCAL TIME ZONE 'UTC'`).Error; err != nil {
			return err
		}
		var administrator bool
		if err := tx.Raw(`SELECT is_admin AND is_active FROM users WHERE userid=? FOR SHARE`, user.ID).Row().Scan(&administrator); err != nil || !administrator {
			return &receiptFlowError{status: http.StatusForbidden, code: "current_administrator_required", message: "Current active procurement administrator required"}
		}
		var receipt *models.IdempotencyRecord
		if !preview {
			if len(input.ExpectedContext) != 64 {
				return &receiptFlowError{status: http.StatusPreconditionRequired, code: "context_required", message: "Copy the exact final lifecycle context"}
			}
			var replay json.RawMessage
			var err error
			receipt, replay, err = beginIdempotentMutation(tx, r, fmt.Sprintf("%s_%s:%d", spec.kind, operation, input.ID), input)
			if err != nil {
				return err
			}
			if replay != nil {
				return json.Unmarshal(replay, &result)
			}
		}
		if err := tx.Exec(`LOCK TABLE proc_suppliers,proc_products,proc_offers,proc_categories,core_product_links,proc_purchase_orders,proc_purchase_order_lines,proc_requisitions,proc_requisition_lines IN SHARE ROW EXCLUSIVE MODE`).Error; err != nil {
			return err
		}
		current, err := lifecycleJSON(tx, "SELECT "+spec.record+" FROM "+spec.table+" WHERE id=?", input.ID)
		if errors.Is(err, sql.ErrNoRows) {
			return gorm.ErrRecordNotFound
		}
		if err != nil {
			return err
		}
		required := []string{}
		restore := operation == "restore"
		if current["active"] == restore {
			required = append(required, "lifecycle_state")
		}
		validationIssues := []string{}
		if restore {
			raw, err := json.Marshal(current)
			if err != nil {
				return err
			}
			var message string
			switch entity {
			case "categories":
				var original models.Category
				if err := json.Unmarshal(raw, &original); err != nil {
					return err
				}
				message = validateCategory(&original)
			case "suppliers":
				var original models.Supplier
				if err := json.Unmarshal(raw, &original); err != nil {
					return err
				}
				message = validateSupplier(&original)
			case "products":
				var original models.Product
				if err := json.Unmarshal(raw, &original); err != nil {
					return err
				}
				message = validateProduct(&original)
			case "offers":
				var original models.Offer
				if err := json.Unmarshal(raw, &original); err != nil {
					return err
				}
				message = validateOffer(&original)
			}
			if message != "" {
				required = append(required, "valid_retained_business_fields")
				validationIssues = append(validationIssues, message)
			}
		}
		if !preview && input.ExpectedUpdatedAt == "" || input.ExpectedUpdatedAt != "" && input.ExpectedUpdatedAt != current["updatedAt"] {
			required = append(required, "expected_updated_at")
		}
		dependencies := map[string]any{}
		queries := []struct{ key, query string }{}
		switch entity {
		case "categories":
			queries = append(queries, struct{ key, query string }{"products", `SELECT id AS product_id,sku,name,active,updated_at FROM proc_products WHERE category_id=?`})
		case "suppliers":
			queries = append(queries,
				struct{ key, query string }{"open_orders", `SELECT id AS order_id,number,status,updated_at FROM proc_purchase_orders WHERE supplier_id=? AND status NOT IN ('cancelled','received') AND NOT is_archived`},
				struct{ key, query string }{"offers", `SELECT id AS offer_id,product_id,active,updated_at FROM proc_offers WHERE supplier_id=?`})
		case "products":
			queries = append(queries,
				struct{ key, query string }{"open_orders", `SELECT DISTINCT po.id AS order_id,po.number,po.status,po.updated_at FROM proc_purchase_order_lines l JOIN proc_purchase_orders po ON po.id=l.purchase_order_id WHERE l.product_id=? AND po.status NOT IN ('cancelled','received') AND NOT po.is_archived`},
				struct{ key, query string }{"open_requisitions", `SELECT DISTINCT rq.id AS requisition_id,rq.number,rq.status,rq.updated_at FROM proc_requisition_lines l JOIN proc_requisitions rq ON rq.id=l.requisition_id WHERE l.product_id=? AND rq.status IN ('draft','submitted','approved') AND NOT rq.is_archived`},
				struct{ key, query string }{"offers", `SELECT id AS offer_id,supplier_id,active,updated_at FROM proc_offers WHERE product_id=?`},
				struct{ key, query string }{"warehouse_links", `SELECT id AS link_id,warehouse_product_id,updated_at FROM core_product_links WHERE procurement_product_id=?`},
				struct{ key, query string }{"category", `SELECT c.id AS category_id,c.name,c.updated_at,COALESCE((to_jsonb(c)->>'active')::boolean,true) AS active FROM proc_categories c JOIN proc_products p ON p.category_id=c.id WHERE p.id=?`})
		case "offers":
			queries = append(queries,
				struct{ key, query string }{"product", `SELECT p.id AS product_id,p.sku,p.name,p.active,p.updated_at FROM proc_products p JOIN proc_offers o ON o.product_id=p.id WHERE o.id=?`},
				struct{ key, query string }{"supplier", `SELECT s.id AS supplier_id,s.name,s.active,s.updated_at FROM proc_suppliers s JOIN proc_offers o ON o.supplier_id=s.id WHERE o.id=?`})
		}
		for _, item := range queries {
			var raw json.RawMessage
			query := `SELECT COALESCE(jsonb_agg(to_jsonb(d) ORDER BY to_jsonb(d)::text),'[]'::jsonb) FROM (` + item.query + ` LIMIT 1001) d`
			if err := tx.Raw(query, input.ID).Row().Scan(&raw); err != nil {
				return err
			}
			var rows []map[string]any
			if err := json.Unmarshal(raw, &rows); err != nil {
				return err
			}
			dependencies[item.key] = rows
			if len(rows) > 1000 {
				required = append(required, "bounded_lifecycle_context")
			}
			if !restore && entity == "categories" && item.key == "products" {
				for _, row := range rows {
					if row["active"] == true {
						required = append(required, "active_products")
						break
					}
				}
			}
			if !restore && strings.HasPrefix(item.key, "open_") && len(rows) > 0 {
				required = append(required, "active_"+item.key)
			}
			if restore && (entity == "offers" || item.key == "category") {
				if item.key == "category" && current["categoryId"] != nil && len(rows) != 1 {
					required = append(required, "existing_category")
				}
				if entity == "offers" && len(rows) != 1 {
					required = append(required, "existing_"+item.key)
				}
				for _, row := range rows {
					if row["active"] != true {
						required = append(required, "active_"+item.key)
					}
				}
			}
		}
		draft := map[string]any{}
		for key, value := range current {
			draft[key] = value
		}
		draft["active"] = restore
		encoded, err := json.Marshal(map[string]any{"entity": entity, "operation": operation, "current": current, "draft": draft, "dependencies": dependencies})
		if err != nil {
			return err
		}
		digest := sha256.Sum256(encoded)
		fingerprint := hex.EncodeToString(digest[:])
		if input.ExpectedContext != "" && input.ExpectedContext != fingerprint {
			required = append(required, "expected_context")
		}
		phrase := fmt.Sprintf("%s PROCUREMENT %s %d %s", strings.ToUpper(operation), strings.ToUpper(spec.kind), input.ID, fingerprint[:16])
		if !preview && len(required) == 0 && input.ConfirmationText != phrase {
			return &receiptFlowError{status: http.StatusPreconditionRequired, code: "confirmation_phrase_required", message: "Copy the exact record/context-bound lifecycle phrase"}
		}
		result = map[string]any{"operation_status": "confirmation_required", "preview": true, "ready_to_execute": len(required) == 0, "required_fields": required, "validation_issues": validationIssues, "current": current, "draft": draft, "dependencies": dependencies, "expected_updated_at": current["updatedAt"], "expected_context": fingerprint, "required_confirmation_text": phrase, "diff": map[string]any{"active": map[string]any{"before": current["active"], "after": restore}}, "effects": map[string]any{"identity_and_fields_retained": true, "stock_movements": false, "price_changes": false, "external_messages_sent": false}}
		if len(required) > 0 {
			result["operation_status"] = "needs_input"
		}
		if preview || len(required) > 0 {
			// A reviewed conflict is not a durable successful operation.
			return errLifecyclePreview
		}
		if err := tx.Exec("UPDATE "+spec.table+" SET active=? WHERE id=?", restore, input.ID).Error; err != nil {
			return err
		}
		after, err := lifecycleJSON(tx, "SELECT "+spec.record+" FROM "+spec.table+" WHERE id=?", input.ID)
		if err != nil {
			return err
		}
		beforeJSON, _ := json.Marshal(current)
		afterJSON, _ := json.Marshal(map[string]any{"origin": "MCP/AI", "before": current, "after": after, "updated_at": after["updatedAt"]})
		if err := tx.Exec(`INSERT INTO audit_log(user_id,action,entity_type,entity_id,old_values,new_values,ip_address,user_agent) VALUES(?,? ,?,? ,?::jsonb,?::jsonb,?,?)`, user.ID, spec.kind+"."+operation, "procurement_"+spec.kind, fmt.Sprint(input.ID), string(beforeJSON), string(afterJSON), requestIP(r), r.UserAgent()).Error; err != nil {
			return err
		}
		if err := tx.Create(&models.Activity{EntityType: spec.kind, EntityID: uint(input.ID), Action: operation, UserID: user.ID, Username: user.Username, Details: "origin=MCP/AI lifecycle; original fields retained"}).Error; err != nil {
			return err
		}
		result = map[string]any{"operation_status": map[bool]string{false: "archived", true: "restored"}[restore], "record": after, "diff": result["diff"], "effects": result["effects"]}
		return completeIdempotentMutation(tx, receipt, http.StatusOK, result)
	})
	if errors.Is(err, errLifecyclePreview) {
		writeJSON(w, http.StatusOK, result)
		return
	}
	if err != nil {
		writeProcurementFlowError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

var errLifecyclePreview = errors.New("read-only lifecycle preview or reviewed conflict")
