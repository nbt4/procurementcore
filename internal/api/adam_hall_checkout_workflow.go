package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"time"

	"procurementcore/internal/auth"
	"procurementcore/internal/models"
	"procurementcore/internal/scraper"

	"github.com/go-chi/chi/v5"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func (h *Handler) adamHallCheckoutMCP(w http.ResponseWriter, r *http.Request) {
	operation := chi.URLParam(r, "operation")
	if operation != "cart" && operation != "send" {
		notFound(w)
		return
	}
	if !signedProcurementUserDelegation(r, "cores:procurement:send") {
		writeJSON(w, 403, map[string]string{"error": "Explicit signed real-user supplier send scope required"})
		return
	}
	input, ok := readAdamHallCheckoutRequest(w, r, 0)
	if !ok {
		return
	}
	h.runAdamHallCheckout(w, r, operation, input, false)
}

func readAdamHallCheckoutRequest(w http.ResponseWriter, r *http.Request, id uint) (adamHallCheckoutRequest, bool) {
	var input adamHallCheckoutRequest
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16384))
	d.DisallowUnknownFields()
	if err := d.Decode(&input); err != nil || d.Decode(new(any)) != io.EOF || input.ID < 0 || input.ID > math.MaxInt32 || input.CheckoutID < 0 || input.CheckoutID > math.MaxInt32 || id > 0 && input.ID != 0 && input.ID != int64(id) {
		badRequest(w, "One bounded exact reviewed Adam Hall action required")
		return input, false
	}
	if id > 0 {
		input.ID = int64(id)
	}
	if input.ID == 0 {
		badRequest(w, "Exact order required")
		return input, false
	}
	return input, true
}

