package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"unicode"

	"procurementcore/internal/auth"
	"procurementcore/internal/models"
)

type warehouseProductCandidate struct {
	ProductID       int64    `json:"productId" gorm:"column:product_id"`
	ProductCode     string   `json:"productCode" gorm:"column:product_code"`
	Name            string   `json:"name"`
	Manufacturer    string   `json:"manufacturer"`
	Model           string   `json:"model"`
	ManufacturerSKU string   `json:"manufacturerPartNumber" gorm:"column:manufacturer_part_number"`
	EAN             string   `json:"ean"`
	Category        string   `json:"category"`
	ProcurementID   *uint    `json:"procurementProductId,omitempty" gorm:"column:procurement_product_id"`
	Score           int      `json:"score" gorm:"-"`
	Reasons         []string `json:"reasons" gorm:"-"`
}

type productLinkOverview struct {
	ProcurementProductID uint                        `json:"procurementProductId"`
	SKU                  string                      `json:"sku"`
	Name                 string                      `json:"name"`
	Manufacturer         string                      `json:"manufacturer"`
	Model                string                      `json:"model"`
	WarehouseProductID   *int64                      `json:"warehouseProductId,omitempty"`
	WarehouseProduct     *warehouseProductCandidate  `json:"warehouseProduct,omitempty"`
	Candidates           []warehouseProductCandidate `json:"candidates"`
}

func normalizedIdentity(value string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return unicode.ToLower(r)
		}
		return -1
	}, strings.TrimSpace(value))
}

func productEAN(product models.Product) string {
	var values map[string]any
	for _, raw := range []json.RawMessage{product.Attributes, product.Parameters} {
		if json.Unmarshal(raw, &values) != nil {
			continue
		}
		for key, value := range values {
			normalizedKey := normalizedIdentity(key)
			if normalizedKey == "ean" || normalizedKey == "gtin" || normalizedKey == "gtin13" {
				return fmt.Sprint(value)
			}
		}
	}
	return ""
}

func identityTokens(value string) map[string]bool {
	tokens := map[string]bool{}
	for _, token := range strings.FieldsFunc(strings.ToLower(value), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) }) {
		if len([]rune(token)) >= 2 {
			tokens[token] = true
		}
	}
	return tokens
}

func scoreWarehouseCandidate(product models.Product, candidate warehouseProductCandidate) warehouseProductCandidate {
	procEAN, warehouseEAN := normalizedIdentity(productEAN(product)), normalizedIdentity(candidate.EAN)
	procManufacturer, warehouseManufacturer := normalizedIdentity(product.Manufacturer), normalizedIdentity(candidate.Manufacturer)
	procModel, warehouseModel := normalizedIdentity(product.Model), normalizedIdentity(candidate.Model)
	procSKU, warehouseSKU := normalizedIdentity(product.SKU), normalizedIdentity(candidate.ManufacturerSKU)
	procName, warehouseName := normalizedIdentity(product.Name), normalizedIdentity(candidate.Name)
	if procEAN != "" && procEAN == warehouseEAN {
		candidate.Score += 100
		candidate.Reasons = append(candidate.Reasons, "EAN identisch")
	}
	if procSKU != "" && procSKU == warehouseSKU {
		candidate.Score += 80
		candidate.Reasons = append(candidate.Reasons, "Herstellerartikelnummer identisch")
	}
	if procModel != "" && procModel == warehouseModel {
		candidate.Score += 55
		candidate.Reasons = append(candidate.Reasons, "Modell identisch")
	}
	if procManufacturer != "" && procManufacturer == warehouseManufacturer {
		candidate.Score += 25
		candidate.Reasons = append(candidate.Reasons, "Hersteller identisch")
	}
	if procName != "" && procName == warehouseName {
		candidate.Score += 60
		candidate.Reasons = append(candidate.Reasons, "Name identisch")
	} else {
		left, right, matches := identityTokens(product.Name), identityTokens(candidate.Name), 0
		for token := range left {
			if right[token] {
				matches++
			}
		}
		if matches > 0 {
			candidate.Score += min(30, matches*10)
			candidate.Reasons = append(candidate.Reasons, "Name ähnlich")
		}
	}
	return candidate
}

func (h *Handler) warehouseProducts() ([]warehouseProductCandidate, error) {
	var rows []warehouseProductCandidate
	err := h.db.Raw(`
		SELECT p.productID AS product_id,COALESCE(p.product_code,'') AS product_code,p.name,
		       COALESCE(m.name,'') AS manufacturer,COALESCE(p.model_number,'') AS model,
		       COALESCE(p.manufacturer_part_number,'') AS manufacturer_part_number,
		       COALESCE(p.ean,'') AS ean,COALESCE(c.name,'') AS category,
		       cpl.procurement_product_id
		FROM products p
		LEFT JOIN manufacturer m ON m.manufacturerid=p.manufacturerid
		LEFT JOIN categories c ON c.categoryid=p.categoryid
		LEFT JOIN core_product_links cpl ON cpl.warehouse_product_id=p.productID
		WHERE p.lifecycle_status='active'
		ORDER BY p.name
	`).Scan(&rows).Error
	return rows, err
}

