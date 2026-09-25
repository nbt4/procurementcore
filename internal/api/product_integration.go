package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"procurementcore/internal/auth"
	"procurementcore/internal/models"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
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
	TrackingMode    string   `json:"trackingMode" gorm:"column:tracking_mode"`
	StockQuantity   float64  `json:"stockQuantity" gorm:"column:stock_quantity"`
	DeviceCount     int64    `json:"deviceCount" gorm:"column:device_count"`
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
		       p.tracking_mode,COALESCE(p.stock_quantity,0) AS stock_quantity,
		       (SELECT COUNT(*) FROM devices d WHERE d.productID=p.productID) AS device_count,
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
	type warehouseLink struct {
		ProcurementProductID uint    `gorm:"column:procurement_product_id"`
		WarehouseProductID   int64   `gorm:"column:warehouse_product_id"`
		ProductCode          string  `gorm:"column:product_code"`
		Name                 string  `gorm:"column:name"`
		TrackingMode         string  `gorm:"column:tracking_mode"`
		StockQuantity        float64 `gorm:"column:stock_quantity"`
		DeviceCount          int64   `gorm:"column:device_count"`
	}
	var links []warehouseLink
	if err := h.db.Raw(`
		SELECT cpl.procurement_product_id,cpl.warehouse_product_id,
		       COALESCE(p.product_code,'') AS product_code,p.name,p.tracking_mode,
		       COALESCE(p.stock_quantity,0) AS stock_quantity,
		       (SELECT COUNT(*) FROM devices d WHERE d.productID=p.productID) AS device_count
		FROM core_product_links cpl
		JOIN products p ON p.productID=cpl.warehouse_product_id
		WHERE cpl.procurement_product_id IN ? AND p.lifecycle_status='active'
	`, ids).Scan(&links).Error; err != nil {
		return
	}
	byProcurement := map[uint]warehouseLink{}
	for _, link := range links {
		byProcurement[link.ProcurementProductID] = link
	}
	for i := range products {
		if link, ok := byProcurement[products[i].ID]; ok {
			value := link.WarehouseProductID
			products[i].WarehouseProductID = &value
			products[i].WarehouseProductCode = link.ProductCode
			products[i].WarehouseProductName = link.Name
			products[i].WarehouseTrackingMode = link.TrackingMode
			products[i].WarehouseStockQuantity = link.StockQuantity
			products[i].WarehouseDeviceCount = link.DeviceCount
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

func (h *Handler) listProductLinks(w http.ResponseWriter, r *http.Request) {
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
	h.enrichProductLinkCandidatesWithJev(r.Context(), result, products, warehouse)
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
	items := []productLinkOverview{{ProcurementProductID: product.ID, Candidates: rankedWarehouseCandidates(product, warehouse)}}
	h.enrichProductLinkCandidatesWithJev(r.Context(), items, []models.Product{product}, warehouse)
	writeJSON(w, http.StatusOK, items[0].Candidates)
}

func (h *Handler) linkWarehouseProduct(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var input struct {
		WarehouseProductID int64      `json:"warehouseProductId"`
		ExpectedUpdatedAt  *time.Time `json:"expectedUpdatedAt"`
	}
	if !decode(w, r, &input) {
		return
	}
	if input.WarehouseProductID <= 0 {
		badRequest(w, "Warehouse-Produkt fehlt")
		return
	}
	user := auth.CurrentUser(r)
	var link models.CoreProductLink
	err := h.db.Transaction(func(tx *gorm.DB) error {
		idempotency, replay, err := beginIdempotentMutation(tx, r, fmt.Sprintf("product_link:%d", id), input)
		if err != nil {
			return err
		}
		if replay != nil {
			return json.Unmarshal(replay, &link)
		}
		var product models.Product
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&product, id).Error; err != nil {
			return err
		}
		if !product.Active {
			return &receiptFlowError{status: http.StatusConflict, code: "product_inactive", message: "Beschaffungsprodukt ist archiviert"}
		}
		var warehouseID int64
		if err := tx.Raw(`SELECT productID FROM products WHERE productID=? AND lifecycle_status='active' FOR UPDATE`, input.WarehouseProductID).Scan(&warehouseID).Error; err != nil {
			return err
		}
		if warehouseID != input.WarehouseProductID {
			return &receiptFlowError{status: http.StatusConflict, code: "warehouse_product_inactive", message: "Warehouse-Produkt fehlt oder ist archiviert"}
		}
		lookup := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("procurement_product_id=?", id).Limit(1).Find(&link)
		if lookup.Error != nil {
			return lookup.Error
		}
		exists := lookup.RowsAffected == 1
		version := product.UpdatedAt
		if exists {
			version = link.UpdatedAt
		}
		if err := validateExpectedUpdate(version, input.ExpectedUpdatedAt, isMCPMutation(r)); err != nil {
			return err
		}
		if exists && link.WarehouseProductID == input.WarehouseProductID {
			return completeIdempotentMutation(tx, idempotency, http.StatusOK, link)
		}
		var taken int64
		if err := tx.Model(&models.CoreProductLink{}).Where("warehouse_product_id=? AND procurement_product_id<>?", input.WarehouseProductID, id).Count(&taken).Error; err != nil {
			return err
		}
		if taken > 0 {
			return &receiptFlowError{status: http.StatusConflict, code: "warehouse_product_already_linked", message: "Warehouse-Produkt ist bereits mit einem anderen Beschaffungsprodukt verknüpft"}
		}
		var before any
		action := "created"
		if exists {
			before = link
			action = "updated"
			var receipts, openOrders int64
			if err := tx.Model(&models.Receipt{}).Joins("JOIN proc_purchase_order_lines pol ON pol.id=proc_receipts.purchase_order_line_id").Where("pol.product_id=?", id).Count(&receipts).Error; err != nil {
				return err
			}
			if err := tx.Model(&models.PurchaseOrderLine{}).Joins("JOIN proc_purchase_orders po ON po.id=proc_purchase_order_lines.purchase_order_id").Where("proc_purchase_order_lines.product_id=? AND po.status IN ?", id, []string{"draft", "sent", "confirmed", "partially_received"}).Count(&openOrders).Error; err != nil {
				return err
			}
			if receipts > 0 || openOrders > 0 {
				return &receiptFlowError{status: http.StatusConflict, code: "product_link_in_use", message: "Wareneingänge oder offene Bestellungen verhindern eine Änderung der Produktverknüpfung"}
			}
			link.WarehouseProductID = input.WarehouseProductID
			link.LinkedBy, link.LinkedByName = user.ID, user.Username
			link.UpdatedAt = time.Time{}
			if err := tx.Save(&link).Error; err != nil {
				return err
			}
		} else {
			link = models.CoreProductLink{ProcurementProductID: id, WarehouseProductID: input.WarehouseProductID, LinkMethod: "manual", LinkedBy: user.ID, LinkedByName: user.Username}
			if err := tx.Create(&link).Error; err != nil {
				return err
			}
		}
		if err := tx.First(&link, link.ID).Error; err != nil {
			return err
		}
		origin := "UI"
		if isMCPMutation(r) {
			origin = "MCP/AI"
		}
		oldJSON, err := json.Marshal(before)
		if err != nil {
			return err
		}
		newJSON, err := json.Marshal(map[string]any{"origin": origin, "link": link})
		if err != nil {
			return err
		}
		if err := tx.Exec(`INSERT INTO audit_log (user_id,action,entity_type,entity_id,old_values,new_values,ip_address,user_agent) VALUES (?, ?, 'procurement_product_link', ?, ?::jsonb, ?::jsonb, ?, ?)`, user.ID, "product_link."+action, strconv.FormatUint(uint64(link.ID), 10), string(oldJSON), string(newJSON), requestIP(r), r.UserAgent()).Error; err != nil {
			return err
		}
		if err := tx.Create(&models.Activity{EntityType: "product_link", EntityID: link.ID, Action: action, UserID: user.ID, Username: user.Username, Details: fmt.Sprintf("origin=%s procurement=%d warehouse=%d", origin, id, input.WarehouseProductID)}).Error; err != nil {
			return err
		}
		return completeIdempotentMutation(tx, idempotency, http.StatusOK, link)
	})
	if err != nil {
		writeProcurementFlowError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, link)
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
