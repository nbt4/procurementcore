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

// Preserve the exact native legacy request encodings used by durable receipts.
type requisitionUpdateInput struct {
	models.Requisition
	ExpectedUpdatedAt *time.Time `json:"expectedUpdatedAt"`
}
type requisitionSubmitInput struct {
	ExpectedUpdatedAt *time.Time `json:"expectedUpdatedAt"`
}
type requisitionDraftLine struct {
	LineID              uint    `json:"line_id,omitempty"`
	ProductID           *uint   `json:"product_id,omitempty"`
	Description         string  `json:"description,omitempty"`
	Quantity            float64 `json:"quantity,omitempty"`
	Unit                string  `json:"unit,omitempty"`
	EstimatedPriceCents int64   `json:"estimated_price_cents,omitempty"`
	PreferredSupplierID *uint   `json:"preferred_supplier_id,omitempty"`
	PurchaseURL         string  `json:"purchase_url,omitempty"`
}
type requisitionDraftRequest struct {
	ID                int64                   `json:"id,omitempty"`
	Title             *string                 `json:"title,omitempty"`
	CostCenter        *string                 `json:"cost_center,omitempty"`
	Justification     *string                 `json:"justification,omitempty"`
	NeededBy          *string                 `json:"needed_by,omitempty"`
	Lines             *[]requisitionDraftLine `json:"lines,omitempty"`
	AllowDuplicate    bool                    `json:"allow_duplicate,omitempty"`
	ExpectedUpdatedAt string                  `json:"expected_updated_at,omitempty"`
	ExpectedContext   string                  `json:"expected_context,omitempty"`
	ConfirmationText  string                  `json:"confirmation_text,omitempty"`
	ConfirmChange     bool                    `json:"confirm_change,omitempty"`
	Preview           bool                    `json:"preview,omitempty"`
}

func currentRequisitionDraftRights(tx *gorm.DB, r *http.Request, id int64) error {
	user := auth.CurrentUser(r)
	var role struct{ Active, Admin bool }
	if err := tx.Raw("SELECT is_active AS active,is_admin AS admin FROM users WHERE userid=? FOR SHARE", user.ID).Scan(&role).Error; err != nil {
		return err
	}
	if !role.Active {
		return &receiptFlowError{status: 403, code: "current_user_required", message: "Current active requester required"}
	}
	if id != 0 && !role.Admin {
		var requester uint
		if err := tx.Raw("SELECT requester_id FROM proc_requisitions WHERE id=?", id).Row().Scan(&requester); err != nil {
			return gorm.ErrRecordNotFound
		}
		if requester != user.ID {
			return &receiptFlowError{status: 403, code: "requester_required", message: "Only the original requester or a current administrator may edit or submit"}
		}
	}
	return nil
}

