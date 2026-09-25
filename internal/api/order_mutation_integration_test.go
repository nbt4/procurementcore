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

func TestOrderCreateAndTransitionVersionAuditReplay(t *testing.T) {
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
	const schema = "order_mutation_test"
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
	if err := db.AutoMigrate(&models.Category{}, &models.Product{}, &models.Supplier{}, &models.PurchaseOrder{}, &models.PurchaseOrderLine{}, &models.Receipt{}, &models.Activity{}, &models.IdempotencyRecord{}); err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`CREATE TABLE audit_log (id BIGSERIAL PRIMARY KEY,user_id BIGINT,action TEXT,entity_type TEXT,entity_id TEXT,old_values JSONB,new_values JSONB,ip_address TEXT,user_agent TEXT)`).Error; err != nil {
		t.Fatal(err)
	}
	supplier := models.Supplier{Name: "Stage Supply", Code: "STAGE", Active: true}
	if err := db.Create(&supplier).Error; err != nil {
		t.Fatal(err)
	}
	h := &Handler{db: db}
	call := func(method, path, key string, payload map[string]any) *httptest.ResponseRecorder {
		body, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		r := httptest.NewRequest(method, path, bytes.NewReader(body))
		r.Header.Set("X-Cores-Origin", "MCP/AI")
		r.Header.Set("Idempotency-Key", key)
		parts := strings.Split(path, "/")
		if len(parts) > 4 {
			route := chi.NewRouteContext()
			route.URLParams.Add("id", parts[4])
			r = r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, route))
		}
		w := httptest.NewRecorder()
		if method == http.MethodPost && path == "/api/v1/orders" {
			h.createOrder(w, r)
		} else if method == http.MethodPost {
			h.receiveOrder(w, r)
		} else if strings.HasSuffix(path, "/draft") {
			h.updateOrderDraft(w, r)
		} else {
			h.updateOrder(w, r)
		}
		return w
	}
	path := "/api/v1/orders"
	payload := map[string]any{"supplierId": supplier.ID, "status": "draft", "currency": "EUR", "lines": []any{map[string]any{"description": "Cable", "quantity": 2, "unitPriceCents": 1000, "receivedQuantity": 2}}}
	bad := map[string]any{"supplierId": supplier.ID, "status": "sent", "lines": payload["lines"]}
	if result := call(http.MethodPost, path, "order-create-sent", bad); result.Code != http.StatusBadRequest {
		t.Fatalf("MCP sent order accepted: %d %s", result.Code, result.Body.String())
	}
	created := call(http.MethodPost, path, "order-create-001", payload)
	if created.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", created.Code, created.Body.String())
	}
	if replay := call(http.MethodPost, path, "order-create-001", payload); replay.Code != http.StatusCreated || replay.Body.String() != created.Body.String() {
		t.Fatalf("create replay: %d %s", replay.Code, replay.Body.String())
	}
	var row models.PurchaseOrder
	if err := json.Unmarshal(created.Body.Bytes(), &row); err != nil {
		t.Fatal(err)
	}
	if row.ID == 0 || len(row.Lines) != 1 || row.Lines[0].ReceivedQuantity != 0 {
		t.Fatalf("unsafe create: %#v", row)
	}
	path += "/" + strconv.FormatUint(uint64(row.ID), 10)
	draftPath := path + "/draft"
	draftUpdate := map[string]any{"supplierId": supplier.ID, "status": "draft", "currency": "USD", "supplierOrderNumber": "S-17", "notes": "Updated draft", "lines": []any{map[string]any{"description": "Replacement cable", "quantity": 3, "unitPriceCents": 1200, "receivedQuantity": 3}, map[string]any{"description": "Connector", "quantity": 1, "unitPriceCents": 400}}}
	if result := call(http.MethodPut, draftPath, "order-draft-no-version", draftUpdate); result.Code != http.StatusPreconditionRequired {
		t.Fatalf("draft update without version: %d %s", result.Code, result.Body.String())
	}
	draftUpdate["expectedUpdatedAt"] = row.UpdatedAt.UTC().Format(time.RFC3339Nano)
	draftChanged := call(http.MethodPut, draftPath, "order-draft-001", draftUpdate)
	if draftChanged.Code != http.StatusOK {
		t.Fatalf("draft update: %d %s", draftChanged.Code, draftChanged.Body.String())
	}
	if replay := call(http.MethodPut, draftPath, "order-draft-001", draftUpdate); replay.Code != http.StatusOK || replay.Body.String() != draftChanged.Body.String() {
		t.Fatalf("draft update replay: %d %s", replay.Code, replay.Body.String())
	}
	if stale := call(http.MethodPut, draftPath, "order-draft-stale", draftUpdate); stale.Code != http.StatusConflict {
		t.Fatalf("stale draft update: %d %s", stale.Code, stale.Body.String())
	}
	if err := json.Unmarshal(draftChanged.Body.Bytes(), &row); err != nil {
		t.Fatal(err)
	}
	if row.Currency != "USD" || row.TotalCents != 4000 || len(row.Lines) != 2 || row.Lines[0].ReceivedQuantity != 0 {
		t.Fatalf("incorrect draft replacement: %#v", row)
	}
	update := map[string]any{"status": "sent", "supplierOrderNumber": "S-17", "notes": "Dispatched"}
	if result := call(http.MethodPut, path, "order-update-no-version", update); result.Code != http.StatusPreconditionRequired {
		t.Fatalf("missing version: %d %s", result.Code, result.Body.String())
	}
	update["expectedUpdatedAt"] = row.UpdatedAt.UTC().Format(time.RFC3339Nano)
	sent := call(http.MethodPut, path, "order-send-001", update)
	if sent.Code != http.StatusOK {
		t.Fatalf("send: %d %s", sent.Code, sent.Body.String())
	}
	if replay := call(http.MethodPut, path, "order-send-001", update); replay.Code != http.StatusOK || replay.Body.String() != sent.Body.String() {
		t.Fatalf("send replay: %d %s", replay.Code, replay.Body.String())
	}
	if err := json.Unmarshal(sent.Body.Bytes(), &row); err != nil {
		t.Fatal(err)
	}
	draftUpdate["expectedUpdatedAt"] = row.UpdatedAt.UTC().Format(time.RFC3339Nano)
	if result := call(http.MethodPut, draftPath, "order-draft-after-send", draftUpdate); result.Code != http.StatusConflict {
		t.Fatalf("sent order draft changed: %d %s", result.Code, result.Body.String())
	}
	receiptPayload := map[string]any{"lineId": row.Lines[0].ID, "quantity": 1, "note": "Box one", "expectedUpdatedAt": row.UpdatedAt.UTC().Format(time.RFC3339Nano)}
	received := call(http.MethodPost, path+"/receipt", "order-receipt-001", receiptPayload)
	if received.Code != http.StatusCreated {
		t.Fatalf("receipt: %d %s", received.Code, received.Body.String())
	}
	if replay := call(http.MethodPost, path+"/receipt", "order-receipt-001", receiptPayload); replay.Code != http.StatusCreated || replay.Body.String() != received.Body.String() {
		t.Fatalf("receipt replay: %d %s", replay.Code, replay.Body.String())
	}
	if err := db.First(&row, row.ID).Error; err != nil {
		t.Fatal(err)
	}
	if row.Status != "partially_received" {
		t.Fatalf("partial receipt did not update order: %#v", row)
	}
	update["status"] = "draft"
	update["expectedUpdatedAt"] = row.UpdatedAt.UTC().Format(time.RFC3339Nano)
	if result := call(http.MethodPut, path, "order-backwards", update); result.Code != http.StatusConflict {
		t.Fatalf("backwards transition: %d %s", result.Code, result.Body.String())
	}
	update["status"] = "cancelled"
	if result := call(http.MethodPut, path, "order-cancel-001", update); result.Code != http.StatusOK {
		t.Fatalf("cancel: %d %s", result.Code, result.Body.String())
	}
	for table, want := range map[string]int64{"proc_purchase_orders": 1, "proc_purchase_order_lines": 2, "proc_receipts": 1, "audit_log": 5, "proc_activities": 5, "proc_idempotency_records": 5} {
		var count int64
		if err := db.Table(table).Count(&count).Error; err != nil || count != want {
			t.Fatalf("%s count=%d err=%v want=%d", table, count, err, want)
		}
	}
}
