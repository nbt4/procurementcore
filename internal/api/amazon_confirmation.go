package api

import (
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"sort"
	"strings"
	"time"

	"procurementcore/internal/amazon"
	"procurementcore/internal/models"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var errAmazonConfirmationMismatch = errors.New("Amazon confirmation does not match purchase order")

func writeAmazonConfirmationResponse(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/xml; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, `<?xml version="1.0" encoding="UTF-8"?><cXML><Response><Status code="200" text="OK"/></Response></cXML>`)
}

// HandleAmazonConfirmation receives Amazon's optional cXML Order Confirmation.
// Amazon can send HTTP Basic or credentials inside the cXML Header.
func (h *Handler) HandleAmazonConfirmation(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	data, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "Confirmation too large", http.StatusRequestEntityTooLarge)
		return
	}
	username, password, basic := r.BasicAuth()
	if h.amazon == nil || !(basic && h.amazon.VerifyConfirmationCredentials(username, password)) && !h.amazon.VerifyConfirmationCXML(data) {
		w.Header().Set("WWW-Authenticate", `Basic realm="ProcurementCore Amazon confirmation"`)
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	confirmation, err := amazon.ParseConfirmation(data, h.amazon.BuyerIdentity())
	if err != nil {
		http.Error(w, "Invalid cXML confirmation", http.StatusBadRequest)
		return
	}
	if err := h.applyAmazonConfirmation(confirmation); err != nil {
		if errors.Is(err, errAmazonConfirmationMismatch) || errors.Is(err, gorm.ErrRecordNotFound) {
			http.Error(w, "Unknown or conflicting purchase order", http.StatusConflict)
			return
		}
		serverError(w, err)
		return
	}
	writeAmazonConfirmationResponse(w)
}

func (h *Handler) HandleAmazonShipment(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	data, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "Shipment too large", http.StatusRequestEntityTooLarge)
		return
	}
	username, password, basic := r.BasicAuth()
	if h.amazon == nil || !(basic && h.amazon.VerifyConfirmationCredentials(username, password)) && !h.amazon.VerifyConfirmationCXML(data) {
		w.Header().Set("WWW-Authenticate", `Basic realm="ProcurementCore Amazon shipment"`)
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	shipment, err := amazon.ParseShipment(data, h.amazon.BuyerIdentity())
	if err != nil {
		http.Error(w, "Invalid cXML shipment", http.StatusBadRequest)
		return
	}
	if err := h.applyAmazonShipment(shipment); err != nil {
		if errors.Is(err, errAmazonConfirmationMismatch) || errors.Is(err, gorm.ErrRecordNotFound) {
			http.Error(w, "Unknown or conflicting purchase order", http.StatusConflict)
			return
		}
		serverError(w, err)
		return
	}
	writeAmazonConfirmationResponse(w)
}

