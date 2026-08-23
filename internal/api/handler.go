package api

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"procurementcore/internal/auth"
	"procurementcore/internal/models"
	"procurementcore/internal/service"

	"github.com/go-chi/chi/v5"
	"gorm.io/gorm"
)

type Handler struct {
	db *gorm.DB
}

func NewHandler(db *gorm.DB) *Handler { return &Handler{db: db} }

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
	r.Get("/products/{id}", h.getProduct)
	r.With(auth.RequireAdmin).Post("/products", h.createProduct)
	r.With(auth.RequireAdmin).Put("/products/{id}", h.updateProduct)
	r.With(auth.RequireAdmin).Delete("/products/{id}", h.deleteProduct)
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
	r.With(auth.RequireAdmin).Post("/orders", h.createOrder)
	r.With(auth.RequireAdmin).Put("/orders/{id}", h.updateOrder)
	r.With(auth.RequireAdmin).Post("/orders/{id}/receipt", h.receiveOrder)
	r.Get("/activity", h.listActivity)
	r.Get("/export/spend.csv", h.exportSpend)
	return r
}

func (h *Handler) me(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, auth.CurrentUser(r))
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
	if !decode(w, r, &row) || strings.TrimSpace(row.Name) == "" {
		if row.Name == "" {
			badRequest(w, "Name ist erforderlich")
		}
		return
	}
	if len(row.ParameterSchema) == 0 {
		row.ParameterSchema = json.RawMessage("[]")
	}
	if !json.Valid(row.ParameterSchema) {
		badRequest(w, "Parameter-Schema ist kein gültiges JSON")
		return
	}
	if err := h.db.Create(&row).Error; err != nil {
		conflictOrServer(w, err)
		return
	}
	h.activity(r, "category", row.ID, "created", row.Name)
	writeJSON(w, http.StatusCreated, row)
}

func (h *Handler) updateCategory(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var row models.Category
	if err := h.db.First(&row, id).Error; err != nil {
		notFound(w)
		return
	}
	var input models.Category
	if !decode(w, r, &input) {
		return
	}
	if strings.TrimSpace(input.Name) == "" || !json.Valid(input.ParameterSchema) {
		badRequest(w, "Name und gültiges Parameter-Schema sind erforderlich")
		return
	}
	row.Name, row.Description, row.ParameterSchema = input.Name, input.Description, input.ParameterSchema
	if err := h.db.Save(&row).Error; err != nil {
		conflictOrServer(w, err)
		return
	}
	h.activity(r, "category", row.ID, "updated", row.Name)
	writeJSON(w, http.StatusOK, row)
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
	var row models.Supplier
	if !decode(w, r, &row) {
		return
	}
	if msg := validateSupplier(&row); msg != "" {
		badRequest(w, msg)
		return
	}
	if err := h.db.Create(&row).Error; err != nil {
		conflictOrServer(w, err)
		return
	}
	h.activity(r, "supplier", row.ID, "created", row.Name)
	writeJSON(w, http.StatusCreated, row)
}