func (h *Handler) requisitionDraftMCP(w http.ResponseWriter, r *http.Request) {
	op := chi.URLParam(r, "operation")
	if op != "create" && op != "update" && op != "submit" {
		notFound(w)
		return
	}
	if !signedProcurementUserDelegation(r, "cores:procurement:"+op) {
		writeJSON(w, 403, map[string]string{"error": "Signed real-user requisition action required"})
		return
	}
	var input requisitionDraftRequest
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 512<<10))
	d.DisallowUnknownFields()
	if err := d.Decode(&input); err != nil || d.Decode(new(any)) != io.EOF || input.ID < 0 || input.ID > math.MaxInt32 || (op == "create" && input.ID != 0) || (op != "create" && input.ID == 0) || (op != "create" && input.AllowDuplicate) || (op == "submit" && (input.Title != nil || input.CostCenter != nil || input.Justification != nil || input.NeededBy != nil || input.Lines != nil)) || (input.Lines != nil && len(*input.Lines) > 100) || (input.Justification != nil && len(*input.Justification) > 4000) {
		badRequest(w, "One bounded exact requisition action required")
		return
	}
	preview := input.Preview || !input.ConfirmChange
	result := map[string]any{}
	err := h.db.WithContext(r.Context()).Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec(`SET LOCAL lock_timeout='5s';SET LOCAL statement_timeout='20s';SET LOCAL TIME ZONE 'UTC'`).Error; err != nil {
			return err
		}
		if err := currentRequisitionDraftRights(tx, r, input.ID); err != nil {
			return err
		}
		var receipt *models.IdempotencyRecord
		if !preview {
			legacy, err := legacyRequisitionDraftReplay(tx, r, op, input)
			if err != nil {
				return err
			}
			if legacy != nil {
				result = legacy
				return nil
			}
			if len(input.ExpectedContext) != 64 {
				return &receiptFlowError{status: 428, code: "context_required", message: "Copy the complete final draft context"}
			}
			var replay json.RawMessage
			receipt, replay, err = beginIdempotentMutation(tx, r, fmt.Sprintf("mcp_requisition_%s:%d", op, input.ID), input)
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
		p, draft, err := prepareRequisitionOwnerDraft(tx, r, op, input)
		if err != nil {
			return err
		}
		result = p
		if preview || p["ready_to_execute"] != true {
			return errLifecyclePreview
		}
		if input.ConfirmationText != p["required_confirmation_text"] {
			return &receiptFlowError{status: 428, code: "confirmation_phrase_required", message: "Copy the complete draft/context-bound phrase"}
		}
		if op == "create" {
			draft.Number = nextNumber("BAN")
			if err := tx.Create(&draft).Error; err != nil {
				return err
			}
		} else {
			updates := map[string]any{"title": draft.Title, "cost_center": draft.CostCenter, "justification": draft.Justification, "needed_by": draft.NeededBy, "estimated_total_cents": draft.EstimatedTotalCents, "status": draft.Status}
			if op == "submit" {
				updates["submitted_at"] = time.Now()
				updates["approved_by"], updates["approved_by_name"], updates["decision_note"], updates["decided_at"] = nil, "", "", nil
			}
			if err := tx.Model(&models.Requisition{}).Where("id=?", input.ID).Updates(updates).Error; err != nil {
				return err
			}
			// Omitted lines retain every original identity and opaque native field.
			// An explicit replacement may retain selected existing line IDs.
			if op == "update" && input.Lines != nil {
				keep := []uint{}
				for _, line := range draft.Lines {
					if line.ID != 0 {
						keep = append(keep, line.ID)
					}
				}
				q := tx.Where("requisition_id=?", input.ID)
				if len(keep) > 0 {
					q = q.Where("id NOT IN ?", keep)
				}
				if err := q.Delete(&models.RequisitionLine{}).Error; err != nil {
					return err
				}
				for _, line := range draft.Lines {
					if line.ID == 0 {
						if err := tx.Create(&line).Error; err != nil {
							return err
						}
					} else if err := tx.Model(&models.RequisitionLine{}).Where("id=? AND requisition_id=?", line.ID, input.ID).Updates(map[string]any{"product_id": line.ProductID, "description": line.Description, "quantity": line.Quantity, "unit": line.Unit, "estimated_price_cents": line.EstimatedPriceCents, "preferred_supplier_id": line.PreferredSupplierID, "purchase_url": line.PurchaseURL}).Error; err != nil {
						return err
					}
				}
			}
		}
		var after models.Requisition
		if err := tx.Preload("Lines", func(db *gorm.DB) *gorm.DB { return db.Order("id") }).First(&after, draft.ID).Error; err != nil {
			return err
		}
		status := map[string]string{"create": "created", "update": "updated", "submit": "submitted"}[op]
		if err := auditRequisitionMutation(tx, r, status, p["current"], after); err != nil {
			return err
		}
		result = map[string]any{"operation_status": status, "requisition": after, "diff": p["diff"], "effects": p["effects"]}
		if op == "create" {
			result["creation_status"] = "created"
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

func applyRequisitionDraft(input requisitionDraftRequest, row *models.Requisition, legacy bool) error {
	for _, field := range []struct {
		dst *string
		src *string
	}{{&row.Title, input.Title}, {&row.CostCenter, input.CostCenter}, {&row.Justification, input.Justification}} {
		if field.src != nil {
			*field.dst = strings.TrimSpace(*field.src)
		}
	}
	if input.NeededBy != nil {
		row.NeededBy = nil
		if date := strings.TrimSpace(*input.NeededBy); date != "" {
			t, err := time.Parse(time.RFC3339Nano, date)
			if err != nil {
				return &receiptFlowError{status: 400, code: "invalid_needed_by", message: "RFC3339 need date required"}
			}
			t = t.UTC()
			row.NeededBy = &t
		}
	}
	if input.Lines != nil {
		old := map[uint]models.RequisitionLine{}
		for _, l := range row.Lines {
			old[l.ID] = l
		}
		seen := map[uint]bool{}
		lines := make([]models.RequisitionLine, 0, len(*input.Lines))
		for _, l := range *input.Lines {
			if l.LineID > math.MaxInt32 || l.ProductID != nil && (*l.ProductID == 0 || *l.ProductID > math.MaxInt32) || l.PreferredSupplierID != nil && (*l.PreferredSupplierID == 0 || *l.PreferredSupplierID > math.MaxInt32) {
				return &receiptFlowError{status: 400, code: "invalid_line_identity", message: "Existing positive bounded reference IDs required"}
			}
			line := models.RequisitionLine{}
			if l.LineID != 0 {
				retained, ok := old[l.LineID]
				if legacy || !ok || seen[l.LineID] {
					return &receiptFlowError{status: 409, code: "invalid_line_identity", message: "Unique line IDs must belong to this original draft"}
				}
				line = retained
				seen[l.LineID] = true
			}
			line.RequisitionID = row.ID
			line.ProductID = l.ProductID
			line.Product = nil
			line.Description = l.Description
			line.Quantity = l.Quantity
			line.Unit = l.Unit
			line.EstimatedPriceCents = l.EstimatedPriceCents
			line.PreferredSupplierID = l.PreferredSupplierID
			line.PurchaseURL = l.PurchaseURL
			lines = append(lines, line)
		}
		row.Lines = lines
	}
	return nil
}

func prepareRequisitionOwnerDraft(tx *gorm.DB, r *http.Request, op string, input requisitionDraftRequest) (map[string]any, models.Requisition, error) {
	var current map[string]any
	draft := models.Requisition{Status: "draft", RequesterID: auth.CurrentUser(r).ID, RequesterName: auth.CurrentUser(r).Username}
	required := []string{}
	if op != "create" {
		var err error
		current, err = procurementWorkflowRecord(tx, "requisitions", input.ID)
		if err != nil {
			return nil, draft, err
		}
		raw, _ := json.Marshal(current)
		if err = json.Unmarshal(raw, &draft); err != nil {
			return nil, draft, err
		}
		if draft.IsArchived {
			required = append(required, "active_requisition")
		}
		if draft.AmazonPunchoutSessionID != nil {
			required = append(required, "native_amazon_punchout_required")
		}
		if (op == "submit" && draft.Status != "draft") || (op == "update" && draft.Status != "draft" && draft.Status != "returned") {
			required = append(required, "editable_draft")
		}
		if input.ExpectedUpdatedAt != "" && input.ExpectedUpdatedAt != current["updatedAt"] || input.ConfirmChange && !input.Preview && input.ExpectedUpdatedAt == "" {
			required = append(required, "expected_updated_at")
		}
	}
	if op == "submit" {
		draft.Status = "submitted"
		draft.ApprovedBy = nil
		draft.ApprovedByName = ""
		draft.DecisionNote = ""
		draft.DecidedAt = nil
	} else {
		if err := applyRequisitionDraft(input, &draft, false); err != nil {
			return nil, draft, err
		}
		draft.Status = "draft"
	}
	retainedLines := draft.Lines
	draft.Lines = append([]models.RequisitionLine(nil), draft.Lines...)
	if msg := validateRequisition(&draft); msg != "" {
		required = append(required, "valid_draft_fields")
	}
	if op != "create" && input.Lines == nil {
		draft.Lines = retainedLines
	}
	// Native records are read in ID order. Retained rows precede newly created
	// rows, whose IDs are assigned only during execution; preview the same order.
	sort.SliceStable(draft.Lines, func(i, j int) bool {
		if draft.Lines[i].ID == 0 {
			return false
		}
		return draft.Lines[j].ID == 0 || draft.Lines[i].ID < draft.Lines[j].ID
	})
	if len(draft.Lines) > 100 {
		required = append(required, "bounded_draft_lines")
	}
	deps := map[string]any{"products": []map[string]any{}, "suppliers": []map[string]any{}}
	seen := map[string]bool{}
	for i, l := range draft.Lines {
		for _, ref := range []struct {
			id         *uint
			table, key string
		}{{l.ProductID, "proc_products", "products"}, {l.PreferredSupplierID, "proc_suppliers", "suppliers"}} {
			if ref.id == nil {
				continue
			}
			key := fmt.Sprintf("%s:%d", ref.key, *ref.id)
			if seen[key] {
				continue
			}
			seen[key] = true
			rows, err := receiptSnapshot(tx, "SELECT id,name,active,updated_at FROM "+ref.table+" WHERE id=?", *ref.id)
			if err != nil {
				return nil, draft, err
			}
			deps[ref.key] = append(deps[ref.key].([]map[string]any), rows...)
			if len(rows) != 1 || rows[0]["active"] != true {
				required = append(required, fmt.Sprintf("lines[%d].active_%s", i, ref.key))
			}
		}
	}
	duplicates := []map[string]any{}
	if op == "create" {
		var err error
		duplicates, err = receiptSnapshot(tx, `SELECT id,number,title,status,is_archived,updated_at FROM proc_requisitions WHERE requester_id=? AND lower(btrim(title))=lower(btrim(?)) ORDER BY id`, draft.RequesterID, draft.Title)
		if err != nil {
			return nil, draft, err
		}
		if len(duplicates) > 100 {
			required = append(required, "bounded_duplicates")
		}
		if len(duplicates) > 0 && !input.AllowDuplicate {
			required = append(required, "allow_duplicate")
		}
	}
	raw, _ := json.Marshal(draft)
	draftMap := map[string]any{}
	if err := json.Unmarshal(raw, &draftMap); err != nil {
		return nil, draft, err
	}
	diff := map[string]any{}
	if op != "create" {
		for _, key := range []string{"title", "costCenter", "justification", "neededBy", "estimatedTotalCents", "lines", "status", "approvedBy", "approvedByName", "decisionNote", "decidedAt"} {
			if !reflect.DeepEqual(current[key], draftMap[key]) {
				diff[key] = map[string]any{"before": current[key], "after": draftMap[key]}
			}
		}
		if len(diff) == 0 {
			required = append(required, "changes")
		}
	}
	contextBody, err := json.Marshal(map[string]any{"operation": op, "current": current, "draft": draftMap, "dependencies": deps, "duplicates": duplicates, "allow_duplicate": input.AllowDuplicate})
	if err != nil {
		return nil, draft, err
	}
	if len(contextBody) > 1<<20 {
		return nil, draft, &receiptFlowError{status: 413, code: "bounded_draft_context", message: "Complete draft context exceeds the bounded preview size"}
	}
	digest := sha256.Sum256(contextBody)
	fingerprint := hex.EncodeToString(digest[:])
	if input.ExpectedContext != "" && input.ExpectedContext != fingerprint {
		required = append(required, "expected_context")
	}
	phrase := fmt.Sprintf("%s REQUISITION %d %s", strings.ToUpper(op), input.ID, fingerprint[:16])
	version := ""
	if current != nil {
		version, _ = current["updatedAt"].(string)
	}
	status := "confirmation_required"
	if len(required) > 0 {
		status = "needs_input"
	}
	return map[string]any{"operation_status": status, "preview": true, "current": current, "draft": draftMap, "dependencies": deps, "duplicates": duplicates, "diff": diff, "ready_to_execute": len(required) == 0, "required_fields": required, "expected_updated_at": version, "expected_context": fingerprint, "required_confirmation_text": phrase, "effects": map[string]any{"external_messages": false, "physical_stock_changed": false, "omitted_lines_retained": true}}, draft, nil
}
