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

func TestCategoryMutationCommitsAuditAndHonorsVersion(t *testing.T) {
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
	const schema = "category_mutation_test"
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
	if err := db.AutoMigrate(&models.Category{}, &models.Activity{}, &models.IdempotencyRecord{}); err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`CREATE TABLE audit_log (id BIGSERIAL PRIMARY KEY, user_id BIGINT, action TEXT, entity_type TEXT, entity_id TEXT, old_values JSONB, new_values JSONB, ip_address TEXT, user_agent TEXT)`).Error; err != nil {
		t.Fatal(err)
	}
	h := &Handler{db: db}
	call := func(method string, id uint, key string, payload map[string]any) *httptest.ResponseRecorder {
		body, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		path := "/categories"
		if id != 0 {
			path += "/" + strconv.FormatUint(uint64(id), 10)
		}
		r := httptest.NewRequest(method, path, bytes.NewReader(body))
		r.Header.Set("X-Cores-Origin", "MCP/AI")
		r.Header.Set("Idempotency-Key", key)
		w := httptest.NewRecorder()
		if method == http.MethodPost {
			h.createCategory(w, r)
		} else {
			route := chi.NewRouteContext()
			route.URLParams.Add("id", strconv.FormatUint(uint64(id), 10))
			r = r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, route))
			h.updateCategory(w, r)
		}
		return w
	}
	schemaValue := []map[string]any{{"key": "power", "label": "Power", "type": "number", "unit": "W"}}
	createPayload := map[string]any{"name": "Lighting", "description": "Lighting fixtures", "parameterSchema": schemaValue}
	createdResponse := call(http.MethodPost, 0, "category-create-001", createPayload)
	if createdResponse.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", createdResponse.Code, createdResponse.Body.String())
	}
	var created models.Category
	if err := json.Unmarshal(createdResponse.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if created.ID == 0 || !bytes.Contains(created.ParameterSchema, []byte(`"power"`)) {
		t.Fatalf("created category: %#v", created)
	}
	if replay := call(http.MethodPost, 0, "category-create-001", createPayload); replay.Code != http.StatusCreated || replay.Body.String() != createdResponse.Body.String() {
		t.Fatalf("create replay: %d %s", replay.Code, replay.Body.String())
	}
	if conflict := call(http.MethodPost, 0, "category-create-001", map[string]any{"name": "Different"}); conflict.Code != http.StatusConflict {
		t.Fatalf("reused key accepted different payload: %d", conflict.Code)
	}
	if err := db.First(&created, created.ID).Error; err != nil {
		t.Fatal(err)
	}
	updatePayload := map[string]any{"name": "Stage Lighting", "description": "Fixtures", "parameterSchema": schemaValue, "expectedUpdatedAt": created.UpdatedAt}
	updatedResponse := call(http.MethodPut, created.ID, "category-update-001", updatePayload)
	if updatedResponse.Code != http.StatusOK {
		t.Fatalf("update: %d %s", updatedResponse.Code, updatedResponse.Body.String())
	}
	var updated models.Category
	if err := json.Unmarshal(updatedResponse.Body.Bytes(), &updated); err != nil || updated.Name != "Stage Lighting" || !updated.UpdatedAt.After(created.UpdatedAt) {
		t.Fatalf("updated category: %#v, %v", updated, err)
	}
	if replay := call(http.MethodPut, created.ID, "category-update-001", updatePayload); replay.Code != http.StatusOK {
		t.Fatalf("update replay: %d %s", replay.Code, replay.Body.String())
	} else {
		var repeated models.Category
		if err := json.Unmarshal(replay.Body.Bytes(), &repeated); err != nil || repeated.ID != updated.ID || repeated.Name != updated.Name || !repeated.UpdatedAt.Equal(updated.UpdatedAt) {
			t.Fatalf("update replay differed: %#v, %v", repeated, err)
		}
	}
	stale := map[string]any{"name": "Old version", "parameterSchema": schemaValue, "expectedUpdatedAt": created.UpdatedAt}
	if response := call(http.MethodPut, created.ID, "category-update-002", stale); response.Code != http.StatusConflict {
		t.Fatalf("stale version accepted: %d %s", response.Code, response.Body.String())
	}
	for table, want := range map[string]int64{"proc_categories": 1, "proc_activities": 2, "proc_idempotency_records": 2, "audit_log": 2} {
		var count int64
		if err := db.Table(table).Count(&count).Error; err != nil || count != want {
			t.Fatalf("%s count=%d err=%v, want %d", table, count, err, want)
		}
	}
	if err := db.Exec("DROP TABLE audit_log").Error; err != nil {
		t.Fatal(err)
	}
	if response := call(http.MethodPost, 0, "category-create-002", map[string]any{"name": "Video"}); response.Code != http.StatusInternalServerError {
		t.Fatalf("missing audit must roll back creation: %d", response.Code)
	}
	if response := call(http.MethodPut, created.ID, "category-update-003", map[string]any{"name": "Changed", "expectedUpdatedAt": updated.UpdatedAt}); response.Code != http.StatusInternalServerError {
		t.Fatalf("missing audit must roll back update: %d", response.Code)
	}
	var persisted models.Category
	if err := db.First(&persisted, created.ID).Error; err != nil || persisted.Name != updated.Name {
		t.Fatalf("failed update changed category: %#v, %v", persisted, err)
	}
	var count int64
	if err := db.Table("proc_categories").Count(&count).Error; err != nil || count != 1 {
		t.Fatalf("failed creation left a category: %d, %v", count, err)
	}
}