func (h *Handler) updateSupplier(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var row models.Supplier
	if err := h.db.First(&row, id).Error; err != nil {
		notFound(w)
		return
	}
	var input models.Supplier
	if !decode(w, r, &input) {
		return
	}
	if msg := validateSupplier(&input); msg != "" {
		badRequest(w, msg)
		return
	}
	input.ID, input.CreatedAt = row.ID, row.CreatedAt
	if err := h.db.Save(&input).Error; err != nil {
		conflictOrServer(w, err)
		return
	}
	h.activity(r, "supplier", input.ID, "updated", input.Name)
	writeJSON(w, http.StatusOK, input)
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

func (h *Handler) listProducts(w http.ResponseWriter, r *http.Request) {
	f := ParseProductFilter(r.URL.Query())
	query := h.db.Model(&models.Product{}).Preload("Category").Preload("Offers", "active = ?", true).Preload("Offers.Supplier")
	if f.Query != "" {
		like := "%" + f.Query + "%"
		query = query.Where("proc_products.name ILIKE ? OR proc_products.sku ILIKE ? OR proc_products.description ILIKE ?", like, like, like)
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
	writeJSON(w, http.StatusOK, row)
}

func validateProduct(row *models.Product) string {
	row.SKU, row.Name = strings.ToUpper(strings.TrimSpace(row.SKU)), strings.TrimSpace(row.Name)
	if row.SKU == "" || row.Name == "" {
		return "SKU und Name sind erforderlich"
	}
	if len(row.Parameters) == 0 {
		row.Parameters = json.RawMessage("{}")
	}
	if !json.Valid(row.Parameters) {
		return "Parameter sind kein gültiges JSON"
	}
	if row.Unit == "" {
		row.Unit = "Stk."
	}
	return ""
}

func (h *Handler) createProduct(w http.ResponseWriter, r *http.Request) {
	var row models.Product
	if !decode(w, r, &row) {
		return
	}
	if msg := validateProduct(&row); msg != "" {
		badRequest(w, msg)
		return
	}
	row.Offers = nil
	if err := h.db.Create(&row).Error; err != nil {
		conflictOrServer(w, err)
		return
	}
	h.activity(r, "product", row.ID, "created", row.Name)
	writeJSON(w, http.StatusCreated, row)
}

func (h *Handler) updateProduct(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var existing models.Product
	if err := h.db.First(&existing, id).Error; err != nil {
		notFound(w)
		return
	}
	var input models.Product
	if !decode(w, r, &input) {
		return
	}
	if msg := validateProduct(&input); msg != "" {
		badRequest(w, msg)
		return
	}
	input.ID, input.CreatedAt, input.Offers = existing.ID, existing.CreatedAt, nil
	if err := h.db.Save(&input).Error; err != nil {
		conflictOrServer(w, err)
		return
	}
	h.activity(r, "product", input.ID, "updated", input.Name)
	writeJSON(w, http.StatusOK, input)
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
	if row.Currency == "" {
		row.Currency = "EUR"
	}
	row.Currency = strings.ToUpper(row.Currency)
	if len(row.Currency) != 3 {
		return "Währung muss aus drei Buchstaben bestehen"
	}
	if row.MinimumQuantity <= 0 {
		row.MinimumQuantity = 1
	}
	if row.PackSize <= 0 {
		row.PackSize = 1
	}
	if row.PurchaseURL != "" {
		u, err := url.ParseRequestURI(row.PurchaseURL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
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
	err := h.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&row).Error; err != nil {
			return err
		}
		return service.RecordPriceAndEvaluateAlerts(tx, &row)
	})
	if err != nil {
		conflictOrServer(w, err)
		return
	}
	h.activity(r, "offer", row.ID, "created", fmt.Sprintf("product=%d price=%d", row.ProductID, row.PriceCents))
	h.db.Preload("Supplier").First(&row, row.ID)
	writeJSON(w, http.StatusCreated, row)
}

func (h *Handler) updateOffer(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var existing models.Offer
	if err := h.db.First(&existing, id).Error; err != nil {
		notFound(w)
		return
	}
	var input models.Offer
	if !decode(w, r, &input) {
		return
	}
	input.ID, input.ProductID, input.CreatedAt = existing.ID, existing.ProductID, existing.CreatedAt
	if msg := validateOffer(&input); msg != "" {
		badRequest(w, msg)
		return
	}
	err := h.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Save(&input).Error; err != nil {
			return err
		}
		if input.PriceCents != existing.PriceCents {
			return service.RecordPriceAndEvaluateAlerts(tx, &input)
		}
		return nil
	})
	if err != nil {
		conflictOrServer(w, err)
		return
	}
	h.activity(r, "offer", input.ID, "updated", fmt.Sprintf("price=%d", input.PriceCents))
	h.db.Preload("Supplier").First(&input, input.ID)
	writeJSON(w, http.StatusOK, input)
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
		Decision string `json:"decision"`
		Note     string `json:"note"`
	}
	if !decode(w, r, &input) {
		return
	}
	if input.Decision != "approved" && input.Decision != "rejected" {
		badRequest(w, "Entscheidung muss approved oder rejected sein")
		return
	}
	var row models.Requisition
	if err := h.db.First(&row, id).Error; err != nil {
		notFound(w)
		return
	}
	if row.Status != "submitted" {
		badRequest(w, "Bedarf ist nicht zur Entscheidung eingereicht")
		return
	}
	user, now := auth.CurrentUser(r), time.Now()
	row.Status, row.ApprovedBy, row.ApprovedByName, row.DecisionNote, row.DecidedAt = input.Decision, &user.ID, user.Username, input.Note, &now
	if err := h.db.Save(&row).Error; err != nil {
		serverError(w, err)
		return
	}
	h.activity(r, "requisition", id, input.Decision, row.Number)
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
	writeJSON(w, http.StatusOK, row)
}

func validateOrder(row *models.PurchaseOrder) string {
	if row.SupplierID == 0 || len(row.Lines) == 0 {
		return "Lieferant und mindestens eine Position sind erforderlich"
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
	if row.Currency == "" {
		row.Currency = "EUR"
	}
	return ""
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
	if row.Status == "" {
		row.Status = "draft"
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
		Status           string     `json:"status"`
		ExpectedDelivery *time.Time `json:"expectedDelivery"`
		Notes            string     `json:"notes"`
	}
	if !decode(w, r, &input) {
		return
	}
	allowed := map[string]bool{"draft": true, "sent": true, "confirmed": true, "partially_received": true, "received": true, "cancelled": true}
	if !allowed[input.Status] {
		badRequest(w, "Ungültiger Bestellstatus")
		return
	}
	row.Status, row.ExpectedDelivery, row.Notes = input.Status, input.ExpectedDelivery, input.Notes
	if input.Status == "sent" && row.OrderDate == nil {
		now := time.Now()
		row.OrderDate = &now
	}
	if err := h.db.Save(&row).Error; err != nil {
		serverError(w, err)
		return
	}
	h.activity(r, "purchase_order", row.ID, "status_changed", input.Status)
	writeJSON(w, http.StatusOK, row)
}

func (h *Handler) receiveOrder(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var input struct {
		LineID   uint    `json:"lineId"`
		Quantity float64 `json:"quantity"`
		Note     string  `json:"note"`
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
	if err := h.db.Where("id = ? AND purchase_order_id = ?", input.LineID, id).First(&line).Error; err != nil {
		notFound(w)
		return
	}
	if line.ReceivedQuantity+input.Quantity > line.Quantity {
		badRequest(w, "Wareneingang überschreitet Bestellmenge")
		return
	}
	receipt := models.Receipt{PurchaseOrderID: id, PurchaseOrderLineID: line.ID, Quantity: input.Quantity, ReceivedBy: user.ID, ReceivedByName: user.Username, Note: input.Note, ReceivedAt: time.Now()}
	err := h.db.Transaction(func(tx *gorm.DB) error {
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
		return tx.Model(&models.PurchaseOrder{}).Where("id = ?", id).Update("status", status).Error
	})
	if err != nil {
		serverError(w, err)
		return
	}
	h.activity(r, "purchase_order", id, "goods_received", fmt.Sprintf("line=%d quantity=%g", line.ID, input.Quantity))
	writeJSON(w, http.StatusCreated, receipt)
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