func (h *Handler) runAdamHallCheckout(w http.ResponseWriter, r *http.Request, operation string, input adamHallCheckoutRequest, native bool) {
	preview := input.Preview || !input.ConfirmChange
	result := map[string]any{}
	var draft adamHallOwnerDraft
	var checkoutClaim adamHallCheckoutRecord
	var submissionClaim orderSubmissionRecord
	var receipt *models.IdempotencyRecord
	var privateToken string
	err := h.db.WithContext(r.Context()).Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec(`SET LOCAL lock_timeout='5s';SET LOCAL statement_timeout='20s';SET LOCAL TIME ZONE 'UTC'`).Error; err != nil {
			return err
		}
		if err := currentProcurementApprovalRights(tx, r, "orders", 0); err != nil {
			return err
		}
		if !preview {
			if len(input.ExpectedContext) != 64 {
				return &receiptFlowError{status: 428, code: "context_required", message: "Prepare and explicitly confirm the complete final supplier action"}
			}
			var replay json.RawMessage
			var err error
			receipt, replay, err = beginRequiredOwnerMutation(tx, r, fmt.Sprintf("mcp_adam_hall_%s:%d", operation, input.ID), input)
			if err != nil {
				return err
			}
			if replay != nil {
				if err := json.Unmarshal(replay, &result); err != nil {
					return err
				}
				if result["operation_status"] == "pending" {
					if operation == "cart" {
						var saved adamHallCheckoutRecord
						if err := tx.First(&saved, result["checkout_id"]).Error; err != nil {
							return err
						}
						if saved.Status != "preparing" {
							result, err = finishAdamHallCart(tx, r, saved, receipt)
							return err
						}
					} else {
						var saved orderSubmissionRecord
						if err := tx.Where("purchase_order_id=?", input.ID).First(&saved).Error; err != nil {
							return err
						}
						if saved.Status != "pending" {
							result, err = finalizeAmazonSubmission(tx, r, saved, receipt)
							return err
						}
					}
				}
				return nil
			}
		}
		if err := lockRequisitionOrderContext(tx); err != nil {
			return err
		}
		p, prepared, err := h.prepareAdamHallOwner(tx, r, operation, input)
		if err != nil {
			return err
		}
		result = p
		draft = prepared
		if preview || p["ready_to_execute"] != true {
			return errLifecyclePreview
		}
		if input.ConfirmationText != p["required_confirmation_text"] {
			return &receiptFlowError{status: 428, code: "confirmation_phrase_required", message: "Copy exact supplier action/order/context-bound phrase"}
		}
		if _, err := adamHallCheckoutCipher(); err != nil {
			return err
		}
		raw, err := json.Marshal(p)
		if err != nil {
			return err
		}
		if operation == "cart" {
			checkoutClaim = adamHallCheckoutRecord{PurchaseOrderID: input.ID, UserID: auth.CurrentUser(r).ID, LocalContext: p["local_context"].(string), AccountFingerprint: h.scraper.AdamHallAccountFingerprint(), Status: "preparing", ReviewedRequest: json.RawMessage(raw), CartSnapshot: json.RawMessage(`{}`), CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
			if err := tx.Create(&checkoutClaim).Error; err != nil {
				return err
			}
			if err := auditAdamHallCheckout(tx, r, "adam_hall_cart_preparation_requested", nil, checkoutClaim); err != nil {
				return err
			}
			result = map[string]any{"operation_status": "pending", "checkout_id": checkoutClaim.ID, "effects": map[string]any{"remote_cart_may_be_changed": true, "paid_supplier_order": false, "physical_stock_changed": false}, "message": "A durable remote cart preparation claim exists. Retry only this original request to inspect its saved outcome; no paid order is sent by this action."}
		} else {
			privateToken, err = decryptAdamHallContext(draft.Checkout)
			if err != nil {
				return err
			}
			proposed := p["proposed_order"].(models.PurchaseOrder)
			// Synchronize only the prices explicitly reviewed for this checkout,
			// before the immutable supplier claim is inserted in the same transaction.
			for _, line := range proposed.Lines {
				if err := tx.Model(&models.PurchaseOrderLine{}).Where("id=? AND purchase_order_id=?", line.ID, input.ID).Update("unit_price_cents", line.UnitPriceCents).Error; err != nil {
					return err
				}
			}
			if err := tx.Model(&models.PurchaseOrder{}).Where("id=?", input.ID).Updates(map[string]any{"status": "submitting", "currency": proposed.Currency, "total_cents": proposed.TotalCents}).Error; err != nil {
				return err
			}
			var nonce [16]byte
			if _, err := rand.Read(nonce[:]); err != nil {
				return err
			}
			submissionClaim = orderSubmissionRecord{PurchaseOrderID: input.ID, Provider: "adam_hall", UserID: auth.CurrentUser(r).ID, PayloadID: hex.EncodeToString(nonce[:]) + "@procurementcore", ExpectedContext: p["expected_context"].(string), Status: "pending", Outcome: json.RawMessage(`{}`), ReviewedPayload: json.RawMessage(raw), CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
			if err := tx.Create(&submissionClaim).Error; err != nil {
				return err
			}
			var order models.PurchaseOrder
			if err := tx.Preload("Lines").First(&order, input.ID).Error; err != nil {
				return err
			}
			if err := auditOrderMutation(tx, r, "adam_hall_submission_requested", p["current"], order); err != nil {
				return err
			}
			result = map[string]any{"operation_status": "pending", "submission_id": submissionClaim.ID, "purchase_order": order, "supplier_cart": draft.Cart, "effects": map[string]any{"external_outcome": "unresolved", "automatic_resubmission": false, "physical_stock_changed": false}, "message": "A durable supplier submission claim exists. Check the external outcome and never resend automatically."}
		}
		return completeIdempotentMutation(tx, receipt, 202, result)
	})
	if errors.Is(err, errLifecyclePreview) {
		writeJSON(w, 200, result)
		return
	}
	if err != nil {
		writeProcurementFlowError(w, err)
		return
	}
	if checkoutClaim.ID == 0 && submissionClaim.ID == 0 {
		writeAdamHallResult(w, result, operation, native)
		return
	}
	if operation == "cart" {
		result, err = h.completeAdamHallRemoteCart(r, draft, checkoutClaim, receipt)
	} else {
		result, err = h.completeAdamHallRemoteOrder(r, draft, submissionClaim, receipt, privateToken)
	}
	if err != nil {
		writeProcurementFlowError(w, err)
		return
	}
	writeAdamHallResult(w, result, operation, native)
}

func auditAdamHallCheckout(tx *gorm.DB, r *http.Request, action string, before any, after adamHallCheckoutRecord) error {
	oldJSON, err := json.Marshal(before)
	if err != nil {
		return err
	}
	newJSON, err := json.Marshal(map[string]any{"origin": func() string {
		if isMCPMutation(r) {
			return "MCP/AI"
		}
		return "UI"
	}(), "checkout": after})
	if err != nil {
		return err
	}
	if err := tx.Exec(`INSERT INTO audit_log(user_id,action,entity_type,entity_id,old_values,new_values,ip_address,user_agent) VALUES(?,?,'procurement_order',?,?::jsonb,?::jsonb,?,?)`, after.UserID, "order."+action, fmt.Sprint(after.PurchaseOrderID), string(oldJSON), string(newJSON), requestIP(r), r.UserAgent()).Error; err != nil {
		return err
	}
	return tx.Create(&models.Activity{EntityType: "purchase_order", EntityID: uint(after.PurchaseOrderID), Action: action, UserID: after.UserID, Username: auth.CurrentUser(r).Username, Details: "origin=guided_supplier_checkout"}).Error
}

