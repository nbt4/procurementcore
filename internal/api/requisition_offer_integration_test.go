package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"

	"procurementcore/internal/models"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func TestOfferRequisitionCreatesProductsAndRollsBackTogether(t *testing.T) {
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
	const schema = "offer_requisition_test"
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
	if err := db.AutoMigrate(&models.Category{}, &models.Product{}, &models.Supplier{}, &models.Offer{}, &models.PriceHistory{}, &models.PriceAlert{}, &models.Requisition{}, &models.RequisitionLine{}, &models.Activity{}, &models.IdempotencyRecord{}); err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`CREATE TABLE audit_log (id BIGSERIAL PRIMARY KEY,user_id BIGINT,action TEXT,entity_type TEXT,entity_id TEXT,old_values JSONB,new_values JSONB,ip_address TEXT,user_agent TEXT)`).Error; err != nil {
		t.Fatal(err)
	}
	supplier := models.Supplier{Name: "Adam Hall GmbH", Code: "ADAM-HALL", Active: true}
	if err := db.Create(&supplier).Error; err != nil {
		t.Fatal(err)
	}
	h := &Handler{db: db}
	call := func(sku string) *httptest.ResponseRecorder {
		payload := map[string]any{
			"title": "Bedarf aus Angebot", "supplierId": supplier.ID, "offerNumber": "AH-4711", "sourceFileName": "Angebot.pdf", "currency": "EUR",
			"lines": []any{map[string]any{"description": "Adapterkabel", "quantity": 2, "unit": "Stk.", "estimatedPriceCents": 1760, "createProduct": true, "sku": sku, "productName": "Adapterkabel", "manufacturer": "Adam Hall"}},
		}
		body, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		r := httptest.NewRequest(http.MethodPost, "/requisitions/from-offer", bytes.NewReader(body))
		w := httptest.NewRecorder()
		h.createRequisitionFromOffer(w, r)
		return w
	}
	created := call("K4TPP0300")
	if created.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", created.Code, created.Body.String())
	}
	var row models.Requisition
	if err := json.Unmarshal(created.Body.Bytes(), &row); err != nil {
		t.Fatal(err)
	}
	if len(row.Lines) != 1 || row.Lines[0].ProductID == nil || row.Lines[0].PreferredSupplierID == nil || *row.Lines[0].PreferredSupplierID != supplier.ID || !strings.Contains(row.Justification, "AH-4711") {
		t.Fatalf("requisition: %+v", row)
	}
	var products, offers, requisitions int64
	db.Model(&models.Product{}).Count(&products)
	db.Model(&models.Offer{}).Count(&offers)
	db.Model(&models.Requisition{}).Count(&requisitions)
	if products != 1 || offers != 1 || requisitions != 1 {
		t.Fatalf("after create: products=%d offers=%d requisitions=%d", products, offers, requisitions)
	}
	duplicate := call("K4TPP0300")
	if duplicate.Code < 400 {
		t.Fatalf("duplicate accepted: %d %s", duplicate.Code, duplicate.Body.String())
	}
	db.Model(&models.Product{}).Count(&products)
	db.Model(&models.Requisition{}).Count(&requisitions)
	if products != 1 || requisitions != 1 {
		t.Fatalf("failed transaction leaked rows: products=%d requisitions=%d", products, requisitions)
	}
}