func (h *Handler) hydrateWarehouseLinks(products []models.Product) {
	if len(products) == 0 {
		return
	}
	ids := make([]uint, 0, len(products))
	for i := range products {
		ids = append(ids, products[i].ID)
	}
	var links []models.CoreProductLink
	if h.db.Where("procurement_product_id IN ?", ids).Find(&links).Error != nil {
		return
	}
	byProcurement := map[uint]int64{}
	for _, link := range links {
		byProcurement[link.ProcurementProductID] = link.WarehouseProductID
	}
	for i := range products {
		if id, ok := byProcurement[products[i].ID]; ok {
			value := id
			products[i].WarehouseProductID = &value
		}
	}
}

func rankedWarehouseCandidates(product models.Product, warehouse []warehouseProductCandidate) []warehouseProductCandidate {
	rows := make([]warehouseProductCandidate, 0, len(warehouse))
	for _, candidate := range warehouse {
		if candidate.ProcurementID != nil {
			continue
		}
		candidate = scoreWarehouseCandidate(product, candidate)
		if candidate.Score >= 20 {
			rows = append(rows, candidate)
		}
	}
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].Score == rows[j].Score {
			return rows[i].Name < rows[j].Name
		}
		return rows[i].Score > rows[j].Score
	})
	if len(rows) > 5 {
		rows = rows[:5]
	}
	return rows
}

func (h *Handler) listProductLinks(w http.ResponseWriter, _ *http.Request) {
	var products []models.Product
	if err := h.db.Where("active = ?", true).Order("name").Find(&products).Error; err != nil {
		serverError(w, err)
		return
	}
	warehouse, err := h.warehouseProducts()
	if err != nil {
		serverError(w, err)
		return
	}
	var links []models.CoreProductLink
	if err := h.db.Find(&links).Error; err != nil {
		serverError(w, err)
		return
	}
	linksByProcurement := map[uint]models.CoreProductLink{}
	warehouseByID := map[int64]warehouseProductCandidate{}
	for _, row := range warehouse {
		warehouseByID[row.ProductID] = row
	}
	for _, link := range links {
		linksByProcurement[link.ProcurementProductID] = link
	}
	result := make([]productLinkOverview, 0, len(products))
	for _, product := range products {
		item := productLinkOverview{ProcurementProductID: product.ID, SKU: product.SKU, Name: product.Name, Manufacturer: product.Manufacturer, Model: product.Model, Candidates: []warehouseProductCandidate{}}
		if link, ok := linksByProcurement[product.ID]; ok {
			id := link.WarehouseProductID
			item.WarehouseProductID = &id
			if target, found := warehouseByID[id]; found {
				item.WarehouseProduct = &target
			}
		} else {
			item.Candidates = rankedWarehouseCandidates(product, warehouse)
		}
		result = append(result, item)
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": result, "warehouseProducts": warehouse})
}

func (h *Handler) warehouseCandidates(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var product models.Product
	if err := h.db.First(&product, id).Error; err != nil {
		notFound(w)
		return
	}
	warehouse, err := h.warehouseProducts()
	if err != nil {
		serverError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, rankedWarehouseCandidates(product, warehouse))
}

func (h *Handler) linkWarehouseProduct(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var input struct {
		WarehouseProductID int64 `json:"warehouseProductId"`
	}
	if !decode(w, r, &input) {
		return
	}
	if input.WarehouseProductID <= 0 {
		badRequest(w, "Warehouse-Produkt fehlt")
		return
	}
	var procCount, warehouseCount int64
	h.db.Model(&models.Product{}).Where("id=? AND active=TRUE", id).Count(&procCount)
	h.db.Raw("SELECT COUNT(*) FROM products WHERE productID=? AND lifecycle_status='active'", input.WarehouseProductID).Scan(&warehouseCount)
	if procCount == 0 || warehouseCount == 0 {
		badRequest(w, "Produkt wurde nicht gefunden oder ist archiviert")
		return
	}
	user := auth.CurrentUser(r)
	link := models.CoreProductLink{ProcurementProductID: id, WarehouseProductID: input.WarehouseProductID, LinkMethod: "manual", LinkedBy: user.ID, LinkedByName: user.Username}
	if err := h.db.Create(&link).Error; err != nil {
		conflictOrServer(w, err)
		return
	}
	h.activity(r, "product_link", link.ID, "created", fmt.Sprintf("procurement=%d warehouse=%d", id, input.WarehouseProductID))
	writeJSON(w, http.StatusCreated, link)
}

func (h *Handler) unlinkWarehouseProduct(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	result := h.db.Where("procurement_product_id=?", id).Delete(&models.CoreProductLink{})
	if result.Error != nil {
		serverError(w, result.Error)
		return
	}
	if result.RowsAffected == 0 {
		notFound(w)
		return
	}
	h.activity(r, "product_link", id, "deleted", fmt.Sprintf("procurement=%d", id))
	w.WriteHeader(http.StatusNoContent)
}