func finishAdamHallCart(tx *gorm.DB, r *http.Request, checkout adamHallCheckoutRecord, receipt *models.IdempotencyRecord) (map[string]any, error) {
	if receipt != nil {
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(receipt, receipt.ID).Error; err != nil {
			return nil, err
		}
	}
	if err := tx.Clauses(clause.Locking{Strength: "SHARE"}).First(&checkout, checkout.ID).Error; err != nil {
		return nil, err
	}
	result := map[string]any{"operation_status": checkout.Status, "checkout_id": checkout.ID, "supplier_cart": checkout.CartSnapshot, "effects": map[string]any{"paid_supplier_order": false, "physical_stock_changed": false, "existing_unrelated_cart_contents_never_deleted": true}}
	if checkout.Status == "failed" {
		result["failure_code"] = checkout.FailureCode
		result["message"] = "Remote cart could not be verified. Check the supplier account and existing contents; no paid order was sent."
	}
	if err := auditAdamHallCheckout(tx, r, "adam_hall_cart_preparation_finished", nil, checkout); err != nil {
		return nil, err
	}
	if err := completeIdempotentMutation(tx, receipt, 200, result); err != nil {
		return nil, err
	}
	return result, nil
}

func (h *Handler) completeAdamHallRemoteCart(r *http.Request, draft adamHallOwnerDraft, checkout adamHallCheckoutRecord, receipt *models.IdempotencyRecord) (map[string]any, error) {
	remote, remoteErr := h.scraper.PrepareAdamHallReviewedCheckout(r.Context(), draft.Items)
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 30*time.Second)
	defer cancel()
	completion := r.WithContext(ctx)
	status := "ready"
	failureCode := ""
	snapshot := json.RawMessage(`{}`)
	var encrypted []byte
	if remoteErr == nil {
		raw, _ := json.Marshal(remote.Cart)
		snapshot = json.RawMessage(raw)
		encrypted, remoteErr = encryptAdamHallContext(checkout, remote.ContextToken)
	}
	if remoteErr != nil {
		status = "failed"
		failureCode = "remote_cart_preparation_failed"
		snapshot = json.RawMessage(`{}`)
		encrypted = nil
	}
	err := h.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&checkout, checkout.ID).Error; err != nil {
			return err
		}
		before := checkout
		if err := tx.Model(&checkout).Updates(map[string]any{"status": status, "cart_snapshot": snapshot, "context_cipher": encrypted, "failure_code": failureCode}).Error; err != nil {
			return err
		}
		if err := tx.First(&checkout, checkout.ID).Error; err != nil {
			return err
		}
		return auditAdamHallCheckout(tx, completion, "adam_hall_cart_preparation_outcome", before, checkout)
	})
	if err != nil {
		return nil, err
	}
	var result map[string]any
	err = h.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var err error
		result, err = finishAdamHallCart(tx, completion, checkout, receipt)
		return err
	})
	return result, err
}

func (h *Handler) completeAdamHallRemoteOrder(r *http.Request, draft adamHallOwnerDraft, submission orderSubmissionRecord, receipt *models.IdempotencyRecord, token string) (map[string]any, error) {
	remote, remoteErr := h.scraper.SubmitReviewedAdamHallCheckout(r.Context(), token, draft.Items, draft.Cart, draft.Order.Number)
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 30*time.Second)
	defer cancel()
	completion := r.WithContext(ctx)
	status := "accepted"
	outcome := map[string]any{"acknowledged": true, "supplier_order_number": remote.OrderNumber}
	if remoteErr != nil {
		status = "unknown"
		outcome = map[string]any{"acknowledged": false, "error_code": "supplier_response_uncertain", "checkout_request_may_have_started": scraper.IsAdamHallSubmissionUncertain(remoteErr)}
	}
	err := h.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&submission, submission.ID).Error; err != nil {
			return err
		}
		before := submission
		raw, _ := json.Marshal(outcome)
		if err := tx.Model(&submission).Updates(map[string]any{"status": status, "outcome": json.RawMessage(raw)}).Error; err != nil {
			return err
		}
		if err := tx.First(&submission, submission.ID).Error; err != nil {
			return err
		}
		return auditSupplierOutcome(tx, completion, before, submission)
	})
	if err != nil {
		return nil, err
	}
	var result map[string]any
	err = h.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var err error
		result, err = finalizeAmazonSubmission(tx, completion, submission, receipt)
		return err
	})
	return result, err
}

func writeAdamHallResult(w http.ResponseWriter, result map[string]any, operation string, native bool) {
	if !native {
		writeJSON(w, 200, result)
		return
	}
	if operation == "cart" {
		writeJSON(w, 200, result)
		return
	}
	if result["operation_status"] != "sent" {
		writeJSON(w, 502, map[string]any{"error": "Bestellstatus prüfen und nicht erneut absenden. Der ursprüngliche Übermittlungsauftrag bleibt erhalten.", "result": result})
		return
	}
	writeJSON(w, 200, map[string]any{"order": result["purchase_order"], "cart": result["supplier_cart"]})
}
