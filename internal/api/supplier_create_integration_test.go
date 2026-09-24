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

	"procurementcore/internal/models"

	"github.com/go-chi/chi/v5"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func TestCreateSupplierCommitsAuditAndReplaysOnce(t *testing.T) {
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
	schema := "supplier_create_test"
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
	if err := db.AutoMigrate(&models.Supplier{}, &models.Activity{}, &models.IdempotencyRecord{}); err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`CREATE TABLE audit_log (id BIGSERIAL PRIMARY KEY, user_id BIGINT, action TEXT, entity_type TEXT, entity_id TEXT, old_values JSONB, new_values JSONB, ip_address TEXT, user_agent TEXT)`).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`CREATE TABLE proc_purchase_orders (id BIGSERIAL PRIMARY KEY, supplier_id BIGINT, status TEXT)`).Error; err != nil {
		t.Fatal(err)
	}
	h := &Handler{db: db}
	request := func(code, key string, active *bool) *httptest.ResponseRecorder {
		payload := map[string]any{"name": "Test Supplier", "code": code, "riskLevel": "low"}
		if active != nil {
			payload["active"] = *active
		}
		body, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		r := httptest.NewRequest(http.MethodPost, "/suppliers", bytes.NewReader(body))
		r.Header.Set("X-Cores-Origin", "MCP/AI")
		r.Header.Set("Idempotency-Key", key)
		w := httptest.NewRecorder()
		h.createSupplier(w, r)
		return w
	}
	inactive := false
	first := request("TEST-001", "supplier-create-test-001", &inactive)
	if first.Code != http.StatusCreated {
		t.Fatalf("first create: %d %s", first.Code, first.Body.String())
	}
	var created models.Supplier
	if err := json.Unmarshal(first.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if created.ID == 0 || created.Active {
		t.Fatalf("explicitly inactive supplier was not preserved: %#v", created)
	}
	replay := request("TEST-001", "supplier-create-test-001", &inactive)
	if replay.Code != http.StatusCreated || replay.Body.String() != first.Body.String() {
		t.Fatalf("idempotent replay: %d %s", replay.Code, replay.Body.String())
	}
	if conflict := request("TEST-002", "supplier-create-test-001", &inactive); conflict.Code != http.StatusConflict {
		t.Fatalf("changed payload reused idempotency key: %d", conflict.Code)
	}
	for table, want := range map[string]int64{"proc_suppliers": 1, "proc_activities": 1, "proc_idempotency_records": 1, "audit_log": 1} {
		var count int64
		if err := db.Table(table).Count(&count).Error; err != nil || count != want {
			t.Fatalf("%s count=%d err=%v, want %d", table, count, err, want)
		}
	}
	var audit struct{ NewValues json.RawMessage }
	if err := db.Table("audit_log").Select("new_values").Scan(&audit).Error; err != nil || !bytes.Contains(audit.NewValues, []byte(`"MCP/AI"`)) {
		t.Fatalf("missing MCP origin in audit: %s, %v", audit.NewValues, err)
	}
	defaultActive := request("TEST-002", "supplier-create-test-002", nil)
	if defaultActive.Code != http.StatusCreated {
		t.Fatalf("default-active create: %d %s", defaultActive.Code, defaultActive.Body.String())
	}
	var withDefault models.Supplier
	if err := json.Unmarshal(defaultActive.Body.Bytes(), &withDefault); err != nil || !withDefault.Active {
		t.Fatalf("omitted active must default to true: %#v, %v", withDefault, err)
	}
	if err := db.First(&withDefault, withDefault.ID).Error; err != nil {
		t.Fatal(err)
	}
	update := func(name, key string, expected any) *httptest.ResponseRecorder {
		payload := map[string]any{"name": name, "code": withDefault.Code, "riskLevel": "low", "active": false, "expectedUpdatedAt": expected}
		body, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		r := httptest.NewRequest(http.MethodPut, "/suppliers/"+strconv.FormatUint(uint64(withDefault.ID), 10), bytes.NewReader(body))
		r.Header.Set("X-Cores-Origin", "MCP/AI")
		r.Header.Set("Idempotency-Key", key)
		route := chi.NewRouteContext()
		route.URLParams.Add("id", strconv.FormatUint(uint64(withDefault.ID), 10))
		r = r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, route))
		w := httptest.NewRecorder()
		h.updateSupplier(w, r)
		return w
	}
	if err := db.Exec("INSERT INTO proc_purchase_orders (supplier_id,status) VALUES (?, 'draft')", withDefault.ID).Error; err != nil {
		t.Fatal(err)
	}
	if blocked := update("Renamed Supplier", "supplier-update-blocked", withDefault.UpdatedAt); blocked.Code != http.StatusConflict || !strings.Contains(blocked.Body.String(), "supplier_open_orders") {
		t.Fatalf("open order did not block deactivation: %d %s", blocked.Code, blocked.Body.String())
	}
	if err := db.Exec("DELETE FROM proc_purchase_orders WHERE supplier_id = ?", withDefault.ID).Error; err != nil {
		t.Fatal(err)
	}
	changed := update("Renamed Supplier", "supplier-update-test-001", withDefault.UpdatedAt)
	if changed.Code != http.StatusOK {
		t.Fatalf("update: %d %s", changed.Code, changed.Body.String())
	}
	var updated models.Supplier
	if err := json.Unmarshal(changed.Body.Bytes(), &updated); err != nil || updated.Name != "Renamed Supplier" || updated.Active || !updated.UpdatedAt.After(withDefault.UpdatedAt) {
		t.Fatalf("updated supplier: %#v, %v", updated, err)
	}
	if err := db.First(&updated, updated.ID).Error; err != nil {
		t.Fatal(err)
	}
	if replay := update("Renamed Supplier", "supplier-update-test-001", withDefault.UpdatedAt); replay.Code != http.StatusOK || replay.Body.String() != changed.Body.String() {
		t.Fatalf("update replay: %d %s", replay.Code, replay.Body.String())
	}
	if stale := update("Stale Supplier", "supplier-update-test-002", withDefault.UpdatedAt); stale.Code != http.StatusConflict {
		t.Fatalf("stale update: %d %s", stale.Code, stale.Body.String())
	}
	for table, want := range map[string]int64{"proc_activities": 3, "audit_log": 3, "proc_idempotency_records": 3} {
		var count int64
		if err := db.Table(table).Count(&count).Error; err != nil || count != want {
			t.Fatalf("%s count=%d err=%v, want %d", table, count, err, want)
		}
	}
	var lastAction string
	if err := db.Table("audit_log").Select("action").Order("id DESC").Limit(1).Scan(&lastAction).Error; err != nil || lastAction != "supplier.deactivate" {
		t.Fatalf("deactivation audit action = %q, %v", lastAction, err)
	}
	if err := db.Exec("DROP TABLE audit_log").Error; err != nil {
		t.Fatal(err)
	}
	if failed := update("Lost Audit", "supplier-update-test-003", updated.UpdatedAt); failed.Code != http.StatusInternalServerError {
		t.Fatalf("missing audit did not abort update: %d", failed.Code)
	}
	var persisted models.Supplier
	if err := db.First(&persisted, withDefault.ID).Error; err != nil || persisted.Name != updated.Name {
		t.Fatalf("failed update changed supplier: %#v, %v", persisted, err)
	}
	if failed := request("TEST-003", "supplier-create-test-003", nil); failed.Code != http.StatusInternalServerError {
		t.Fatalf("missing audit did not abort create: %d", failed.Code)
	}
	var count int64
	if err := db.Table("proc_suppliers").Count(&count).Error; err != nil || count != 2 {
		t.Fatalf("supplier was not rolled back: count=%d err=%v", count, err)
	}
}
