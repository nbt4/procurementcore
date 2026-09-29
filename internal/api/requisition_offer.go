package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"procurementcore/internal/auth"
	"procurementcore/internal/models"
	"procurementcore/internal/orderimport"
	"procurementcore/internal/service"

	"gorm.io/gorm"
)

type offeredRequisitionLine struct {
	models.RequisitionLine
	CreateProduct bool   `json:"createProduct"`
	SKU           string `json:"sku"`
	ProductName   string `json:"productName"`
	Manufacturer  string `json:"manufacturer"`
}

type offeredRequisitionInput struct {
	Title         string                   `json:"title"`
	CostCenter    string                   `json:"costCenter"`
	Justification string                   `json:"justification"`
	NeededBy      *time.Time               `json:"neededBy"`
	SupplierID    uint                     `json:"supplierId"`
	OfferNumber   string                   `json:"offerNumber"`
	SourceFile    string                   `json:"sourceFileName"`
	Currency      string                   `json:"currency"`
	Lines         []offeredRequisitionLine `json:"lines"`
}

func (h *Handler) previewRequisitionOffer(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, orderimport.MaxPDFBytes+(1<<20))
	if err := r.ParseMultipartForm(orderimport.MaxPDFBytes); err != nil {
		var sizeErr *http.MaxBytesError
		if errors.As(err, &sizeErr) {
			writeJSON(w, http.StatusRequestEntityTooLarge, map[string]string{"error": "PDF darf maximal 12 MB groß sein"})
		} else {
			badRequest(w, "Ungültiger PDF-Upload")
		}
		return
	}
	if r.MultipartForm != nil {
		defer r.MultipartForm.RemoveAll()
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		badRequest(w, "PDF-Datei ist erforderlich")
		return
	}
	defer file.Close()
	if header.Size <= 0 || header.Size > orderimport.MaxPDFBytes {
		writeJSON(w, http.StatusRequestEntityTooLarge, map[string]string{"error": "PDF darf maximal 12 MB groß sein"})
		return
	}
	data, err := io.ReadAll(io.LimitReader(file, orderimport.MaxPDFBytes+1))
	if err != nil || len(data) > orderimport.MaxPDFBytes {
		writeJSON(w, http.StatusRequestEntityTooLarge, map[string]string{"error": "PDF darf maximal 12 MB groß sein"})
		return
	}
	text, pages, ocrUsed, err := orderimport.ExtractTextWithOCR(r.Context(), data)
	if err != nil {
		badRequest(w, err.Error())
		return
	}

	var supplierRows []models.Supplier
	if err := h.db.Where("active = ?", true).Order("name").Find(&supplierRows).Error; err != nil {
		serverError(w, err)
		return
	}
	var productRows []models.Product
	if err := h.db.Where("active = ?", true).Preload("Offers", "active = ?", true).Order("name").Find(&productRows).Error; err != nil {
		serverError(w, err)
		return
	}
	suppliers := make([]orderimport.SupplierHint, 0, len(supplierRows))
	for _, supplier := range supplierRows {
		suppliers = append(suppliers, orderimport.SupplierHint{ID: supplier.ID, Name: supplier.Name, Code: supplier.Code, Website: supplier.Website, Email: supplier.Email})
	}
	products := make([]orderimport.ProductHint, 0, len(productRows))
	for _, product := range productRows {
		offers := make([]orderimport.OfferHint, 0, len(product.Offers))
		for _, offer := range product.Offers {
			offers = append(offers, orderimport.OfferHint{SupplierID: offer.SupplierID, SupplierSKU: offer.SupplierSKU, PurchaseURL: offer.PurchaseURL})
		}
		products = append(products, orderimport.ProductHint{ID: product.ID, SKU: product.SKU, Name: product.Name, Unit: product.Unit, Manufacturer: product.Manufacturer, Model: product.Model, Offers: offers})
	}
	preview := orderimport.AnalyzeOffer(header.Filename, text, pages, ocrUsed, suppliers, products)
	match := orderimport.Preview{SupplierID: preview.SupplierID, Lines: preview.Lines, Warnings: preview.Warnings}
	h.enrichOrderImportWithJev(r.Context(), &match, products)
	preview.Lines, preview.Warnings = match.Lines, match.Warnings
	writeJSON(w, http.StatusOK, preview)
}

