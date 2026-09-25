package api

import (
	"crypto/sha256"
	"database/sql"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"procurementcore/internal/auth"
	"procurementcore/internal/jev"
	"procurementcore/internal/models"
	"procurementcore/internal/orderimport"
	"procurementcore/internal/scraper"
	"procurementcore/internal/service"

	"github.com/go-chi/chi/v5"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type Handler struct {
	db      *gorm.DB
	scraper *scraper.Fetcher
	jev     *jev.Client
}

func NewHandler(db *gorm.DB, productScraper *scraper.Fetcher) *Handler {
	return &Handler{db: db, scraper: productScraper, jev: jev.FromEnv()}
}

func (h *Handler) Routes() http.Handler {
	r := chi.NewRouter()
	r.Get("/me", h.me)
	r.Get("/dashboard", h.dashboard)
	r.Get("/categories", h.listCategories)
	r.With(auth.RequireAdmin).Post("/categories", h.createCategory)
	r.With(auth.RequireAdmin).Put("/categories/{id}", h.updateCategory)
	r.With(auth.RequireAdmin).Delete("/categories/{id}", h.deleteCategory)
	r.Get("/suppliers", h.listSuppliers)
	r.With(auth.RequireAdmin).Post("/suppliers", h.createSupplier)
	r.With(auth.RequireAdmin).Put("/suppliers/{id}", h.updateSupplier)
	r.With(auth.RequireAdmin).Delete("/suppliers/{id}", h.deleteSupplier)
	r.Get("/products", h.listProducts)
	r.With(auth.RequireAdmin).Post("/products/import-preview", h.importProductPreview)
	r.Get("/products/{id}", h.getProduct)
	r.With(auth.RequireAdmin).Post("/products", h.createProduct)
	r.With(auth.RequireAdmin).Put("/products/{id}", h.updateProduct)
	r.With(auth.RequireAdmin).Delete("/products/{id}", h.deleteProduct)
	r.Get("/product-links", h.listProductLinks)
	r.Get("/products/{id}/warehouse-candidates", h.warehouseCandidates)
	r.With(auth.RequireAdmin).Post("/products/{id}/warehouse-link", h.linkWarehouseProduct)
	r.With(auth.RequireAdmin).Delete("/products/{id}/warehouse-link", h.unlinkWarehouseProduct)
	r.Get("/products/{id}/offers", h.listOffers)
	r.With(auth.RequireAdmin).Post("/products/{id}/offers", h.createOffer)
	r.With(auth.RequireAdmin).Put("/offers/{id}", h.updateOffer)
	r.With(auth.RequireAdmin).Delete("/offers/{id}", h.deleteOffer)
	r.Get("/offers/{id}/history", h.offerHistory)
	r.Get("/alerts", h.listAlerts)
	r.Post("/alerts", h.createAlert)
	r.Put("/alerts/{id}", h.updateAlert)
	r.Delete("/alerts/{id}", h.deleteAlert)
	r.Get("/requisitions", h.listRequisitions)
	r.Get("/requisitions/{id}", h.getRequisition)
	r.Post("/requisitions", h.createRequisition)
	r.Put("/requisitions/{id}", h.updateRequisition)
	r.Post("/requisitions/{id}/submit", h.submitRequisition)
	r.With(auth.RequireAdmin).Post("/requisitions/{id}/decision", h.decideRequisition)
	r.With(auth.RequireAdmin).Post("/requisitions/{id}/order", h.convertRequisition)
	r.Get("/orders", h.listOrders)
	r.Get("/orders/{id}", h.getOrder)
	r.With(auth.RequireAdmin).Post("/orders/import-preview", h.previewOrderImport)
	r.With(auth.RequireAdmin).Post("/orders", h.createOrder)
	r.With(auth.RequireAdmin).Put("/orders/{id}", h.updateOrder)
	r.With(auth.RequireAdmin).Post("/orders/{id}/adam-hall/cart", h.previewAdamHallOrder)
	r.With(auth.RequireAdmin).Post("/orders/{id}/adam-hall/order", h.placeAdamHallOrder)
	r.With(auth.RequireAdmin).Post("/orders/{id}/receipt", h.receiveOrder)
	r.Get("/activity", h.listActivity)
	r.Get("/export/spend.csv", h.exportSpend)
	return r
}

func (h *Handler) importProductPreview(w http.ResponseWriter, r *http.Request) {
	var input struct {
		URL string `json:"url"`
	}
	if !decode(w, r, &input) {
		return
	}
	preview, err := h.scraper.Scrape(r.Context(), input.URL)
	if err != nil {
		badRequest(w, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, preview)
}

func (h *Handler) previewOrderImport(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, orderimport.MaxPDFBytes+(1<<20))
	if err := r.ParseMultipartForm(orderimport.MaxPDFBytes); err != nil {
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
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
	text, pages, err := orderimport.ExtractText(data)
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
	if err := h.db.Where("active = ?", true).
		Preload("Offers", "active = ?", true).
		Order("name").Find(&productRows).Error; err != nil {
		serverError(w, err)
		return
	}
	suppliers := make([]orderimport.SupplierHint, 0, len(supplierRows))
	for _, supplier := range supplierRows {
		suppliers = append(suppliers, orderimport.SupplierHint{
			ID: supplier.ID, Name: supplier.Name, Code: supplier.Code,
			Website: supplier.Website, Email: supplier.Email,
		})
	}
	products := make([]orderimport.ProductHint, 0, len(productRows))
	for _, product := range productRows {
		offers := make([]orderimport.OfferHint, 0, len(product.Offers))
		for _, offer := range product.Offers {
			offers = append(offers, orderimport.OfferHint{
				SupplierID: offer.SupplierID, SupplierSKU: offer.SupplierSKU, PurchaseURL: offer.PurchaseURL,
			})
		}
		products = append(products, orderimport.ProductHint{
			ID: product.ID, SKU: product.SKU, Name: product.Name, Unit: product.Unit,
			Manufacturer: product.Manufacturer, Model: product.Model, Offers: offers,
		})
	}
	preview := orderimport.Analyze(header.Filename, text, pages, suppliers, products)
	h.enrichOrderImportWithJev(r.Context(), &preview, products)
	writeJSON(w, http.StatusOK, preview)
}

func (h *Handler) me(w http.ResponseWriter, r *http.Request) {
	user := auth.CurrentUser(r)
	response := struct {
		UserID      uint   `json:"userId"`
		Username    string `json:"username"`
		DisplayName string `json:"displayName"`
		IsAdmin     bool   `json:"isAdmin"`
	}{UserID: user.ID, Username: user.Username, DisplayName: user.Username, IsAdmin: user.IsAdmin}
	h.db.Raw(`SELECT COALESCE(
		NULLIF(p.display_name, ''),
		NULLIF(TRIM(CONCAT_WS(' ', u.first_name, u.last_name)), ''),
		u.username
	) FROM users u LEFT JOIN user_profiles p ON p.user_id = u.userid WHERE u.userid = ?`, user.ID).
		Scan(&response.DisplayName)
	if strings.TrimSpace(response.DisplayName) == "" {
		response.DisplayName = user.Username
	}
	writeJSON(w, http.StatusOK, response)
}

func (h *Handler) dashboard(w http.ResponseWriter, r *http.Request) {
	type metric struct {
		Count int64 `json:"count"`
		Cents int64 `json:"cents"`
	}
	var pending, alerts, preferred, products int64
	var spend, savings int64
	h.db.Model(&models.Requisition{}).Where("status = ?", "submitted").Count(&pending)
	h.db.Model(&models.PriceAlert{}).Where("active = ? AND triggered = ?", true, true).Count(&alerts)
	h.db.Model(&models.Supplier{}).Where("active = ? AND preferred = ?", true, true).Count(&preferred)
	h.db.Model(&models.Product{}).Where("active = ?", true).Count(&products)
	h.db.Model(&models.PurchaseOrder{}).Where("status <> ?", "cancelled").Select("COALESCE(SUM(total_cents), 0)").Scan(&spend)
	// Savings is the difference between requisition estimate and final PO value.
	h.db.Raw(`SELECT COALESCE(SUM(GREATEST(r.estimated_total_cents - p.total_cents, 0)), 0)
		FROM proc_requisitions r JOIN proc_purchase_orders p ON p.requisition_id = r.id
		WHERE p.status <> 'cancelled'`).Scan(&savings)
	var recent []models.Activity
	h.db.Order("created_at DESC").Limit(8).Find(&recent)
	writeJSON(w, http.StatusOK, map[string]any{
		"pendingApprovals": pending, "triggeredAlerts": alerts, "preferredSuppliers": preferred,
		"activeProducts": products, "spend": metric{Cents: spend}, "savings": metric{Cents: savings}, "recentActivity": recent,
	})
}

func (h *Handler) listCategories(w http.ResponseWriter, r *http.Request) {
	var rows []models.Category
	if err := h.db.Order("name").Find(&rows).Error; err != nil {
		serverError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, rows)
}

func (h *Handler) createCategory(w http.ResponseWriter, r *http.Request) {
	var row models.Category
	if !decode(w, r, &row) {
		return
	}
	if msg := validateCategory(&row); msg != "" {
		badRequest(w, msg)
		return
	}
	var created models.Category
	err := h.db.Transaction(func(tx *gorm.DB) error {
		idempotency, replay, err := beginIdempotentMutation(tx, r, "category_create", row)
		if err != nil {
			return err
		}
		if replay != nil {
			return json.Unmarshal(replay, &created)
		}
		if err := tx.Create(&row).Error; err != nil {
			return err
		}
		origin := "UI"
		if isMCPMutation(r) {
			origin = "MCP/AI"
		}
		changes, err := json.Marshal(map[string]any{"origin": origin, "after": row})
		if err != nil {
			return err
		}
		user := auth.CurrentUser(r)
		if err := tx.Exec(`INSERT INTO audit_log (user_id,action,entity_type,entity_id,old_values,new_values,ip_address,user_agent)
			VALUES (?, 'category.create', 'procurement_category', ?, NULL, ?::jsonb, ?, ?)`, user.ID, strconv.FormatUint(uint64(row.ID), 10), string(changes), requestIP(r), r.UserAgent()).Error; err != nil {
			return err
		}
		if err := tx.Create(&models.Activity{EntityType: "category", EntityID: row.ID, Action: "created", UserID: user.ID, Username: user.Username, Details: "origin=" + origin + " " + row.Name}).Error; err != nil {
			return err
		}
		created = row
		return completeIdempotentMutation(tx, idempotency, http.StatusCreated, created)
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
	writeJSON(w, http.StatusCreated, created)
}

func (h *Handler) updateCategory(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var input struct {
		models.Category
		ExpectedUpdatedAt *time.Time `json:"expectedUpdatedAt"`
	}
	if !decode(w, r, &input) {
		return
	}
	if msg := validateCategory(&input.Category); msg != "" {
		badRequest(w, msg)
		return
	}
	var updated models.Category
	err := h.db.Transaction(func(tx *gorm.DB) error {
		idempotency, replay, err := beginIdempotentMutation(tx, r, fmt.Sprintf("category_update:%d", id), input)
		if err != nil {
			return err
		}
		if replay != nil {
			return json.Unmarshal(replay, &updated)
		}
		var previous models.Category
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&previous, id).Error; err != nil {
			return err
		}
		if err := validateExpectedUpdate(previous.UpdatedAt, input.ExpectedUpdatedAt, isMCPMutation(r)); err != nil {
			return err
		}
		updated = input.Category
		updated.ID, updated.CreatedAt, updated.UpdatedAt = previous.ID, previous.CreatedAt, time.Time{}
		if err := tx.Save(&updated).Error; err != nil {
			return err
		}
		if err := tx.First(&updated, id).Error; err != nil {
			return err
		}
		origin := "UI"
		if isMCPMutation(r) {
			origin = "MCP/AI"
		}
		beforeJSON, err := json.Marshal(previous)
		if err != nil {
			return err
		}
		changes, err := json.Marshal(map[string]any{"origin": origin, "before": previous, "after": updated})
		if err != nil {
			return err
		}
		user := auth.CurrentUser(r)
		if err := tx.Exec(`INSERT INTO audit_log (user_id,action,entity_type,entity_id,old_values,new_values,ip_address,user_agent)
			VALUES (?, 'category.update', 'procurement_category', ?, ?::jsonb, ?::jsonb, ?, ?)`, user.ID, strconv.FormatUint(uint64(id), 10), string(beforeJSON), string(changes), requestIP(r), r.UserAgent()).Error; err != nil {
			return err
		}
		if err := tx.Create(&models.Activity{EntityType: "category", EntityID: id, Action: "updated", UserID: user.ID, Username: user.Username, Details: "origin=" + origin + " " + updated.Name}).Error; err != nil {
			return err
		}
		return completeIdempotentMutation(tx, idempotency, http.StatusOK, updated)
	})
	if err != nil {
		var flowErr *receiptFlowError
		if errors.As(err, &flowErr) || errors.Is(err, gorm.ErrRecordNotFound) {
			writeProcurementFlowError(w, err)
		} else {
			conflictOrServer(w, err)
		}
		return
	}
	writeJSON(w, http.StatusOK, updated)
}

func validateCategory(row *models.Category) string {
	row.Name = strings.TrimSpace(row.Name)
	if row.Name == "" || len([]rune(row.Name)) > 160 {
		return "Kategoriename mit höchstens 160 Zeichen ist erforderlich"
	}
	if len(row.ParameterSchema) == 0 {
		row.ParameterSchema = json.RawMessage("[]")
	}
	if len(row.ParameterSchema) > 65536 {
		return "Parameter-Schema ist zu groß"
	}
	if !strings.HasPrefix(strings.TrimSpace(string(row.ParameterSchema)), "[") {
		return "Parameter-Schema muss eine Liste sein"
	}
	var definitions []struct {
		Key     string   `json:"key"`
		Label   string   `json:"label"`
		Type    string   `json:"type"`
		Unit    string   `json:"unit"`
		Options []string `json:"options"`
	}
	if err := json.Unmarshal(row.ParameterSchema, &definitions); err != nil || len(definitions) > 100 {
		return "Parameter-Schema ist ungültig oder enthält mehr als 100 Felder"
	}
	seen := map[string]bool{}
	for _, definition := range definitions {
		key := strings.TrimSpace(definition.Key)
		if key == "" || key != definition.Key || len([]rune(key)) > 80 || strings.TrimSpace(definition.Label) == "" || len([]rune(definition.Label)) > 160 {
			return "Parameter benötigen einen eindeutigen Schlüssel und eine Beschriftung"
		}
		if seen[strings.ToLower(key)] {
			return "Parameterschlüssel müssen eindeutig sein"
		}
		seen[strings.ToLower(key)] = true
		if !map[string]bool{"text": true, "number": true, "select": true, "boolean": true}[definition.Type] || len([]rune(definition.Unit)) > 60 {
			return "Ungültiger Parametertyp"
		}
		if len(definition.Options) > 100 {
			return "Zu viele Auswahloptionen"
		}
		if definition.Type == "select" && len(definition.Options) == 0 {
			return "Auswahlparameter benötigen Optionen"
		}
		for _, option := range definition.Options {
			if strings.TrimSpace(option) == "" || len([]rune(option)) > 160 {
				return "Auswahloptionen müssen 1 bis 160 Zeichen enthalten"
			}
		}
	}
	return ""
}

func (h *Handler) deleteCategory(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var count int64
	h.db.Model(&models.Product{}).Where("category_id = ?", id).Count(&count)
	if count > 0 {
		badRequest(w, "Kategorie wird noch von Artikeln verwendet")
		return
	}
	if h.db.Delete(&models.Category{}, id).RowsAffected == 0 {
		notFound(w)
		return
	}
	h.activity(r, "category", id, "deleted", "")
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) listSuppliers(w http.ResponseWriter, r *http.Request) {
	query := h.db.Model(&models.Supplier{})
	if value := strings.TrimSpace(r.URL.Query().Get("q")); value != "" {
		like := "%" + value + "%"
		query = query.Where("name ILIKE ? OR code ILIKE ? OR email ILIKE ?", like, like, like)
	}
	if preferred := r.URL.Query().Get("preferred"); preferred != "" {
		query = query.Where("preferred = ?", preferred == "true")
	}
	if active := r.URL.Query().Get("active"); active != "" {
		query = query.Where("active = ?", active == "true")
	}
	var rows []models.Supplier
	if err := query.Order("preferred DESC, name").Find(&rows).Error; err != nil {
		serverError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, rows)
}

func validateSupplier(row *models.Supplier) string {
	row.Name, row.Code, row.Email = strings.TrimSpace(row.Name), strings.ToUpper(strings.TrimSpace(row.Code)), strings.TrimSpace(row.Email)
	if row.Name == "" || row.Code == "" {
		return "Name und Lieferantencode sind erforderlich"
	}
	if row.Rating < 0 || row.Rating > 5 {
		return "Bewertung muss zwischen 0 und 5 liegen"
	}
	if row.RiskLevel != "low" && row.RiskLevel != "medium" && row.RiskLevel != "high" {
		return "Ungültige Risikostufe"
	}
	if row.Website != "" {
		if u, err := url.ParseRequestURI(row.Website); err != nil || (u.Scheme != "http" && u.Scheme != "https") {
			return "Website muss eine gültige HTTP(S)-URL sein"
		}
	}
	return ""
}

func (h *Handler) createSupplier(w http.ResponseWriter, r *http.Request) {
	var input struct {
		models.Supplier
		Active *bool `json:"active"`
	}
	if !decode(w, r, &input) {
		return
	}
	row := input.Supplier
	row.Active = input.Active == nil || *input.Active
	if msg := validateSupplier(&row); msg != "" {
		badRequest(w, msg)
		return
	}
	user := auth.CurrentUser(r)
	err := h.db.Transaction(func(tx *gorm.DB) error {
		idempotency, replay, err := beginIdempotentMutation(tx, r, "supplier_create", row)
		if err != nil {
			return err
		}
		if replay != nil {
			return json.Unmarshal(replay, &row)
		}
		requestedActive := row.Active
		if err := tx.Create(&row).Error; err != nil {
			return err
		}
		// GORM applies the model's default:true to a zero bool during Create.
		// Preserve an explicitly inactive supplier in the persisted record.
		if !requestedActive {
			if err := tx.Model(&row).UpdateColumn("active", false).Error; err != nil {
				return err
			}
			row.Active = false
		}
		origin := "UI"
		if isMCPMutation(r) {
			origin = "MCP/AI"
		}
		changes, err := json.Marshal(map[string]any{"origin": origin, "after": row})
		if err != nil {
			return err
		}
		if err := tx.Exec(`INSERT INTO audit_log (user_id,action,entity_type,entity_id,old_values,new_values,ip_address,user_agent)
			VALUES (?, 'supplier.create', 'procurement_supplier', ?, NULL, ?::jsonb, ?, ?)`, user.ID, strconv.FormatUint(uint64(row.ID), 10), string(changes), requestIP(r), r.UserAgent()).Error; err != nil {
			return err
		}
		if err := tx.Create(&models.Activity{EntityType: "supplier", EntityID: row.ID, Action: "created", UserID: user.ID, Username: user.Username, Details: "origin=" + origin + " " + row.Name}).Error; err != nil {
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

func (h *Handler) updateSupplier(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var input struct {
		models.Supplier
		ExpectedUpdatedAt *time.Time `json:"expectedUpdatedAt"`
	}
	if !decode(w, r, &input) {
		return
	}
	if msg := validateSupplier(&input.Supplier); msg != "" {
		badRequest(w, msg)
		return
	}
	var updated models.Supplier
	err := h.db.Transaction(func(tx *gorm.DB) error {
		idempotency, replay, err := beginIdempotentMutation(tx, r, fmt.Sprintf("supplier_update:%d", id), input)
		if err != nil {
			return err
		}
		if replay != nil {
			return json.Unmarshal(replay, &updated)
		}
		var previous models.Supplier
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&previous, id).Error; err != nil {
			return err
		}
		if err := validateExpectedUpdate(previous.UpdatedAt, input.ExpectedUpdatedAt, isMCPMutation(r)); err != nil {
			return err
		}
		updated = input.Supplier
		updated.ID, updated.CreatedAt = previous.ID, previous.CreatedAt
		updated.UpdatedAt = time.Time{}
		if previous.Active && !updated.Active {
			var openOrders int64
			if err := tx.Model(&models.PurchaseOrder{}).Where("supplier_id = ? AND status NOT IN ?", id, []string{"cancelled", "received"}).Count(&openOrders).Error; err != nil {
				return err
			}
			if openOrders > 0 {
				return &receiptFlowError{status: http.StatusConflict, code: "supplier_open_orders", message: "Lieferant besitzt offene Bestellungen und kann noch nicht deaktiviert werden"}
			}
		}
		if err := tx.Save(&updated).Error; err != nil {
			return err
		}
		origin := "UI"
		if isMCPMutation(r) {
			origin = "MCP/AI"
		}
		beforeJSON, err := json.Marshal(previous)
		if err != nil {
			return err
		}
		changes, err := json.Marshal(map[string]any{"origin": origin, "before": previous, "after": updated})
		if err != nil {
			return err
		}
		auditAction, activityAction := "supplier.update", "updated"
		if previous.Active && !updated.Active {
			auditAction, activityAction = "supplier.deactivate", "deactivated"
		} else if !previous.Active && updated.Active {
			auditAction, activityAction = "supplier.reactivate", "reactivated"
		}
		user := auth.CurrentUser(r)
		if err := tx.Exec(`INSERT INTO audit_log (user_id,action,entity_type,entity_id,old_values,new_values,ip_address,user_agent)
			VALUES (?, ?, 'procurement_supplier', ?, ?::jsonb, ?::jsonb, ?, ?)`, user.ID, auditAction, strconv.FormatUint(uint64(id), 10), string(beforeJSON), string(changes), requestIP(r), r.UserAgent()).Error; err != nil {
			return err
		}
		if err := tx.Create(&models.Activity{EntityType: "supplier", EntityID: id, Action: activityAction, UserID: user.ID, Username: user.Username, Details: "origin=" + origin + " " + updated.Name}).Error; err != nil {
			return err
		}
		return completeIdempotentMutation(tx, idempotency, http.StatusOK, updated)
	})
	if err != nil {
		var flowErr *receiptFlowError
		if errors.As(err, &flowErr) || errors.Is(err, gorm.ErrRecordNotFound) {
			writeProcurementFlowError(w, err)
		} else {
			conflictOrServer(w, err)
		}
		return
	}
	writeJSON(w, http.StatusOK, updated)
}

func (h *Handler) deleteSupplier(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var references int64
	h.db.Model(&models.Offer{}).Where("supplier_id = ?", id).Count(&references)
	if references > 0 {
		badRequest(w, "Lieferant besitzt Angebote und kann nur deaktiviert werden")
		return
	}
	if h.db.Delete(&models.Supplier{}, id).RowsAffected == 0 {
		notFound(w)
		return
	}
	h.activity(r, "supplier", id, "deleted", "")
	w.WriteHeader(http.StatusNoContent)
}

type ProductFilter struct {
	Query, Manufacturer       string
	CategoryID, SupplierID    uint
	PreferredOnly, AlertsOnly bool
	MinPrice, MaxPrice        *int64
	Parameters                map[string]string
}

func ParseProductFilter(values url.Values) ProductFilter {
	f := ProductFilter{Query: strings.TrimSpace(values.Get("q")), Manufacturer: strings.TrimSpace(values.Get("manufacturer")), Parameters: map[string]string{}}
	f.CategoryID, _ = parseUint(values.Get("categoryId"))
	f.SupplierID, _ = parseUint(values.Get("supplierId"))
	f.PreferredOnly = values.Get("preferred") == "true"
	f.AlertsOnly = values.Get("alertsOnly") == "true"
	if n, err := strconv.ParseInt(values.Get("minPriceCents"), 10, 64); err == nil {
		f.MinPrice = &n
	}
	if n, err := strconv.ParseInt(values.Get("maxPriceCents"), 10, 64); err == nil {
		f.MaxPrice = &n
	}
	for _, raw := range values["param"] {
		parts := strings.SplitN(raw, ":", 2)
		if len(parts) == 2 && strings.TrimSpace(parts[0]) != "" {
			f.Parameters[strings.TrimSpace(parts[0])] = strings.TrimSpace(parts[1])
		}
	}
	return f
}

func productSearchTerms(value string) []string {
	return strings.Fields(strings.ToLower(strings.TrimSpace(value)))
}

func (h *Handler) listProducts(w http.ResponseWriter, r *http.Request) {
	f := ParseProductFilter(r.URL.Query())
	query := h.db.Model(&models.Product{}).Preload("Category").Preload("Offers", "active = ?", true).Preload("Offers.Supplier")
	for _, term := range productSearchTerms(f.Query) {
		like := "%" + term + "%"
		query = query.Where(`(
			CONCAT_WS(' ',proc_products.id::text,proc_products.name,proc_products.sku,
			 proc_products.description,proc_products.manufacturer,proc_products.model,
			 proc_products.unit,proc_products.parameters::text,proc_products.attributes::text) ILIKE ?
			OR EXISTS (SELECT 1 FROM proc_categories search_category
			 WHERE search_category.id=proc_products.category_id AND search_category.name ILIKE ?)
			OR EXISTS (SELECT 1 FROM proc_offers search_offer
			 JOIN proc_suppliers search_supplier ON search_supplier.id=search_offer.supplier_id
			 WHERE search_offer.product_id=proc_products.id AND search_offer.active=TRUE
			 AND CONCAT_WS(' ',search_offer.supplier_sku,search_offer.purchase_url,search_supplier.name,
			 search_supplier.code) ILIKE ?)
		)`, like, like, like)
	}
	if f.Manufacturer != "" {
		query = query.Where("proc_products.manufacturer ILIKE ?", "%"+f.Manufacturer+"%")
	}
	if f.CategoryID > 0 {
		query = query.Where("proc_products.category_id = ?", f.CategoryID)
	}
	if f.SupplierID > 0 || f.PreferredOnly || f.MinPrice != nil || f.MaxPrice != nil {
		query = query.Joins("JOIN proc_offers search_offers ON search_offers.product_id = proc_products.id AND search_offers.active = TRUE").Joins("JOIN proc_suppliers search_suppliers ON search_suppliers.id = search_offers.supplier_id")
		if f.SupplierID > 0 {
			query = query.Where("search_offers.supplier_id = ?", f.SupplierID)
		}
		if f.PreferredOnly {
			query = query.Where("search_suppliers.preferred = TRUE")
		}
		if f.MinPrice != nil {
			query = query.Where("search_offers.price_cents >= ?", *f.MinPrice)
		}
		if f.MaxPrice != nil {
			query = query.Where("search_offers.price_cents <= ?", *f.MaxPrice)
		}
	}
	if f.AlertsOnly {
		query = query.Joins("JOIN proc_price_alerts search_alerts ON search_alerts.product_id = proc_products.id AND search_alerts.active = TRUE AND search_alerts.triggered = TRUE")
	}
	for key, value := range f.Parameters {
		// Compare JSON values through their textual representation so the same
		// query works for category parameters stored as strings, numbers or bools.
		query = query.Where("proc_products.parameters ->> ? = ?", key, value)
	}
	var rows []models.Product
	if err := query.Where("proc_products.active = ?", true).Distinct("proc_products.*").Order("proc_products.name").Find(&rows).Error; err != nil {
		serverError(w, err)
		return
	}
	h.hydrateWarehouseLinks(rows)
	writeJSON(w, http.StatusOK, rows)
}

func (h *Handler) getProduct(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var row models.Product
	if err := h.db.Preload("Category").Preload("Offers", func(tx *gorm.DB) *gorm.DB { return tx.Order("price_cents") }).Preload("Offers.Supplier").First(&row, id).Error; err != nil {
		notFound(w)
		return
	}
	products := []models.Product{row}
	h.hydrateWarehouseLinks(products)
	row = products[0]
	writeJSON(w, http.StatusOK, row)
}

func validateProduct(row *models.Product) string {
	row.SKU, row.Name = strings.ToUpper(strings.TrimSpace(row.SKU)), strings.TrimSpace(row.Name)
	if row.SKU == "" || row.Name == "" {
		return "SKU und Name sind erforderlich"
	}
	if len([]rune(row.SKU)) > 80 || len([]rune(row.Name)) > 240 || len([]rune(row.Unit)) > 30 || len([]rune(row.Manufacturer)) > 180 || len([]rune(row.Model)) > 180 {
		return "Ein Produktfeld überschreitet die maximal zulässige Länge"
	}
	if row.ReorderPoint < 0 || row.TargetStock < 0 || math.IsNaN(row.ReorderPoint) || math.IsNaN(row.TargetStock) || math.IsInf(row.ReorderPoint, 0) || math.IsInf(row.TargetStock, 0) {
		return "Bestandsgrenzen müssen endliche, nicht-negative Zahlen sein"
	}
	if len(row.Parameters) == 0 {
		row.Parameters = json.RawMessage("{}")
	}
	if !json.Valid(row.Parameters) {
		return "Parameter sind kein gültiges JSON"
	}
	if len(row.Attributes) == 0 {
		row.Attributes = json.RawMessage("{}")
	}
	if !json.Valid(row.Attributes) {
		return "Attribute sind kein gültiges JSON"
	}
	if row.Unit == "" {
		row.Unit = "Stk."
	}
	return ""
}

func (h *Handler) createProduct(w http.ResponseWriter, r *http.Request) {
	var input struct {
		models.Product
		InitialOffer *models.Offer `json:"initialOffer"`
	}
	if !decode(w, r, &input) {
		return
	}
	row := input.Product
	if msg := validateProduct(&row); msg != "" {
		badRequest(w, msg)
		return
	}
	if input.InitialOffer != nil {
		if msg := validateOffer(input.InitialOffer); msg != "" {
			badRequest(w, msg)
			return
		}
		// The request fingerprint must remain stable across retries.
		input.InitialOffer.LastCheckedAt = time.Time{}
	}
	row.Offers = nil
	response := struct {
		models.Product
		CreatedOffer *models.Offer `json:"createdOffer,omitempty"`
	}{}
	err := h.db.Transaction(func(tx *gorm.DB) error {
		idempotency, replay, err := beginIdempotentMutation(tx, r, "product_create", input)
		if err != nil {
			return err
		}
		if replay != nil {
			return json.Unmarshal(replay, &response)
		}
		if row.CategoryID != nil {
			var count int64
			if err := tx.Model(&models.Category{}).Where("id = ?", *row.CategoryID).Count(&count).Error; err != nil {
				return err
			}
			if count == 0 {
				return &receiptFlowError{status: http.StatusConflict, code: "category_not_found", message: "Kategorie existiert nicht mehr"}
			}
		}
		if err := tx.Create(&row).Error; err != nil {
			return err
		}
		if input.InitialOffer != nil {
			var supplierCount int64
			if err := tx.Model(&models.Supplier{}).Where("id = ? AND active = true", input.InitialOffer.SupplierID).Count(&supplierCount).Error; err != nil {
				return err
			}
			if supplierCount == 0 {
				return &receiptFlowError{status: http.StatusConflict, code: "supplier_not_found", message: "Aktiver Lieferant existiert nicht mehr"}
			}
			offer := *input.InitialOffer
			offer.ID, offer.ProductID, offer.Supplier = 0, row.ID, nil
			offer.LastCheckedAt = time.Now()
			if err := tx.Create(&offer).Error; err != nil {
				return err
			}
			if err := service.RecordPriceAndEvaluateAlerts(tx, &offer); err != nil {
				return err
			}
			response.CreatedOffer = &offer
		}
		origin := "UI"
		if isMCPMutation(r) {
			origin = "MCP/AI"
		}
		changes, err := json.Marshal(map[string]any{"origin": origin, "after": row})
		if err != nil {
			return err
		}
		user := auth.CurrentUser(r)
		if err := tx.Exec(`INSERT INTO audit_log (user_id,action,entity_type,entity_id,old_values,new_values,ip_address,user_agent)
			VALUES (?, 'product.create', 'procurement_product', ?, NULL, ?::jsonb, ?, ?)`, user.ID, strconv.FormatUint(uint64(row.ID), 10), string(changes), requestIP(r), r.UserAgent()).Error; err != nil {
			return err
		}
		if err := tx.Create(&models.Activity{EntityType: "product", EntityID: row.ID, Action: "created", UserID: user.ID, Username: user.Username, Details: "origin=" + origin + " " + row.Name}).Error; err != nil {
			return err
		}
		if response.CreatedOffer != nil {
			offerChanges, err := json.Marshal(map[string]any{"origin": origin, "after": response.CreatedOffer})
			if err != nil {
				return err
			}
			if err := tx.Exec(`INSERT INTO audit_log (user_id,action,entity_type,entity_id,old_values,new_values,ip_address,user_agent)
				VALUES (?, 'offer.create', 'procurement_offer', ?, NULL, ?::jsonb, ?, ?)`, user.ID, strconv.FormatUint(uint64(response.CreatedOffer.ID), 10), string(offerChanges), requestIP(r), r.UserAgent()).Error; err != nil {
				return err
			}
			if err := tx.Create(&models.Activity{EntityType: "offer", EntityID: response.CreatedOffer.ID, Action: "created", UserID: user.ID, Username: user.Username, Details: "origin=" + origin + " product=" + strconv.FormatUint(uint64(row.ID), 10)}).Error; err != nil {
				return err
			}
		}
		response.Product = row
		return completeIdempotentMutation(tx, idempotency, http.StatusCreated, response)
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
	writeJSON(w, http.StatusCreated, response)
}

func (h *Handler) updateProduct(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var input struct {
		models.Product
		ExpectedUpdatedAt *time.Time `json:"expectedUpdatedAt"`
	}
	if !decode(w, r, &input) {
		return
	}
	if msg := validateProduct(&input.Product); msg != "" {
		badRequest(w, msg)
		return
	}
	var updated models.Product
	err := h.db.Transaction(func(tx *gorm.DB) error {
		idempotency, replay, err := beginIdempotentMutation(tx, r, fmt.Sprintf("product_update:%d", id), input)
		if err != nil {
			return err
		}
		if replay != nil {
			return json.Unmarshal(replay, &updated)
		}
		var previous models.Product
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&previous, id).Error; err != nil {
			return err
		}
		if err := validateExpectedUpdate(previous.UpdatedAt, input.ExpectedUpdatedAt, isMCPMutation(r)); err != nil {
			return err
		}
		if input.CategoryID != nil {
			var categoryCount int64
			if err := tx.Model(&models.Category{}).Where("id = ?", *input.CategoryID).Count(&categoryCount).Error; err != nil {
				return err
			}
			if categoryCount == 0 {
				return &receiptFlowError{status: http.StatusConflict, code: "category_not_found", message: "Kategorie existiert nicht mehr"}
			}
		}
		if previous.Active && !input.Active {
			var openOrders, openRequisitions int64
			if err := tx.Model(&models.PurchaseOrderLine{}).Joins("JOIN proc_purchase_orders po ON po.id = proc_purchase_order_lines.purchase_order_id").Where("proc_purchase_order_lines.product_id = ? AND po.status NOT IN ?", id, []string{"cancelled", "received"}).Count(&openOrders).Error; err != nil {
				return err
			}
			if err := tx.Model(&models.RequisitionLine{}).Joins("JOIN proc_requisitions r ON r.id = proc_requisition_lines.requisition_id").Where("proc_requisition_lines.product_id = ? AND r.status IN ?", id, []string{"draft", "submitted", "approved"}).Count(&openRequisitions).Error; err != nil {
				return err
			}
			if openOrders+openRequisitions > 0 {
				return &receiptFlowError{status: http.StatusConflict, code: "product_active_references", message: "Produkt wird in offenen Bestellungen oder Bedarfen verwendet und kann noch nicht deaktiviert werden"}
			}
		}
		updated = input.Product
		updated.ID, updated.CreatedAt = previous.ID, previous.CreatedAt
		updated.UpdatedAt, updated.Offers, updated.Category = time.Time{}, nil, nil
		if err := tx.Save(&updated).Error; err != nil {
			return err
		}
		origin := "UI"
		if isMCPMutation(r) {
			origin = "MCP/AI"
		}
		beforeJSON, err := json.Marshal(previous)
		if err != nil {
			return err
		}
		changes, err := json.Marshal(map[string]any{"origin": origin, "before": previous, "after": updated})
		if err != nil {
			return err
		}
		auditAction, activityAction := "product.update", "updated"
		if previous.Active && !updated.Active {
			auditAction, activityAction = "product.deactivate", "deactivated"
		} else if !previous.Active && updated.Active {
			auditAction, activityAction = "product.reactivate", "reactivated"
		}
		user := auth.CurrentUser(r)
		if err := tx.Exec(`INSERT INTO audit_log (user_id,action,entity_type,entity_id,old_values,new_values,ip_address,user_agent)
			VALUES (?, ?, 'procurement_product', ?, ?::jsonb, ?::jsonb, ?, ?)`, user.ID, auditAction, strconv.FormatUint(uint64(id), 10), string(beforeJSON), string(changes), requestIP(r), r.UserAgent()).Error; err != nil {
			return err
		}
		if err := tx.Create(&models.Activity{EntityType: "product", EntityID: id, Action: activityAction, UserID: user.ID, Username: user.Username, Details: "origin=" + origin + " " + updated.Name}).Error; err != nil {
			return err
		}
		return completeIdempotentMutation(tx, idempotency, http.StatusOK, updated)
	})
	if err != nil {
		var flowErr *receiptFlowError
		if errors.As(err, &flowErr) || errors.Is(err, gorm.ErrRecordNotFound) {
			writeProcurementFlowError(w, err)
		} else {
			conflictOrServer(w, err)
		}
		return
	}
	writeJSON(w, http.StatusOK, updated)
}

func (h *Handler) deleteProduct(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var references int64
	h.db.Model(&models.RequisitionLine{}).Where("product_id = ?", id).Count(&references)
	if references > 0 {
		badRequest(w, "Artikel wird in Bedarfsmeldungen verwendet und kann nur deaktiviert werden")
		return
	}
	if err := h.db.Transaction(func(tx *gorm.DB) error {
		tx.Where("product_id = ?", id).Delete(&models.PriceAlert{})
		tx.Where("product_id = ?", id).Delete(&models.Offer{})
		return tx.Delete(&models.Product{}, id).Error
	}); err != nil {
		serverError(w, err)
		return
	}
	h.activity(r, "product", id, "deleted", "")
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) listOffers(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var rows []models.Offer
	if err := h.db.Preload("Supplier").Where("product_id = ?", id).Order("active DESC, price_cents").Find(&rows).Error; err != nil {
		serverError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, rows)
}

func validateOffer(row *models.Offer) string {
	if row.SupplierID == 0 || row.PriceCents < 0 {
		return "Lieferant und nicht-negativer Preis sind erforderlich"
	}
	row.SupplierSKU = strings.TrimSpace(row.SupplierSKU)
	if len([]rune(row.SupplierSKU)) > 120 || row.LeadDays < 0 || math.IsNaN(row.MinimumQuantity) || math.IsNaN(row.PackSize) || math.IsInf(row.MinimumQuantity, 0) || math.IsInf(row.PackSize, 0) {
		return "Ungültige Angebotsfelder"
	}
	if row.Currency == "" {
		row.Currency = "EUR"
	}
	row.Currency = strings.ToUpper(row.Currency)
	if len(row.Currency) != 3 || strings.Trim(row.Currency, "ABCDEFGHIJKLMNOPQRSTUVWXYZ") != "" {
		return "Währung muss aus drei Buchstaben bestehen"
	}
	if row.MinimumQuantity < 0 || row.PackSize < 0 {
		return "Mindestmenge und Packgröße dürfen nicht negativ sein"
	}
	if row.MinimumQuantity == 0 {
		row.MinimumQuantity = 1
	}
	if row.PackSize == 0 {
		row.PackSize = 1
	}
	if row.PurchaseURL != "" {
		u, err := url.ParseRequestURI(row.PurchaseURL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || len(row.PurchaseURL) > 2000 {
			return "Einkaufslink muss eine gültige HTTP(S)-URL sein"
		}
	}
	row.LastCheckedAt = time.Now()
	return ""
}

func (h *Handler) createOffer(w http.ResponseWriter, r *http.Request) {
	productID, ok := pathID(w, r)
	if !ok {
		return
	}
	var row models.Offer
	if !decode(w, r, &row) {
		return
	}
	row.ID, row.ProductID = 0, productID
	if msg := validateOffer(&row); msg != "" {
		badRequest(w, msg)
		return
	}
	row.LastCheckedAt = time.Time{}
	err := h.db.Transaction(func(tx *gorm.DB) error {
		idempotency, replay, err := beginIdempotentMutation(tx, r, fmt.Sprintf("offer_create:%d", productID), row)
		if err != nil {
			return err
		}
		if replay != nil {
			return json.Unmarshal(replay, &row)
		}
		if err := validateOfferReferences(tx, row.ProductID, row.SupplierID, true); err != nil {
			return err
		}
		row.LastCheckedAt = time.Now()
		if err := tx.Create(&row).Error; err != nil {
			return err
		}
		if err := service.RecordPriceAndEvaluateAlerts(tx, &row); err != nil {
			return err
		}
		if err := auditOfferMutation(tx, r, "offer.create", nil, &row); err != nil {
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

func (h *Handler) updateOffer(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var input struct {
		models.Offer
		ExpectedUpdatedAt *time.Time `json:"expectedUpdatedAt"`
	}
	if !decode(w, r, &input) {
		return
	}
	if msg := validateOffer(&input.Offer); msg != "" {
		badRequest(w, msg)
		return
	}
	input.LastCheckedAt = time.Time{}
	var updated models.Offer
	err := h.db.Transaction(func(tx *gorm.DB) error {
		idempotency, replay, err := beginIdempotentMutation(tx, r, fmt.Sprintf("offer_update:%d", id), input)
		if err != nil {
			return err
		}
		if replay != nil {
			return json.Unmarshal(replay, &updated)
		}
		var existing models.Offer
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&existing, id).Error; err != nil {
			return err
		}
		if err := validateExpectedUpdate(existing.UpdatedAt, input.ExpectedUpdatedAt, isMCPMutation(r)); err != nil {
			return err
		}
		if err := validateOfferReferences(tx, existing.ProductID, input.SupplierID, input.Active); err != nil {
			return err
		}
		updated = input.Offer
		updated.ID, updated.ProductID, updated.CreatedAt = existing.ID, existing.ProductID, existing.CreatedAt
		updated.UpdatedAt, updated.Supplier = time.Time{}, nil
		updated.LastCheckedAt = time.Now()
		if err := tx.Save(&updated).Error; err != nil {
			return err
		}
		if updated.PriceCents != existing.PriceCents {
			if err := service.RecordPriceAndEvaluateAlerts(tx, &updated); err != nil {
				return err
			}
		}
		action := "offer.update"
		if existing.Active && !updated.Active {
			action = "offer.deactivate"
		} else if !existing.Active && updated.Active {
			action = "offer.reactivate"
		}
		if err := auditOfferMutation(tx, r, action, &existing, &updated); err != nil {
			return err
		}
		return completeIdempotentMutation(tx, idempotency, http.StatusOK, updated)
	})
	if err != nil {
		var flowErr *receiptFlowError
		if errors.As(err, &flowErr) || errors.Is(err, gorm.ErrRecordNotFound) {
			writeProcurementFlowError(w, err)
		} else {
			conflictOrServer(w, err)
		}
		return
	}
	writeJSON(w, http.StatusOK, updated)
}

func validateOfferReferences(tx *gorm.DB, productID, supplierID uint, requireActive bool) error {
	var products, suppliers int64
	productQuery := tx.Model(&models.Product{}).Where("id = ?", productID)
	supplierQuery := tx.Model(&models.Supplier{}).Where("id = ?", supplierID)
	if requireActive {
		productQuery = productQuery.Where("active = true")
		supplierQuery = supplierQuery.Where("active = true")
	}
	if err := productQuery.Count(&products).Error; err != nil {
		return err
	}
	if err := supplierQuery.Count(&suppliers).Error; err != nil {
		return err
	}
	if products == 0 || suppliers == 0 {
		return &receiptFlowError{status: http.StatusConflict, code: "offer_reference_inactive", message: "Aktives Produkt und aktiver Lieferant sind erforderlich"}
	}
	return nil
}

func auditOfferMutation(tx *gorm.DB, r *http.Request, action string, before, after *models.Offer) error {
	origin := "UI"
	if isMCPMutation(r) {
		origin = "MCP/AI"
	}
	beforeJSON, err := json.Marshal(before)
	if err != nil {
		return err
	}
	changes, err := json.Marshal(map[string]any{"origin": origin, "before": before, "after": after})
	if err != nil {
		return err
	}
	user := auth.CurrentUser(r)
	if err := tx.Exec(`INSERT INTO audit_log (user_id,action,entity_type,entity_id,old_values,new_values,ip_address,user_agent)
		VALUES (?, ?, 'procurement_offer', ?, ?::jsonb, ?::jsonb, ?, ?)`, user.ID, action, strconv.FormatUint(uint64(after.ID), 10), string(beforeJSON), string(changes), requestIP(r), r.UserAgent()).Error; err != nil {
		return err
	}
	return tx.Create(&models.Activity{EntityType: "offer", EntityID: after.ID, Action: strings.TrimPrefix(action, "offer."), UserID: user.ID, Username: user.Username, Details: "origin=" + origin + " product=" + strconv.FormatUint(uint64(after.ProductID), 10)}).Error
}

func (h *Handler) deleteOffer(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if err := h.db.Transaction(func(tx *gorm.DB) error {
		tx.Where("offer_id = ?", id).Delete(&models.PriceHistory{})
		return tx.Delete(&models.Offer{}, id).Error
	}); err != nil {
		serverError(w, err)
		return
	}
	h.activity(r, "offer", id, "deleted", "")
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) offerHistory(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var rows []models.PriceHistory
	if err := h.db.Where("offer_id = ?", id).Order("recorded_at DESC").Find(&rows).Error; err != nil {
		serverError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, rows)
}

func (h *Handler) listAlerts(w http.ResponseWriter, r *http.Request) {
	user := auth.CurrentUser(r)
	query := h.db.Preload("Product")
	if !user.IsAdmin {
		query = query.Where("created_by = ?", user.ID)
	}
	if active := r.URL.Query().Get("active"); active != "" {
		query = query.Where("active = ?", active == "true")
	}
	var rows []models.PriceAlert
	if err := query.Order("triggered DESC, created_at DESC").Find(&rows).Error; err != nil {
		serverError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, rows)
}

func (h *Handler) createAlert(w http.ResponseWriter, r *http.Request) {
	var row models.PriceAlert
	if !decode(w, r, &row) {
		return
	}
	if row.ProductID == 0 || row.TargetPriceCents < 0 {
		badRequest(w, "Artikel und Zielpreis sind erforderlich")
		return
	}
	user := auth.CurrentUser(r)
	row.ID, row.CreatedBy, row.CreatedByName = 0, user.ID, user.Username
	if row.Currency == "" {
		row.Currency = "EUR"
	}
	row.Active = true
	if err := h.db.Create(&row).Error; err != nil {
		conflictOrServer(w, err)
		return
	}
	// Evaluate against already-known prices immediately.
	var offer models.Offer
	if err := h.db.Where("product_id = ? AND active = ? AND currency = ? AND price_cents <= ?", row.ProductID, true, row.Currency, row.TargetPriceCents).Order("price_cents").First(&offer).Error; err == nil {
		now, price, offerID := time.Now(), offer.PriceCents, offer.ID
		row.Triggered, row.TriggeredAt, row.TriggeredPriceCents, row.TriggeredOfferID = true, &now, &price, &offerID
		h.db.Save(&row)
	}
	h.activity(r, "price_alert", row.ID, "created", fmt.Sprintf("target=%d", row.TargetPriceCents))
	h.db.Preload("Product").First(&row, row.ID)
	writeJSON(w, http.StatusCreated, row)
}

func (h *Handler) updateAlert(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	user := auth.CurrentUser(r)
	var row models.PriceAlert
	if err := h.db.First(&row, id).Error; err != nil {
		notFound(w)
		return
	}
	if !user.IsAdmin && row.CreatedBy != user.ID {
		forbidden(w)
		return
	}
	var input struct {
		TargetPriceCents int64 `json:"targetPriceCents"`
		Active           bool  `json:"active"`
		Triggered        bool  `json:"triggered"`
	}
	if !decode(w, r, &input) {
		return
	}
	if input.TargetPriceCents < 0 {
		badRequest(w, "Zielpreis darf nicht negativ sein")
		return
	}
	row.TargetPriceCents, row.Active = input.TargetPriceCents, input.Active
	if !input.Triggered {
		row.Triggered, row.TriggeredAt, row.TriggeredPriceCents, row.TriggeredOfferID = false, nil, nil, nil
	}
	if err := h.db.Save(&row).Error; err != nil {
		serverError(w, err)
		return
	}
	h.activity(r, "price_alert", row.ID, "updated", "")
	writeJSON(w, http.StatusOK, row)
}

func (h *Handler) deleteAlert(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	user := auth.CurrentUser(r)
	var row models.PriceAlert
	if err := h.db.First(&row, id).Error; err != nil {
		notFound(w)
		return
	}
	if !user.IsAdmin && row.CreatedBy != user.ID {
		forbidden(w)
		return
	}
	h.db.Delete(&row)
	h.activity(r, "price_alert", id, "deleted", "")
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) listRequisitions(w http.ResponseWriter, r *http.Request) {
	user := auth.CurrentUser(r)
	query := h.db.Preload("Lines").Preload("Lines.Product")
	if !user.IsAdmin {
		query = query.Where("requester_id = ?", user.ID)
	}
	if status := r.URL.Query().Get("status"); status != "" {
		query = query.Where("status = ?", status)
	}
	var rows []models.Requisition
	if err := query.Order("created_at DESC").Find(&rows).Error; err != nil {
		serverError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, rows)
}

func (h *Handler) getRequisition(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	user := auth.CurrentUser(r)
	var row models.Requisition
	if err := h.db.Preload("Lines").Preload("Lines.Product").First(&row, id).Error; err != nil {
		notFound(w)
		return
	}
	if !user.IsAdmin && row.RequesterID != user.ID {
		forbidden(w)
		return
	}
	writeJSON(w, http.StatusOK, row)
}

func validateRequisition(row *models.Requisition) string {
	row.Title = strings.TrimSpace(row.Title)
	if row.Title == "" || len(row.Lines) == 0 {
		return "Titel und mindestens eine Position sind erforderlich"
	}
	for i := range row.Lines {
		if row.Lines[i].Description == "" || row.Lines[i].Quantity <= 0 {
			return "Jede Position benötigt Beschreibung und positive Menge"
		}
		if row.Lines[i].Unit == "" {
			row.Lines[i].Unit = "Stk."
		}
	}
	row.EstimatedTotalCents = service.RequisitionTotal(row.Lines)
	return ""
}

func (h *Handler) createRequisition(w http.ResponseWriter, r *http.Request) {
	var row models.Requisition
	if !decode(w, r, &row) {
		return
	}
	if msg := validateRequisition(&row); msg != "" {
		badRequest(w, msg)
		return
	}
	user := auth.CurrentUser(r)
	row.ID, row.Number, row.Status = 0, nextNumber("BAN"), "draft"
	row.RequesterID, row.RequesterName = user.ID, user.Username
	for i := range row.Lines {
		row.Lines[i].ID, row.Lines[i].RequisitionID = 0, 0
	}
	if err := h.db.Create(&row).Error; err != nil {
		conflictOrServer(w, err)
		return
	}
	h.activity(r, "requisition", row.ID, "created", row.Number)
	writeJSON(w, http.StatusCreated, row)
}

func (h *Handler) updateRequisition(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	user := auth.CurrentUser(r)
	var existing models.Requisition
	if err := h.db.First(&existing, id).Error; err != nil {
		notFound(w)
		return
	}
	if existing.Status != "draft" || (!user.IsAdmin && existing.RequesterID != user.ID) {
		forbidden(w)
		return
	}
	var input models.Requisition
	if !decode(w, r, &input) {
		return
	}
	if msg := validateRequisition(&input); msg != "" {
		badRequest(w, msg)
		return
	}
	err := h.db.Transaction(func(tx *gorm.DB) error {
		existing.Title, existing.CostCenter, existing.Justification, existing.NeededBy, existing.EstimatedTotalCents = input.Title, input.CostCenter, input.Justification, input.NeededBy, input.EstimatedTotalCents
		if err := tx.Save(&existing).Error; err != nil {
			return err
		}
		if err := tx.Where("requisition_id = ?", id).Delete(&models.RequisitionLine{}).Error; err != nil {
			return err
		}
		for i := range input.Lines {
			input.Lines[i].ID, input.Lines[i].RequisitionID = 0, id
		}
		return tx.Create(&input.Lines).Error
	})
	if err != nil {
		serverError(w, err)
		return
	}
	h.activity(r, "requisition", id, "updated", existing.Number)
	h.getRequisition(w, r)
}

func (h *Handler) submitRequisition(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	user := auth.CurrentUser(r)
	var row models.Requisition
	if err := h.db.First(&row, id).Error; err != nil {
		notFound(w)
		return
	}
	if row.Status != "draft" || (!user.IsAdmin && row.RequesterID != user.ID) {
		badRequest(w, "Nur eigene Entwürfe können eingereicht werden")
		return
	}
	now := time.Now()
	row.Status, row.SubmittedAt = "submitted", &now
	if err := h.db.Save(&row).Error; err != nil {
		serverError(w, err)
		return
	}
	h.activity(r, "requisition", id, "submitted", row.Number)
	writeJSON(w, http.StatusOK, row)
}

func (h *Handler) decideRequisition(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var input struct {
		Decision          string     `json:"decision"`
		Note              string     `json:"note"`
		ExpectedUpdatedAt *time.Time `json:"expectedUpdatedAt"`
	}
	if !decode(w, r, &input) {
		return
	}
	if input.Decision != "approved" && input.Decision != "rejected" && input.Decision != "returned" {
		badRequest(w, "Entscheidung muss approved, rejected oder returned sein")
		return
	}
	if input.Decision == "returned" && strings.TrimSpace(input.Note) == "" {
		badRequest(w, "Für die Rückgabe ist eine Begründung erforderlich")
		return
	}
	user := auth.CurrentUser(r)
	var row models.Requisition
	replayed := false
	err := h.db.Transaction(func(tx *gorm.DB) error {
		idempotency, replay, err := beginIdempotentMutation(tx, r, "requisition_decision", map[string]any{"id": id, "input": input})
		if err != nil {
			return err
		}
		if replay != nil {
			replayed = true
			return json.Unmarshal(replay, &row)
		}
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&row, id).Error; err != nil {
			return err
		}
		if row.Status != "submitted" {
			return &receiptFlowError{status: http.StatusConflict, code: "requisition_not_submitted", message: "Bedarf ist nicht zur Entscheidung eingereicht"}
		}
		if row.RequesterID == user.ID {
			return &receiptFlowError{status: http.StatusForbidden, code: "separation_of_duties", message: "Anfordernde dürfen den eigenen Bedarf nicht freigeben oder ablehnen"}
		}
		if versionErr := validateExpectedUpdate(row.UpdatedAt, input.ExpectedUpdatedAt, isMCPMutation(r)); versionErr != nil {
			return versionErr
		}
		now := time.Now()
		row.Status, row.ApprovedBy, row.ApprovedByName, row.DecisionNote, row.DecidedAt = input.Decision, &user.ID, user.Username, strings.TrimSpace(input.Note), &now
		if err := tx.Save(&row).Error; err != nil {
			return err
		}
		return completeIdempotentMutation(tx, idempotency, http.StatusOK, row)
	})
	if err != nil {
		writeProcurementFlowError(w, err)
		return
	}
	if !replayed {
		h.activity(r, "requisition", id, input.Decision, fmt.Sprintf("from=submitted to=%s number=%s", input.Decision, row.Number))
	}
	writeJSON(w, http.StatusOK, row)
}

func (h *Handler) convertRequisition(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var input struct {
		SupplierID       uint       `json:"supplierId"`
		ExpectedDelivery *time.Time `json:"expectedDelivery"`
	}
	if !decode(w, r, &input) {
		return
	}
	if input.SupplierID == 0 {
		badRequest(w, "Lieferant ist erforderlich")
		return
	}
	var req models.Requisition
	if err := h.db.Preload("Lines").First(&req, id).Error; err != nil {
		notFound(w)
		return
	}
	if req.Status != "approved" {
		badRequest(w, "Nur freigegebene Bedarfe können bestellt werden")
		return
	}
	user := auth.CurrentUser(r)
	order := models.PurchaseOrder{Number: nextNumber("PO"), SupplierID: input.SupplierID, RequisitionID: &req.ID, Status: "draft", Currency: "EUR", OrderedBy: user.ID, OrderedByName: user.Username, ExpectedDelivery: input.ExpectedDelivery}
	for _, line := range req.Lines {
		price, link := line.EstimatedPriceCents, line.PurchaseURL
		if line.ProductID != nil {
			var offer models.Offer
			if err := h.db.Where("product_id = ? AND supplier_id = ? AND active = ?", *line.ProductID, input.SupplierID, true).Order("price_cents").First(&offer).Error; err == nil {
				price, link = offer.PriceCents, offer.PurchaseURL
			}
		}
		order.Lines = append(order.Lines, models.PurchaseOrderLine{ProductID: line.ProductID, Description: line.Description, Quantity: line.Quantity, Unit: line.Unit, UnitPriceCents: price, PurchaseURL: link})
	}
	order.TotalCents = service.PurchaseOrderTotal(order.Lines)
	err := h.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&order).Error; err != nil {
			return err
		}
		req.Status = "ordered"
		return tx.Save(&req).Error
	})
	if err != nil {
		conflictOrServer(w, err)
		return
	}
	h.activity(r, "purchase_order", order.ID, "created_from_requisition", order.Number)
	writeJSON(w, http.StatusCreated, order)
}

func (h *Handler) listOrders(w http.ResponseWriter, r *http.Request) {
	query := h.db.Preload("Supplier").Preload("Lines")
	if status := r.URL.Query().Get("status"); status != "" {
		query = query.Where("status = ?", status)
	}
	if supplier, ok := parseUint(r.URL.Query().Get("supplierId")); ok {
		query = query.Where("supplier_id = ?", supplier)
	}
	var rows []models.PurchaseOrder
	if err := query.Order("created_at DESC").Find(&rows).Error; err != nil {
		serverError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, rows)
}

func (h *Handler) getOrder(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var row models.PurchaseOrder
	if err := h.db.Preload("Supplier").Preload("Lines").Preload("Lines.Product").First(&row, id).Error; err != nil {
		notFound(w)
		return
	}
	products := make([]models.Product, 0, len(row.Lines))
	for i := range row.Lines {
		if row.Lines[i].Product != nil {
			products = append(products, *row.Lines[i].Product)
		}
	}
	h.hydrateWarehouseLinks(products)
	productsByID := make(map[uint]models.Product, len(products))
	for _, product := range products {
		productsByID[product.ID] = product
	}
	for i := range row.Lines {
		if row.Lines[i].Product != nil {
			product := productsByID[row.Lines[i].Product.ID]
			row.Lines[i].Product = &product
		}
	}
	writeJSON(w, http.StatusOK, row)
}

func validateOrder(row *models.PurchaseOrder) string {
	if row.SupplierID == 0 || len(row.Lines) == 0 {
		return "Lieferant und mindestens eine Position sind erforderlich"
	}
	if row.Status == "" {
		row.Status = "draft"
	}
	if !map[string]bool{"draft": true, "sent": true, "confirmed": true}[row.Status] {
		return "Ungültiger initialer Bestellstatus"
	}
	row.Currency = strings.ToUpper(strings.TrimSpace(row.Currency))
	if row.Currency == "" {
		row.Currency = "EUR"
	}
	if !map[string]bool{"EUR": true, "CHF": true, "USD": true, "GBP": true}[row.Currency] {
		return "Ungültige Währung"
	}
	row.SupplierOrderNumber = normalizeSupplierOrderNumber(row.SupplierOrderNumber)
	if len([]rune(row.SupplierOrderNumber)) > 120 {
		return "Lieferanten-Bestellnummer darf maximal 120 Zeichen lang sein"
	}
	for i := range row.Lines {
		if row.Lines[i].Description == "" || row.Lines[i].Quantity <= 0 || row.Lines[i].UnitPriceCents < 0 {
			return "Ungültige Bestellposition"
		}
		if row.Lines[i].Unit == "" {
			row.Lines[i].Unit = "Stk."
		}
	}
	row.TotalCents = service.PurchaseOrderTotal(row.Lines)
	return ""
}

func normalizeSupplierOrderNumber(value string) string {
	return strings.TrimSpace(value)
}

func (h *Handler) createOrder(w http.ResponseWriter, r *http.Request) {
	var row models.PurchaseOrder
	if !decode(w, r, &row) {
		return
	}
	if msg := validateOrder(&row); msg != "" {
		badRequest(w, msg)
		return
	}
	user := auth.CurrentUser(r)
	row.ID, row.Number, row.OrderedBy, row.OrderedByName = 0, nextNumber("PO"), user.ID, user.Username
	if row.Status != "draft" && row.OrderDate == nil {
		now := time.Now()
		row.OrderDate = &now
	}
	for i := range row.Lines {
		row.Lines[i].ID, row.Lines[i].PurchaseOrderID = 0, 0
	}
	if err := h.db.Create(&row).Error; err != nil {
		conflictOrServer(w, err)
		return
	}
	h.activity(r, "purchase_order", row.ID, "created", row.Number)
	writeJSON(w, http.StatusCreated, row)
}

func (h *Handler) updateOrder(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var row models.PurchaseOrder
	if err := h.db.First(&row, id).Error; err != nil {
		notFound(w)
		return
	}
	var input struct {
		Status              string     `json:"status"`
		SupplierOrderNumber string     `json:"supplierOrderNumber"`
		ExpectedDelivery    *time.Time `json:"expectedDelivery"`
		Notes               string     `json:"notes"`
	}
	if !decode(w, r, &input) {
		return
	}
	allowed := map[string]bool{"draft": true, "sent": true, "confirmed": true, "partially_received": true, "received": true, "cancelled": true, "submission_unknown": true}
	if !allowed[input.Status] {
		badRequest(w, "Ungültiger Bestellstatus")
		return
	}
	row.Status, row.SupplierOrderNumber, row.ExpectedDelivery, row.Notes = input.Status, normalizeSupplierOrderNumber(input.SupplierOrderNumber), input.ExpectedDelivery, input.Notes
	if input.Status == "sent" && row.OrderDate == nil {
		now := time.Now()
		row.OrderDate = &now
	}
	if err := h.db.Save(&row).Error; err != nil {
		serverError(w, err)
		return
	}
	h.activity(r, "purchase_order", row.ID, "updated", fmt.Sprintf("status=%s supplier_order_number=%s", input.Status, row.SupplierOrderNumber))
	writeJSON(w, http.StatusOK, row)
}

type adamHallOrderData struct {
	Order                 models.PurchaseOrder
	Items                 []scraper.AdamHallItem
	ProductNumberByLineID map[uint]string
}

func (h *Handler) previewAdamHallOrder(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	data, message := h.adamHallOrderData(id)
	if message != "" {
		badRequest(w, message)
		return
	}
	cart, err := h.scraper.AdamHallCart(r.Context(), data.Items)
	if err != nil {
		badRequest(w, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, cart)
}

func (h *Handler) placeAdamHallOrder(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	data, message := h.adamHallOrderData(id)
	if message != "" {
		badRequest(w, message)
		return
	}
	claimed := h.db.Model(&models.PurchaseOrder{}).
		Where("id = ? AND status = ? AND COALESCE(supplier_order_number, '') = ''", id, "draft").
		Update("status", "submitting")
	if claimed.Error != nil {
		serverError(w, claimed.Error)
		return
	}
	if claimed.RowsAffected != 1 {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "Bestellung wurde bereits übertragen oder wird gerade übertragen"})
		return
	}

	comment := strings.TrimSpace(strings.Join([]string{data.Order.Number, data.Order.Notes}, " · "))
	remoteOrder, err := h.scraper.PlaceAdamHallOrder(r.Context(), data.Items, comment)
	if err != nil {
		status := "draft"
		if scraper.IsAdamHallSubmissionUncertain(err) {
			status = "submission_unknown"
		}
		_ = h.db.Model(&models.PurchaseOrder{}).Where("id = ? AND status = ?", id, "submitting").Update("status", status).Error
		h.activity(r, "purchase_order", id, "adam_hall_order_failed", data.Order.Number)
		badRequest(w, err.Error())
		return
	}

	unitPriceByProductNumber := make(map[string]int64, len(remoteOrder.Cart.Lines))
	for _, line := range remoteOrder.Cart.Lines {
		unitPriceByProductNumber[strings.ToUpper(strings.TrimSpace(line.ProductNumber))] = line.UnitPriceCents
	}
	now := time.Now()
	updates := map[string]any{
		"status": "sent", "supplier_order_number": remoteOrder.OrderNumber,
		"order_date": now, "total_cents": remoteOrder.Cart.TotalCents, "currency": remoteOrder.Cart.Currency,
	}
	result := h.db.Model(&models.PurchaseOrder{}).Where("id = ? AND status = ?", id, "submitting").Updates(updates)
	if result.Error != nil || result.RowsAffected != 1 {
		// Once Adam Hall accepted an order, preserve its external reference even
		// if a concurrent local state change happened.
		fallback := h.db.Model(&models.PurchaseOrder{}).Where("id = ?", id).Updates(updates)
		if fallback.Error != nil || fallback.RowsAffected != 1 {
			serverError(w, errors.New("Adam-Hall-Bestellnummer konnte lokal nicht gesichert werden"))
			return
		}
	}
	for lineID, productNumber := range data.ProductNumberByLineID {
		if price, exists := unitPriceByProductNumber[productNumber]; exists {
			if err := h.db.Model(&models.PurchaseOrderLine{}).Where("id = ? AND purchase_order_id = ?", lineID, id).Update("unit_price_cents", price).Error; err != nil {
				h.activity(r, "purchase_order", id, "adam_hall_price_sync_failed", remoteOrder.OrderNumber)
			}
		}
	}
	h.activity(r, "purchase_order", id, "ordered_at_adam_hall", remoteOrder.OrderNumber)

	var updated models.PurchaseOrder
	if err := h.db.Preload("Supplier").Preload("Lines").Preload("Lines.Product").First(&updated, id).Error; err != nil {
		serverError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"order": updated, "cart": remoteOrder.Cart})
}

func (h *Handler) adamHallOrderData(id uint) (adamHallOrderData, string) {
	var order models.PurchaseOrder
	if err := h.db.Preload("Supplier").Preload("Lines").Preload("Lines.Product").First(&order, id).Error; err != nil {
		return adamHallOrderData{}, "Bestellung wurde nicht gefunden"
	}
	if order.Status != "draft" {
		return adamHallOrderData{}, "Nur Bestellungen im Entwurf können an Adam Hall übertragen werden"
	}
	if strings.TrimSpace(order.SupplierOrderNumber) != "" {
		return adamHallOrderData{}, "Bestellung besitzt bereits eine Lieferanten-Bestellnummer"
	}
	if order.Supplier == nil || !isAdamHallSupplier(*order.Supplier) {
		return adamHallOrderData{}, "Der gewählte Lieferant ist kein Adam-Hall-Konto"
	}
	items := make([]scraper.AdamHallItem, 0, len(order.Lines))
	productNumberByLineID := make(map[uint]string, len(order.Lines))
	for _, line := range order.Lines {
		if line.ProductID == nil || line.Product == nil {
			return adamHallOrderData{}, fmt.Sprintf("Position %q ist keinem Katalogartikel zugeordnet", line.Description)
		}
		productNumber := strings.TrimSpace(line.Product.SKU)
		var offer models.Offer
		if err := h.db.Where("product_id = ? AND supplier_id = ? AND active = ?", *line.ProductID, order.SupplierID, true).
			Order("price_cents").First(&offer).Error; err == nil && strings.TrimSpace(offer.SupplierSKU) != "" {
			productNumber = strings.TrimSpace(offer.SupplierSKU)
		}
		if productNumber == "" {
			return adamHallOrderData{}, fmt.Sprintf("Position %q besitzt keine Adam-Hall-Artikelnummer", line.Description)
		}
		if line.Quantity <= 0 || math.Trunc(line.Quantity) != line.Quantity {
			return adamHallOrderData{}, fmt.Sprintf("Position %q benötigt für Adam Hall eine ganzzahlige Menge", line.Description)
		}
		normalized := strings.ToUpper(productNumber)
		items = append(items, scraper.AdamHallItem{ProductNumber: normalized, Quantity: int(line.Quantity)})
		productNumberByLineID[line.ID] = normalized
	}
	if len(items) == 0 {
		return adamHallOrderData{}, "Bestellung enthält keine Positionen"
	}
	return adamHallOrderData{Order: order, Items: items, ProductNumberByLineID: productNumberByLineID}, ""
}

func isAdamHallSupplier(supplier models.Supplier) bool {
	compact := strings.NewReplacer(" ", "", "-", "", "_", "").Replace(strings.ToLower(supplier.Name + supplier.Code))
	if strings.Contains(compact, "adamhall") {
		return true
	}
	website, err := url.Parse(strings.TrimSpace(supplier.Website))
	if err != nil {
		return false
	}
	host := strings.ToLower(website.Hostname())
	return host == "adamhall.com" || strings.HasSuffix(host, ".adamhall.com")
}

type receiptFlowError struct {
	status  int
	code    string
	message string
}

func (err *receiptFlowError) Error() string { return err.message }

func isMCPMutation(r *http.Request) bool {
	return strings.EqualFold(strings.TrimSpace(r.Header.Get("X-Cores-Origin")), "MCP/AI")
}

func requestIP(r *http.Request) string {
	ip := r.RemoteAddr
	if host, _, err := net.SplitHostPort(ip); err == nil {
		ip = host
	}
	if len(ip) > 45 {
		ip = ip[:45]
	}
	return ip
}

func validateExpectedUpdate(actual time.Time, expected *time.Time, required bool) *receiptFlowError {
	if expected == nil {
		if required {
			return &receiptFlowError{status: http.StatusPreconditionRequired, code: "version_required", message: "expectedUpdatedAt ist für MCP/KI-Änderungen erforderlich"}
		}
		return nil
	}
	if !actual.Equal(*expected) {
		return &receiptFlowError{status: http.StatusConflict, code: "stale_version", message: "Datensatz wurde zwischen Vorschau und Ausführung geändert"}
	}
	return nil
}

func writeProcurementFlowError(w http.ResponseWriter, err error) {
	var flowErr *receiptFlowError
	if errors.As(err, &flowErr) {
		writeJSON(w, flowErr.status, map[string]string{"error": flowErr.message, "code": flowErr.code})
		return
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		notFound(w)
		return
	}
	serverError(w, err)
}

var validIdempotencyKey = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{7,127}$`)

func beginIdempotentMutation(tx *gorm.DB, r *http.Request, operation string, input any) (*models.IdempotencyRecord, json.RawMessage, error) {
	if !isMCPMutation(r) {
		return nil, nil, nil
	}
	key := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if !validIdempotencyKey.MatchString(key) {
		return nil, nil, &receiptFlowError{status: http.StatusPreconditionRequired, code: "idempotency_key_required", message: "Ein gültiger Idempotency-Key ist für MCP/KI-Änderungen erforderlich"}
	}
	payload, err := json.Marshal(input)
	if err != nil {
		return nil, nil, err
	}
	keyDigest := sha256.Sum256([]byte(key))
	requestDigest := sha256.Sum256(payload)
	user := auth.CurrentUser(r)
	record := models.IdempotencyRecord{
		UserID: user.ID, Operation: operation, KeyHash: hex.EncodeToString(keyDigest[:]),
		RequestHash: hex.EncodeToString(requestDigest[:]), Response: json.RawMessage(`{}`), StatusCode: http.StatusOK,
	}
	result := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&record)
	if result.Error != nil {
		return nil, nil, result.Error
	}
	if result.RowsAffected == 1 {
		return &record, nil, nil
	}
	var existing models.IdempotencyRecord
	if err := tx.Where("user_id = ? AND operation = ? AND key_hash = ?", user.ID, operation, record.KeyHash).First(&existing).Error; err != nil {
		return nil, nil, err
	}
	if existing.RequestHash != record.RequestHash {
		return nil, nil, &receiptFlowError{status: http.StatusConflict, code: "idempotency_payload_conflict", message: "Der Idempotency-Key wurde bereits mit einer anderen Payload verwendet"}
	}
	return &existing, existing.Response, nil
}

func completeIdempotentMutation(tx *gorm.DB, record *models.IdempotencyRecord, status int, response any) error {
	if record == nil {
		return nil
	}
	payload, err := json.Marshal(response)
	if err != nil {
		return err
	}
	return tx.Model(record).Updates(map[string]any{"response": json.RawMessage(payload), "status_code": status}).Error
}

func validateWarehouseReceipt(trackingMode string, quantity float64) *receiptFlowError {
	switch trackingMode {
	case "quantity", "none":
		return nil
	case "individual":
		if math.Trunc(quantity) != quantity {
			return &receiptFlowError{status: http.StatusBadRequest, code: "individual_quantity_required", message: "Für Einzelverfolgung muss die Eingangsmenge ganzzahlig sein"}
		}
		return nil
	default:
		return &receiptFlowError{status: http.StatusConflict, code: "warehouse_tracking_invalid", message: "Die Bestandsführung des Warehouse-Produkts ist ungültig"}
	}
}

func normalizeReceiptSerials(values []string, quantity int, required bool) ([]string, *receiptFlowError) {
	if len(values) == 0 && !required {
		return nil, nil
	}
	if quantity <= 0 || len(values) != quantity {
		return nil, &receiptFlowError{status: http.StatusBadRequest, code: "serial_count_mismatch", message: "Für jedes einzeln verfolgte Gerät ist genau eine Seriennummer erforderlich"}
	}
	result := make([]string, 0, len(values))
	seen := make(map[string]bool, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		key := strings.ToLower(value)
		if value == "" || seen[key] {
			return nil, &receiptFlowError{status: http.StatusBadRequest, code: "serial_invalid", message: "Seriennummern dürfen nicht leer oder doppelt sein"}
		}
		seen[key] = true
		result = append(result, value)
	}
	return result, nil
}

func (h *Handler) receiveOrder(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var input struct {
		LineID            uint       `json:"lineId"`
		Quantity          float64    `json:"quantity"`
		Note              string     `json:"note"`
		ExpectedUpdatedAt *time.Time `json:"expectedUpdatedAt"`
		SerialNumbers     []string   `json:"serialNumbers"`
		TargetZoneID      *int64     `json:"targetZoneId"`
		AllowOverdelivery bool       `json:"allowOverdelivery"`
	}
	if !decode(w, r, &input) {
		return
	}
	if input.LineID == 0 || input.Quantity <= 0 {
		badRequest(w, "Position und positive Menge sind erforderlich")
		return
	}
	user := auth.CurrentUser(r)
	var line models.PurchaseOrderLine
	var order models.PurchaseOrder
	replayed := false
	receipt := models.Receipt{PurchaseOrderID: id, PurchaseOrderLineID: input.LineID, Quantity: input.Quantity, ReceivedBy: user.ID, ReceivedByName: user.Username, Note: input.Note, ReceivedAt: time.Now()}
	err := h.db.Transaction(func(tx *gorm.DB) error {
		idempotency, replay, err := beginIdempotentMutation(tx, r, "purchase_order_receipt", map[string]any{"id": id, "input": input})
		if err != nil {
			return err
		}
		if replay != nil {
			replayed = true
			return json.Unmarshal(replay, &receipt)
		}
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&order, id).Error; err != nil {
			return err
		}
		if order.Status != "sent" && order.Status != "confirmed" && order.Status != "partially_received" {
			return &receiptFlowError{status: http.StatusConflict, code: "order_not_receivable", message: "Wareneingang ist nur für versendete oder bestätigte Bestellungen zulässig"}
		}
		if versionErr := validateExpectedUpdate(order.UpdatedAt, input.ExpectedUpdatedAt, isMCPMutation(r)); versionErr != nil {
			return versionErr
		}
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND purchase_order_id = ?", input.LineID, id).First(&line).Error; err != nil {
			return err
		}
		if line.ReceivedQuantity+input.Quantity > line.Quantity && !input.AllowOverdelivery {
			return &receiptFlowError{status: http.StatusBadRequest, code: "ordered_quantity_exceeded", message: "Wareneingang überschreitet Bestellmenge"}
		}
		if line.ProductID != nil {
			var warehouseProductID int64
			var trackingMode string
			scanErr := tx.Raw(`
				SELECT p.productID,p.tracking_mode
				FROM core_product_links cpl
				JOIN products p ON p.productID=cpl.warehouse_product_id
				WHERE cpl.procurement_product_id=? AND p.lifecycle_status='active'
				FOR UPDATE OF p
			`, *line.ProductID).Row().Scan(&warehouseProductID, &trackingMode)
			if errors.Is(scanErr, sql.ErrNoRows) {
				return &receiptFlowError{status: http.StatusConflict, code: "warehouse_product_required", message: "Produkt zuerst mit WarehouseCore verknüpfen oder dort neu anlegen"}
			}
			if scanErr != nil {
				return scanErr
			}
			if validationErr := validateWarehouseReceipt(trackingMode, input.Quantity); validationErr != nil {
				return validationErr
			}
			var targetZone any
			if input.TargetZoneID != nil {
				var zone struct {
					ID                int64
					Code              string
					IsActive          bool
					IsStorable        bool
					OperationalStatus string
				}
				if err := tx.Raw(`SELECT zone_id AS id,code,is_active,is_storable,operational_status FROM storage_zones WHERE zone_id=? FOR UPDATE`, *input.TargetZoneID).Scan(&zone).Error; err != nil {
					return err
				}
				if zone.ID == 0 || !zone.IsActive || !zone.IsStorable || zone.OperationalStatus != "available" {
					return &receiptFlowError{status: http.StatusConflict, code: "target_zone_unavailable", message: "Der Ziel-Lagerplatz ist nicht aktiv, verfügbar und einlagerungsfähig"}
				}
				targetZone = zone.ID
			}
			receipt.WarehouseProductID = &warehouseProductID
			receipt.WarehouseTrackingMode = trackingMode
			switch trackingMode {
			case "quantity":
				if err := tx.Exec(`
					INSERT INTO product_locations(product_id,zone_id,quantity,updated_at)
					VALUES(?,?,?,CURRENT_TIMESTAMP)
					ON CONFLICT(product_id,zone_id) DO UPDATE
					SET quantity=product_locations.quantity+EXCLUDED.quantity,updated_at=CURRENT_TIMESTAMP
				`, warehouseProductID, targetZone, input.Quantity).Error; err != nil {
					return err
				}
				receipt.WarehouseQuantityApplied = input.Quantity
				if err := tx.Raw("SELECT COALESCE(stock_quantity,0) FROM products WHERE productID=?", warehouseProductID).Scan(&receipt.WarehouseStockAfter).Error; err != nil {
					return err
				}
			case "individual":
				serials, validationErr := normalizeReceiptSerials(input.SerialNumbers, int(input.Quantity), isMCPMutation(r))
				if validationErr != nil {
					return validationErr
				}
				for index := 0; index < int(input.Quantity); index++ {
					var deviceID string
					var serial any
					if index < len(serials) {
						serial = serials[index]
						if err := lockAndValidateReceiptSerial(tx, serials[index]); err != nil {
							return err
						}
					}
					if err := tx.Raw(`INSERT INTO devices(productID,serialnumber,status,condition_status,current_location) VALUES(?,?,'location_unknown','available','location_unknown') RETURNING deviceID`, warehouseProductID, serial).Scan(&deviceID).Error; err != nil {
						return err
					}
					receipt.CreatedDeviceIDs = append(receipt.CreatedDeviceIDs, deviceID)
				}
				receipt.WarehouseQuantityApplied = input.Quantity
				if err := tx.Raw("SELECT COUNT(*) FROM devices WHERE productID=?", warehouseProductID).Scan(&receipt.WarehouseDeviceCountAfter).Error; err != nil {
					return err
				}
			}
			var putawayTaskID int64
			if err := tx.Raw(`INSERT INTO warehouse_tasks(task_type,status,priority,to_zone_id,product_id,quantity,notes) VALUES('putaway','open',70,?,?,?,?) RETURNING task_id`, input.TargetZoneID, warehouseProductID, input.Quantity, fmt.Sprintf("Wareneingang Bestellung %d Position %d", id, line.ID)).Scan(&putawayTaskID).Error; err != nil {
				return err
			}
			receipt.PutawayTaskID = &putawayTaskID
		}
		receipt.PurchaseOrderLineID = line.ID
		line.ReceivedQuantity += input.Quantity
		if err := tx.Save(&line).Error; err != nil {
			return err
		}
		if err := tx.Create(&receipt).Error; err != nil {
			return err
		}
		var remaining int64
		tx.Model(&models.PurchaseOrderLine{}).Where("purchase_order_id = ? AND received_quantity < quantity", id).Count(&remaining)
		status := "received"
		if remaining > 0 {
			status = "partially_received"
		}
		if err := tx.Model(&order).Update("status", status).Error; err != nil {
			return err
		}
		return completeIdempotentMutation(tx, idempotency, http.StatusCreated, receipt)
	})
	if err != nil {
		writeProcurementFlowError(w, err)
		return
	}
	details := fmt.Sprintf("line=%d quantity=%g", line.ID, input.Quantity)
	if input.AllowOverdelivery && line.ReceivedQuantity > line.Quantity {
		details += fmt.Sprintf(" overdelivery=%g", line.ReceivedQuantity-line.Quantity)
	}
	if receipt.WarehouseProductID != nil {
		details += fmt.Sprintf(" warehouse_product=%d tracking=%s applied=%g", *receipt.WarehouseProductID, receipt.WarehouseTrackingMode, receipt.WarehouseQuantityApplied)
	}
	if receipt.PutawayTaskID != nil {
		details += fmt.Sprintf(" putaway_task=%d devices_created=%d", *receipt.PutawayTaskID, len(receipt.CreatedDeviceIDs))
	}
	if !replayed {
		h.activity(r, "purchase_order", id, "goods_received", details)
	}
	writeJSON(w, http.StatusCreated, receipt)
}

func lockAndValidateReceiptSerial(tx *gorm.DB, serial string) error {
	if err := tx.Exec(`SELECT pg_advisory_xact_lock(hashtextextended(LOWER(?),0))`, serial).Error; err != nil {
		return err
	}
	var exists bool
	if err := tx.Raw(`SELECT EXISTS(SELECT 1 FROM devices WHERE LOWER(TRIM(serialnumber))=LOWER(?))`, serial).Scan(&exists).Error; err != nil {
		return err
	}
	if exists {
		return &receiptFlowError{status: http.StatusConflict, code: "device_serial_conflict", message: "Eine Seriennummer ist bereits vorhanden"}
	}
	return nil
}

func (h *Handler) listActivity(w http.ResponseWriter, r *http.Request) {
	limit := 50
	if value, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && value > 0 && value <= 200 {
		limit = value
	}
	var rows []models.Activity
	if err := h.db.Order("created_at DESC").Limit(limit).Find(&rows).Error; err != nil {
		serverError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, rows)
}

func (h *Handler) exportSpend(w http.ResponseWriter, r *http.Request) {
	var rows []models.PurchaseOrder
	if err := h.db.Preload("Supplier").Where("status <> ?", "cancelled").Order("created_at DESC").Find(&rows).Error; err != nil {
		serverError(w, err)
		return
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="procurement-spend.csv"`)
	writer := csv.NewWriter(w)
	defer writer.Flush()
	_ = writer.Write([]string{"Bestellnummer", "Lieferant", "Status", "Datum", "Betrag (Cent)", "Währung", "Kostenstelle/Bedarf"})
	for _, row := range rows {
		supplier := ""
		if row.Supplier != nil {
			supplier = row.Supplier.Name
		}
		_ = writer.Write([]string{row.Number, supplier, row.Status, row.CreatedAt.Format("2006-01-02"), strconv.FormatInt(row.TotalCents, 10), row.Currency, optionalUint(row.RequisitionID)})
	}
}

func (h *Handler) activity(r *http.Request, entity string, id uint, action, details string) {
	user := auth.CurrentUser(r)
	origin := "UI"
	if isMCPMutation(r) {
		origin = "MCP/AI"
	}
	details = strings.TrimSpace("origin=" + origin + " " + details)
	_ = h.db.Create(&models.Activity{EntityType: entity, EntityID: id, Action: action, UserID: user.ID, Username: user.Username, Details: details}).Error
}

func decode(w http.ResponseWriter, r *http.Request, dest any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 2<<20)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dest); err != nil {
		badRequest(w, "Ungültige Anfrage: "+err.Error())
		return false
	}
	return true
}

func pathID(w http.ResponseWriter, r *http.Request) (uint, bool) {
	id, ok := parseUint(chi.URLParam(r, "id"))
	if !ok {
		badRequest(w, "Ungültige ID")
	}
	return id, ok
}
func parseUint(value string) (uint, bool) {
	n, err := strconv.ParseUint(value, 10, 64)
	return uint(n), err == nil && n > 0
}
func optionalUint(value *uint) string {
	if value == nil {
		return ""
	}
	return strconv.FormatUint(uint64(*value), 10)
}
func nextNumber(prefix string) string {
	return fmt.Sprintf("%s-%s-%04d", prefix, time.Now().Format("20060102-150405"), time.Now().Nanosecond()%10000)
}
func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
func badRequest(w http.ResponseWriter, message string) {
	writeJSON(w, http.StatusBadRequest, map[string]string{"error": message})
}
func forbidden(w http.ResponseWriter) {
	writeJSON(w, http.StatusForbidden, map[string]string{"error": "Keine Berechtigung"})
}
func notFound(w http.ResponseWriter) {
	writeJSON(w, http.StatusNotFound, map[string]string{"error": "Nicht gefunden"})
}
func serverError(w http.ResponseWriter, _ error) {
	writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Interner Serverfehler"})
}
func conflictOrServer(w http.ResponseWriter, err error) {
	if strings.Contains(strings.ToLower(err.Error()), "duplicate") || strings.Contains(strings.ToLower(err.Error()), "unique") {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "Datensatz existiert bereits"})
		return
	}
	serverError(w, err)
}
