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

func TestRequisitionDraftMutationVersionAuditAndReplay(t *testing.T) {
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
	const schema = "requisition_mutation_test"
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
	if err := db.AutoMigrate(&models.Category{}, &models.Product{}, &models.Supplier{}, &models.Requisition{}, &models.RequisitionLine{}, &models.Activity{}, &models.IdempotencyRecord{}); err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`CREATE TABLE audit_log (id BIGSERIAL PRIMARY KEY,user_id BIGINT,action TEXT,entity_type TEXT,entity_id TEXT,old_values JSONB,new_values JSONB,ip_address TEXT,user_agent TEXT)`).Error; err != nil {
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
		switch {
		case method == http.MethodPost && path == "/api/v1/requisitions":
			h.createRequisition(w, r)
		case method == http.MethodPut:
			h.updateRequisition(w, r)
		default:
			h.submitRequisition(w, r)
		}
		return w
	}
	line := map[string]any{"description": "DMX cable", "quantity": 2, "unit": "Stk.", "estimatedPriceCents": 1250}
	createPayload := map[string]any{"title": "Tour cable", "costCenter": "EVENT", "lines": []any{line}}
	badReference := map[string]any{"title": "Invalid cable", "lines": []any{map[string]any{"productId": 999, "description": "Unknown", "quantity": 1, "estimatedPriceCents": 100}}}
	if response := call(http.MethodPost, "/api/v1/requisitions", "requisition-invalid-product", badReference); response.Code != http.StatusConflict {
		t.Fatalf("invalid product accepted: %d %s", response.Code, response.Body.String())
	}
	created := call(http.MethodPost, "/api/v1/requisitions", "requisition-create-001", createPayload)
	if created.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", created.Code, created.Body.String())
	}
	if replay := call(http.MethodPost, "/api/v1/requisitions", "requisition-create-001", createPayload); replay.Code != http.StatusCreated || replay.Body.String() != created.Body.String() {
		t.Fatalf("create replay: %d %s", replay.Code, replay.Body.String())
	}
	var row models.Requisition
	if err := json.Unmarshal(created.Body.Bytes(), &row); err != nil {
		t.Fatal(err)
	}
	if row.ID == 0 || row.Status != "draft" || row.EstimatedTotalCents != 2500 {
		t.Fatalf("bad draft: %#v", row)
	}
	path := "/api/v1/requisitions/" + strconv.FormatUint(uint64(row.ID), 10)
	updatePayload := map[string]any{"title": "Tour cables", "costCenter": "EVENT", "lines": []any{line}, "expectedUpdatedAt": row.UpdatedAt.Add(-time.Second).UTC().Format(time.RFC3339Nano)}
	delete(updatePayload, "expectedUpdatedAt")
	if response := call(http.MethodPut, path, "requisition-update-no-version", updatePayload); response.Code != http.StatusPreconditionRequired {
		t.Fatalf("missing update version: %d %s", response.Code, response.Body.String())
	}
	updatePayload["expectedUpdatedAt"] = row.UpdatedAt.Add(-time.Second).UTC().Format(time.RFC3339Nano)
	if response := call(http.MethodPut, path, "requisition-update-stale", updatePayload); response.Code != http.StatusConflict {
		t.Fatalf("stale update: %d %s", response.Code, response.Body.String())
	}
	updatePayload["expectedUpdatedAt"] = row.UpdatedAt.UTC().Format(time.RFC3339Nano)
	updated := call(http.MethodPut, path, "requisition-update-001", updatePayload)
	if updated.Code != http.StatusOK {
		t.Fatalf("update: %d %s", updated.Code, updated.Body.String())
	}
	if replay := call(http.MethodPut, path, "requisition-update-001", updatePayload); replay.Code != http.StatusOK || replay.Body.String() != updated.Body.String() {
		t.Fatalf("update replay: %d %s", replay.Code, replay.Body.String())
	}
	if err := json.Unmarshal(updated.Body.Bytes(), &row); err != nil {
		t.Fatal(err)
	}
	if row.Title != "Tour cables" || len(row.Lines) != 1 {
		t.Fatalf("bad update: %#v", row)
	}
	submitPayload := map[string]any{"expectedUpdatedAt": row.UpdatedAt.UTC().Format(time.RFC3339Nano)}
	submitted := call(http.MethodPost, path+"/submit", "requisition-submit-001", submitPayload)
	if submitted.Code != http.StatusOK {
		t.Fatalf("submit: %d %s", submitted.Code, submitted.Body.String())
	}
	if replay := call(http.MethodPost, path+"/submit", "requisition-submit-001", submitPayload); replay.Code != http.StatusOK || replay.Body.String() != submitted.Body.String() {
		t.Fatalf("submit replay: %d %s", replay.Code, replay.Body.String())
	}
	if changed := call(http.MethodPut, path, "requisition-update-after-submit", updatePayload); changed.Code != http.StatusForbidden {
		t.Fatalf("submitted requisition changed: %d %s", changed.Code, changed.Body.String())
	}
	for table, want := range map[string]int64{"proc_requisitions": 1, "proc_requisition_lines": 1, "audit_log": 3, "proc_activities": 3, "proc_idempotency_records": 3} {
		var count int64
		if err := db.Table(table).Count(&count).Error; err != nil || count != want {
			t.Fatalf("%s count=%d err=%v want=%d", table, count, err, want)
		}
	}
}
