package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"procurementcore/internal/models"

	"github.com/go-chi/chi/v5"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func TestProductUpdateVersionAuditArchiveAndReplay(t *testing.T) {
	dsn := os.Getenv("PROCUREMENT_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set PROCUREMENT_TEST_DATABASE_URL to a disposable PostgreSQL database ending in _test")
	}
	parsed, err := url.Parse(dsn)
	if err != nil || !strings.HasSuffix(strings.TrimPrefix(parsed.Path, "/"), "_test") {
		t.Fatal("integration test requires a dedicated _test database")
	}
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	defer sqlDB.Close()
	sqlDB.SetMaxOpenConns(1)
	const schema = "product_mutation_test"
	if err := db.Exec("DROP SCHEMA IF EXISTS " + schema + " CASCADE").Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("CREATE SCHEMA " + schema).Error; err != nil {
		t.Fatal(err)
	}
	defer db.Exec("DROP SCHEMA IF EXISTS " + schema + " CASCADE")
	if err := db.Exec("SET search_path TO " + schema).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&models.Category{}, &models.Supplier{}, &models.Product{}, &models.Offer{}, &models.PriceHistory{}, &models.PriceAlert{}, &models.Activity{}, &models.IdempotencyRecord{}); err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`CREATE TABLE audit_log (id BIGSERIAL PRIMARY KEY, user_id BIGINT, action TEXT, entity_type TEXT, entity_id TEXT, old_values JSONB, new_values JSONB, ip_address TEXT, user_agent TEXT)`,
		`CREATE TABLE proc_purchase_orders (id BIGSERIAL PRIMARY KEY, status TEXT)`,
		`CREATE TABLE proc_purchase_order_lines (id BIGSERIAL PRIMARY KEY, purchase_order_id BIGINT, product_id BIGINT)`,
		`CREATE TABLE proc_requisitions (id BIGSERIAL PRIMARY KEY, status TEXT)`,
		`CREATE TABLE proc_requisition_lines (id BIGSERIAL PRIMARY KEY, requisition_id BIGINT, product_id BIGINT)`,
	} {
		if err := db.Exec(statement).Error; err != nil {
			t.Fatal(err)
		}
	}
	category := models.Category{Name: "Lighting"}
	if err := db.Create(&category).Error; err != nil {
		t.Fatal(err)
	}
	product := models.Product{SKU: "NODE-1", Name: "Node", CategoryID: &category.ID, Active: true, Unit: "Stk.", Parameters: json.RawMessage("{}"), Attributes: json.RawMessage("{}")}
	if err := db.Create(&product).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.First(&product, product.ID).Error; err != nil {
		t.Fatal(err)
	}
	h := &Handler{db: db}
	call := func(key string, data map[string]any) *httptest.ResponseRecorder {
		body, err := json.Marshal(data)
		if err != nil {
			t.Fatal(err)
		}
		r := httptest.NewRequest(http.MethodPut, "/products/"+strconv.FormatUint(uint64(product.ID), 10), bytes.NewReader(body))
		r.Header.Set("X-Cores-Origin", "MCP/AI")
		r.Header.Set("Idempotency-Key", key)
		route := chi.NewRouteContext()
		route.URLParams.Add("id", strconv.FormatUint(uint64(product.ID), 10))
		r = r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, route))
		w := httptest.NewRecorder()
		h.updateProduct(w, r)
		return w
	}
	base := map[string]any{"sku": "NODE-1", "name": "Node Plus", "categoryId": category.ID, "unit": "Stk.", "parameters": map[string]any{}, "attributes": map[string]any{}, "active": true}
	if response := call("product-missing-version", base); response.Code != http.StatusPreconditionRequired {
		t.Fatalf("missing version: %d %s", response.Code, response.Body.String())
	}
	base["expectedUpdatedAt"] = product.UpdatedAt.Add(-time.Second).UTC().Format(time.RFC3339Nano)
	if response := call("product-stale-version", base); response.Code != http.StatusConflict {
		t.Fatalf("stale version: %d %s", response.Code, response.Body.String())
	}
	base["expectedUpdatedAt"] = product.UpdatedAt.UTC().Format(time.RFC3339Nano)
	first := call("product-update-001", base)
	if first.Code != http.StatusOK {
		t.Fatalf("update: %d %s", first.Code, first.Body.String())
	}
	if replay := call("product-update-001", base); replay.Code != http.StatusOK || replay.Body.String() != first.Body.String() {
		t.Fatalf("replay: %d %s", replay.Code, replay.Body.String())
	}
	if conflict := call("product-update-001", map[string]any{"sku": "OTHER", "name": "Other"}); conflict.Code != http.StatusConflict {
		t.Fatalf("changed payload reused key: %d", conflict.Code)
	}
	var updated models.Product
	if err := db.First(&updated, product.ID).Error; err != nil {
		t.Fatal(err)
	}
	if updated.Name != "Node Plus" {
		t.Fatalf("name was not saved: %#v", updated)
	}
	if err := db.Exec(`INSERT INTO proc_purchase_orders(id,status) VALUES (1,'ordered')`).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`INSERT INTO proc_purchase_order_lines(purchase_order_id,product_id) VALUES (1,?)`, product.ID).Error; err != nil {
		t.Fatal(err)
	}
	archive := map[string]any{"sku": updated.SKU, "name": updated.Name, "categoryId": category.ID, "unit": updated.Unit, "parameters": map[string]any{}, "attributes": map[string]any{}, "active": false, "expectedUpdatedAt": updated.UpdatedAt.UTC().Format(time.RFC3339Nano)}
	if response := call("product-archive-blocked", archive); response.Code != http.StatusConflict {
		t.Fatalf("active order did not block archive: %d %s", response.Code, response.Body.String())
	}
	if err := db.Exec(`UPDATE proc_purchase_orders SET status='received' WHERE id=1`).Error; err != nil {
		t.Fatal(err)
	}
	if response := call("product-archive-001", archive); response.Code != http.StatusOK {
		t.Fatalf("archive: %d %s", response.Code, response.Body.String())
	}
	if err := db.First(&updated, product.ID).Error; err != nil || updated.Active {
		t.Fatalf("product not archived: %#v %v", updated, err)
	}
	for table, want := range map[string]int64{"audit_log": 2, "proc_activities": 2, "proc_idempotency_records": 2} {
		var count int64
		if err := db.Table(table).Count(&count).Error; err != nil || count != want {
			t.Fatalf("%s count=%d err=%v, want %d", table, count, err, want)
		}
	}
	supplier := models.Supplier{Name: "Lighting Supply", Code: "LIGHT-SUPPLY", Active: true, RiskLevel: "low"}
	if err := db.Create(&supplier).Error; err != nil {
		t.Fatal(err)
	}
	create := func(key string, supplierID uint) *httptest.ResponseRecorder {
		payload := map[string]any{"sku": "NODE-NEW", "name": "New Node", "categoryId": category.ID, "active": true,
			"initialOffer": map[string]any{"supplierId": supplierID, "priceCents": 12900, "currency": "EUR", "active": true}}
		body, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		r := httptest.NewRequest(http.MethodPost, "/products", bytes.NewReader(body))
		r.Header.Set("X-Cores-Origin", "MCP/AI")
		r.Header.Set("Idempotency-Key", key)
		w := httptest.NewRecorder()
		h.createProduct(w, r)
		return w
	}
	if response := create("product-create-invalid-supplier", supplier.ID+100); response.Code != http.StatusConflict {
		t.Fatalf("invalid supplier did not roll back: %d %s", response.Code, response.Body.String())
	}
	var count int64
	if err := db.Model(&models.Product{}).Where("sku = ?", "NODE-NEW").Count(&count).Error; err != nil || count != 0 {
		t.Fatalf("failed product chain left product: %d %v", count, err)
	}
	created := create("product-create-with-offer", supplier.ID)
	if created.Code != http.StatusCreated || !bytes.Contains(created.Body.Bytes(), []byte(`"createdOffer"`)) {
		t.Fatalf("atomic product+offer create: %d %s", created.Code, created.Body.String())
	}
	if replay := create("product-create-with-offer", supplier.ID); replay.Code != http.StatusCreated || replay.Body.String() != created.Body.String() {
		t.Fatalf("atomic create replay: %d %s", replay.Code, replay.Body.String())
	}
	for table, want := range map[string]int64{"proc_products": 2, "proc_offers": 1, "proc_price_histories": 1, "audit_log": 4, "proc_activities": 4, "proc_idempotency_records": 3} {
		if err := db.Table(table).Count(&count).Error; err != nil || count != want {
			t.Fatalf("%s count=%d err=%v, want %d", table, count, err, want)
		}
	}
}
