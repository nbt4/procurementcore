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

func TestProductLinkVersionConflictAuditAndReplay(t *testing.T) {
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
	const schema = "product_link_mutation_test"
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
	if err := db.AutoMigrate(&models.Category{}, &models.Product{}, &models.CoreProductLink{}, &models.Supplier{}, &models.PurchaseOrder{}, &models.PurchaseOrderLine{}, &models.Receipt{}, &models.Activity{}, &models.IdempotencyRecord{}); err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`CREATE TABLE products (productID BIGINT PRIMARY KEY,product_code TEXT,name TEXT,lifecycle_status TEXT)`,
		`INSERT INTO products VALUES(11,'NODE-1','Node','active'),(12,'NODE-2','Other Node','active')`,
		`CREATE TABLE audit_log (id BIGSERIAL PRIMARY KEY,user_id BIGINT,action TEXT,entity_type TEXT,entity_id TEXT,old_values JSONB,new_values JSONB,ip_address TEXT,user_agent TEXT)`,
	} {
		if err := db.Exec(statement).Error; err != nil {
			t.Fatal(err)
		}
	}
	product := models.Product{SKU: "NODE-1", Name: "Node", Active: true, Unit: "Stk.", Parameters: json.RawMessage("{}"), Attributes: json.RawMessage("{}")}
	if err := db.Create(&product).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.First(&product, product.ID).Error; err != nil {
		t.Fatal(err)
	}
	other := models.Product{SKU: "NODE-2", Name: "Other Node", Active: true, Unit: "Stk.", Parameters: json.RawMessage("{}"), Attributes: json.RawMessage("{}")}
	if err := db.Create(&other).Error; err != nil {
		t.Fatal(err)
	}
	h := &Handler{db: db}
	call := func(id uint, key string, warehouseID int64, version string) *httptest.ResponseRecorder {
		payload := map[string]any{"warehouseProductId": warehouseID}
		if version != "" {
			payload["expectedUpdatedAt"] = version
		}
		body, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		r := httptest.NewRequest(http.MethodPost, "/products/"+strconv.FormatUint(uint64(id), 10)+"/warehouse-link", bytes.NewReader(body))
		r.Header.Set("X-Cores-Origin", "MCP/AI")
		r.Header.Set("Idempotency-Key", key)
		route := chi.NewRouteContext()
		route.URLParams.Add("id", strconv.FormatUint(uint64(id), 10))
		r = r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, route))
		w := httptest.NewRecorder()
		h.linkWarehouseProduct(w, r)
		return w
	}
	if response := call(product.ID, "link-no-version", 11, ""); response.Code != http.StatusPreconditionRequired {
		t.Fatalf("missing version: %d %s", response.Code, response.Body.String())
	}
	created := call(product.ID, "link-create-001", 11, product.UpdatedAt.UTC().Format(time.RFC3339Nano))
	if created.Code != http.StatusOK {
		t.Fatalf("create link: %d %s", created.Code, created.Body.String())
	}
	if replay := call(product.ID, "link-create-001", 11, product.UpdatedAt.UTC().Format(time.RFC3339Nano)); replay.Code != http.StatusOK || replay.Body.String() != created.Body.String() {
		t.Fatalf("replay: %d %s", replay.Code, replay.Body.String())
	}
	var link models.CoreProductLink
	if err := json.Unmarshal(created.Body.Bytes(), &link); err != nil {
		t.Fatal(err)
	}
	if link.WarehouseProductID != 11 {
		t.Fatalf("bad link: %#v", link)
	}
	if response := call(other.ID, "link-taken-001", 11, other.UpdatedAt.UTC().Format(time.RFC3339Nano)); response.Code != http.StatusConflict {
		t.Fatalf("duplicate target accepted: %d %s", response.Code, response.Body.String())
	}
	if response := call(product.ID, "link-stale-001", 12, product.UpdatedAt.UTC().Format(time.RFC3339Nano)); response.Code != http.StatusConflict {
		t.Fatalf("stale link accepted: %d %s", response.Code, response.Body.String())
	}
	updated := call(product.ID, "link-update-001", 12, link.UpdatedAt.UTC().Format(time.RFC3339Nano))
	if updated.Code != http.StatusOK {
		t.Fatalf("relink: %d %s", updated.Code, updated.Body.String())
	}
	if err := json.Unmarshal(updated.Body.Bytes(), &link); err != nil || link.WarehouseProductID != 12 {
		t.Fatalf("bad relink: %#v %v", link, err)
	}
	supplier := models.Supplier{Name: "Stage Supply", Code: "STAGE", Active: true}
	if err := db.Create(&supplier).Error; err != nil {
		t.Fatal(err)
	}
	order := models.PurchaseOrder{Number: "PO-1", SupplierID: supplier.ID, Status: "sent", Currency: "EUR"}
	if err := db.Create(&order).Error; err != nil {
		t.Fatal(err)
	}
	line := models.PurchaseOrderLine{PurchaseOrderID: order.ID, ProductID: &product.ID, Description: "Node", Quantity: 1, Unit: "Stk.", UnitPriceCents: 1000}
	if err := db.Create(&line).Error; err != nil {
		t.Fatal(err)
	}
	if response := call(product.ID, "link-open-order", 11, link.UpdatedAt.UTC().Format(time.RFC3339Nano)); response.Code != http.StatusConflict {
		t.Fatalf("open order did not block relink: %d %s", response.Code, response.Body.String())
	}
	for table, want := range map[string]int64{"core_product_links": 1, "audit_log": 2, "proc_activities": 2, "proc_idempotency_records": 2} {
		var count int64
		if err := db.Table(table).Count(&count).Error; err != nil || count != want {
			t.Fatalf("%s count=%d err=%v want=%d", table, count, err, want)
		}
	}
}