func (h *Handler) applyAmazonShipment(s amazon.Shipment) error {
	return h.db.Transaction(func(tx *gorm.DB) error {
		for _, ref := range s.Orders {
			var order models.PurchaseOrder
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("number = ? AND amazon_punchout_session_id IS NOT NULL", ref.Number).First(&order).Error; err != nil {
				return err
			}
			if order.AmazonPayloadID == "" || (ref.PayloadID != "" && ref.PayloadID != order.AmazonPayloadID) || order.Status == "draft" || order.Status == "cancelled" {
				return errAmazonConfirmationMismatch
			}
			var count int64
			if err := tx.Model(&models.AmazonShipment{}).Where("payload_id = ? AND purchase_order_id = ?", s.PayloadID, order.ID).Count(&count).Error; err != nil {
				return err
			}
			if count > 0 {
				continue
			}
			row := models.AmazonShipment{PayloadID: s.PayloadID, PurchaseOrderID: order.ID, ShipmentID: s.ShipmentID, TrackingNumber: s.TrackingNumber, Carrier: s.Carrier, ShipmentDate: s.ShipmentDate, DeliveryDate: s.DeliveryDate}
			if err := tx.Create(&row).Error; err != nil {
				return err
			}
			if s.DeliveryDate != nil && (order.ExpectedDelivery == nil || s.DeliveryDate.After(*order.ExpectedDelivery)) {
				if err := tx.Model(&order).Update("expected_delivery", s.DeliveryDate).Error; err != nil {
					return err
				}
			}
			if err := tx.Create(&models.Activity{EntityType: "purchase_order", EntityID: order.ID, Action: "amazon_shipment", Username: "Amazon Business", Details: fmt.Sprintf("Sendung %s; Tracking %s", s.ShipmentID, s.TrackingNumber)}).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

func (h *Handler) applyAmazonConfirmation(c amazon.Confirmation) error {
	return h.db.Transaction(func(tx *gorm.DB) error {
		var order models.PurchaseOrder
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Preload("Lines", func(db *gorm.DB) *gorm.DB { return db.Order("id") }).Where("number = ? AND amazon_punchout_session_id IS NOT NULL", c.PurchaseOrder).First(&order).Error; err != nil {
			return err
		}
		if order.AmazonPayloadID == "" || (c.OrderPayloadID != "" && c.OrderPayloadID != order.AmazonPayloadID) || order.Status == "draft" {
			return errAmazonConfirmationMismatch
		}
		var seen models.AmazonConfirmationEvent
		if err := tx.Where("payload_id = ?", c.PayloadID).First(&seen).Error; err == nil {
			if seen.PurchaseOrderID != order.ID {
				return errAmazonConfirmationMismatch
			}
			return nil
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		if order.Status == "received" || order.Status == "partially_received" {
			return errAmazonConfirmationMismatch // never silently cancel or rewrite a received order
		}
		if c.Operation == "delete" {
			return errAmazonConfirmationMismatch
		}
		if c.Type == "reject" && len(c.Lines) == 0 {
			if err := tx.Where("purchase_order_id = ?", order.ID).Delete(&models.AmazonLineConfirmation{}).Error; err != nil {
				return err
			}
			for _, line := range order.Lines {
				row := models.AmazonLineConfirmation{PurchaseOrderID: order.ID, PurchaseOrderLineID: line.ID, AmazonOrderNumber: c.AmazonOrderID, RejectedQuantity: line.Quantity, NoticeDate: c.NoticeDate}
				if err := tx.Create(&row).Error; err != nil {
					return err
				}
			}
		} else if c.Type == "accept" && len(c.Lines) == 0 {
			if err := tx.Where("purchase_order_id = ?", order.ID).Delete(&models.AmazonLineConfirmation{}).Error; err != nil {
				return err
			}
			for _, line := range order.Lines {
				row := models.AmazonLineConfirmation{PurchaseOrderID: order.ID, PurchaseOrderLineID: line.ID, AmazonOrderNumber: c.AmazonOrderID, AcceptedQuantity: line.Quantity, NoticeDate: c.NoticeDate}
				if err := tx.Create(&row).Error; err != nil {
					return err
				}
			}
		} else {
			type lineKey struct {
				Number        int
				AmazonOrderID string
			}
			combined := map[lineKey]amazon.ConfirmationLine{}
			for _, item := range c.Lines {
				key := lineKey{item.LineNumber, item.AmazonOrderID}
				row := combined[key]
				row.LineNumber, row.AmazonOrderID = item.LineNumber, item.AmazonOrderID
				row.Accepted += item.Accepted
				row.Rejected += item.Rejected
				if item.Delivery != nil && (row.Delivery == nil || item.Delivery.After(*row.Delivery)) {
					row.Delivery = item.Delivery
				}
				combined[key] = row
			}
			for _, item := range combined {
				if item.LineNumber > len(order.Lines) {
					return errAmazonConfirmationMismatch
				}
				line := order.Lines[item.LineNumber-1]
				var existing models.AmazonLineConfirmation
				err := tx.Where("purchase_order_line_id = ? AND amazon_order_number = ?", line.ID, item.AmazonOrderID).First(&existing).Error
				if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
					return err
				}
				if err == nil && existing.NoticeDate != nil && c.NoticeDate != nil && c.NoticeDate.Before(*existing.NoticeDate) {
					continue // delayed older update must not undo a cancellation
				}
				if existing.ID == 0 {
					existing.PurchaseOrderID, existing.PurchaseOrderLineID, existing.AmazonOrderNumber = order.ID, line.ID, item.AmazonOrderID
				}
				if err == nil && item.Accepted == 0 && item.Rejected > 0 && item.Rejected < existing.AcceptedQuantity && c.Operation == "update" {
					existing.AcceptedQuantity -= item.Rejected
					existing.RejectedQuantity += item.Rejected
				} else {
					existing.AcceptedQuantity, existing.RejectedQuantity = item.Accepted, item.Rejected
				}
				if item.Delivery != nil {
					existing.ExpectedDelivery = item.Delivery
				}
				existing.NoticeDate = c.NoticeDate
				if err := tx.Save(&existing).Error; err != nil {
					return err
				}
			}
		}
		var states []models.AmazonLineConfirmation
		if err := tx.Where("purchase_order_id = ?", order.ID).Find(&states).Error; err != nil {
			return err
		}
		var numbers []string
		seenNumbers := map[string]bool{}
		var delivery *time.Time
		var acceptedTotal, rejectedTotal, requestedTotal float64
		for _, line := range order.Lines {
			requestedTotal += line.Quantity
			var accounted float64
			for _, state := range states {
				if state.PurchaseOrderLineID != line.ID {
					continue
				}
				accounted += state.AcceptedQuantity + state.RejectedQuantity
				acceptedTotal += state.AcceptedQuantity
				rejectedTotal += state.RejectedQuantity
				if state.AcceptedQuantity > 0 && state.AmazonOrderNumber != "" && !seenNumbers[state.AmazonOrderNumber] {
					seenNumbers[state.AmazonOrderNumber] = true
					numbers = append(numbers, state.AmazonOrderNumber)
				}
				if state.AcceptedQuantity > 0 && state.ExpectedDelivery != nil && (delivery == nil || state.ExpectedDelivery.After(*delivery)) {
					delivery = state.ExpectedDelivery
				}
			}
			if accounted > line.Quantity+0.000001 {
				return errAmazonConfirmationMismatch
			}
		}
		sort.Strings(numbers)
		newStatus := order.Status
		if math.Abs(rejectedTotal-requestedTotal) < 0.000001 {
			newStatus = "cancelled"
		} else if math.Abs(acceptedTotal-requestedTotal) < 0.000001 {
			newStatus = "confirmed"
		} else if acceptedTotal > 0 && rejectedTotal > 0 && math.Abs(acceptedTotal+rejectedTotal-requestedTotal) < 0.000001 {
			newStatus = "partially_confirmed"
		} else if order.Status != "cancelled" {
			newStatus = "sent"
		}
		if order.Status == "cancelled" && newStatus != "cancelled" {
			return errAmazonConfirmationMismatch
		}
		orderNumbers := strings.Join(numbers, ", ")
		if orderNumbers == "" && newStatus == "cancelled" {
			orderNumbers = order.SupplierOrderNumber
		}
		if len(orderNumbers) > 120 && len(numbers) > 0 {
			orderNumbers = numbers[0]
		}
		allNumbers := strings.Join(numbers, ", ")
		if allNumbers == "" && newStatus == "cancelled" {
			allNumbers = order.AmazonOrderNumbers
		}
		updates := map[string]any{"status": newStatus, "supplier_order_number": orderNumbers, "amazon_order_numbers": allNumbers, "expected_delivery": delivery}
		if err := tx.Model(&order).Updates(updates).Error; err != nil {
			return err
		}
		event := models.AmazonConfirmationEvent{PayloadID: c.PayloadID, PurchaseOrderID: order.ID, ConfirmID: c.AmazonOrderID, Type: c.Type, NoticeDate: c.NoticeDate}
		if err := tx.Create(&event).Error; err != nil {
			return err
		}
		return tx.Create(&models.Activity{EntityType: "purchase_order", EntityID: order.ID, Action: "amazon_confirmation", Username: "Amazon Business", Details: fmt.Sprintf("%s: %s; Amazon %s", c.Type, newStatus, strings.Join(numbers, ", "))}).Error
	})
}