func (h *Handler) createRequisitionFromOffer(w http.ResponseWriter, r *http.Request) {
	var input offeredRequisitionInput
	if !decode(w, r, &input) {
		return
	}
	if len(input.Lines) < 1 || len(input.Lines) > 100 || input.SupplierID == 0 {
		badRequest(w, "Lieferant und 1 bis 100 Angebotspositionen sind erforderlich")
		return
	}
	input.Currency = strings.ToUpper(strings.TrimSpace(input.Currency))
	if input.Currency == "" {
		input.Currency = "EUR"
	}
	if input.Currency != "EUR" {
		badRequest(w, "Bedarfe unterstützen derzeit nur EUR; Angebotspreise bitte vor der Anlage umrechnen")
		return
	}
	row := models.Requisition{Title: input.Title, CostCenter: input.CostCenter, Justification: input.Justification, NeededBy: input.NeededBy}
	for _, offered := range input.Lines {
		if offered.CreateProduct {
			if offered.ProductID != nil {
				badRequest(w, "Neue Position darf kein bestehendes Produkt referenzieren")
				return
			}
			product := models.Product{SKU: offered.SKU, Name: offered.ProductName, Unit: offered.Unit, Manufacturer: offered.Manufacturer}
			if msg := validateProduct(&product); msg != "" {
				badRequest(w, msg)
				return
			}
		}
		row.Lines = append(row.Lines, offered.RequisitionLine)
	}
	if msg := validateRequisition(&row); msg != "" {
		badRequest(w, msg)
		return
	}
	if len([]rune(input.OfferNumber)) > 120 || len([]rune(input.SourceFile)) > 255 {
		badRequest(w, "Angebotsreferenz ist zu lang")
		return
	}
	if input.OfferNumber != "" || input.SourceFile != "" {
		row.Justification = strings.TrimSpace(row.Justification + "\nAngebotsquelle: " + strings.TrimSpace(input.OfferNumber+" · "+input.SourceFile))
	}
	user := auth.CurrentUser(r)
	err := h.db.Transaction(func(tx *gorm.DB) error {
		idempotency, replay, err := beginIdempotentMutation(tx, r, "requisition_from_offer", input)
		if err != nil {
			return err
		}
		if replay != nil {
			return json.Unmarshal(replay, &row)
		}
		var supplier models.Supplier
		if err := tx.Where("id = ? AND active = ?", input.SupplierID, true).First(&supplier).Error; err != nil {
			return &receiptFlowError{status: http.StatusConflict, code: "supplier_inactive", message: "Lieferant fehlt oder ist inaktiv"}
		}
		for i, offered := range input.Lines {
			row.Lines[i].ID, row.Lines[i].RequisitionID, row.Lines[i].Product = 0, 0, nil
			row.Lines[i].PreferredSupplierID = &input.SupplierID
			if !offered.CreateProduct {
				continue
			}
			product := models.Product{SKU: offered.SKU, Name: offered.ProductName, Unit: offered.Unit, Manufacturer: offered.Manufacturer, Active: true}
			if msg := validateProduct(&product); msg != "" {
				return &receiptFlowError{status: http.StatusBadRequest, code: "invalid_product", message: msg}
			}
			if err := tx.Create(&product).Error; err != nil {
				return err
			}
			row.Lines[i].ProductID = &product.ID
			if err := auditCreatedOfferProduct(tx, r, product); err != nil {
				return err
			}
			if offered.EstimatedPriceCents > 0 {
				offer := models.Offer{ProductID: product.ID, SupplierID: input.SupplierID, SupplierSKU: product.SKU, PriceCents: offered.EstimatedPriceCents, Currency: input.Currency, MinimumQuantity: 1, PackSize: 1, PurchaseURL: offered.PurchaseURL, Active: true}
				if msg := validateOffer(&offer); msg != "" {
					return &receiptFlowError{status: http.StatusBadRequest, code: "invalid_offer", message: msg}
				}
				if err := tx.Create(&offer).Error; err != nil {
					return err
				}
				if err := service.RecordPriceAndEvaluateAlerts(tx, &offer); err != nil {
					return err
				}
				if err := auditOfferMutation(tx, r, "offer.create", nil, &offer); err != nil {
					return err
				}
			}
		}
		if err := validateRequisitionReferences(tx, &row); err != nil {
			return err
		}
		row.ID, row.Number, row.Status = 0, nextNumber("BAN"), "draft"
		row.RequesterID, row.RequesterName = user.ID, user.Username
		if err := tx.Create(&row).Error; err != nil {
			return err
		}
		if err := tx.Preload("Lines").First(&row, row.ID).Error; err != nil {
			return err
		}
		if err := auditRequisitionMutation(tx, r, "created", nil, row); err != nil {
			return err
		}
		return completeIdempotentMutation(tx, idempotency, http.StatusCreated, row)
	})
	if err != nil {
		var flowErr *receiptFlowError
		if errors.As(err, &flowErr) {
			writeProcurementFlowError(w, err)
		} else {
			conflictOrServer(w, err)
		}
		return
	}
	writeJSON(w, http.StatusCreated, row)
}

func auditCreatedOfferProduct(tx *gorm.DB, r *http.Request, product models.Product) error {
	user := auth.CurrentUser(r)
	value, err := json.Marshal(map[string]any{"origin": "offer_pdf", "after": product})
	if err != nil {
		return err
	}
	if err := tx.Exec(`INSERT INTO audit_log (user_id,action,entity_type,entity_id,old_values,new_values,ip_address,user_agent) VALUES (?, 'product.create', 'procurement_product', ?, NULL, ?::jsonb, ?, ?)`, user.ID, strconv.FormatUint(uint64(product.ID), 10), string(value), requestIP(r), r.UserAgent()).Error; err != nil {
		return err
	}
	return tx.Create(&models.Activity{EntityType: "product", EntityID: product.ID, Action: "created", UserID: user.ID, Username: user.Username, Details: fmt.Sprintf("origin=offer_pdf %s", product.Name)}).Error
}
