package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"procurementcore/internal/auth"
	"procurementcore/internal/database"
	"procurementcore/internal/models"

	"github.com/golang-jwt/jwt/v5"
	commonjwt "github.com/nbt4/cores-common/pkg/jwt"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func TestMasterLifecycleOwnerAtomicReplayAndRetainedFields(t *testing.T) {
	dsn := os.Getenv("PROCUREMENT_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("disposable PostgreSQL required")
	}
	u, err := url.Parse(dsn)
	if err != nil || !strings.HasSuffix(u.Path, "_test") {
		t.Fatal("dedicated _test database required")
	}
	fixture, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	fixtureSQL, err := fixture.DB()
	if err != nil {
		t.Fatal(err)
	}
	defer fixtureSQL.Close()
	const schema = "procurement_master_lifecycle_test"
	must := func(db *gorm.DB, q string, args ...any) {
		t.Helper()
		if err := db.Exec(q, args...).Error; err != nil {
			t.Fatal(err)
		}
	}
	must(fixture, "DROP SCHEMA IF EXISTS "+schema+" CASCADE;CREATE SCHEMA "+schema)
	defer fixture.Exec("DROP SCHEMA IF EXISTS " + schema + " CASCADE")
	params := u.Query()
	params.Set("search_path", schema)
	u.RawQuery = params.Encode()
	db, err := database.Open(u.String())
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	defer sqlDB.Close()
	must(db, `CREATE TABLE users(userid BIGINT PRIMARY KEY,username TEXT,is_active BOOLEAN,is_admin BOOLEAN);INSERT INTO users VALUES(1,'admin',true,true),(2,'member',true,false);CREATE TABLE audit_log(id BIGSERIAL PRIMARY KEY,user_id BIGINT,action TEXT,entity_type TEXT,entity_id TEXT,old_values JSONB,new_values JSONB,ip_address TEXT,user_agent TEXT)`)
	supplier := models.Supplier{Name: "Fixture supplier", Code: "FIXTURE", Active: true, RiskLevel: "low", Notes: "retained note", PaymentTerms: "30 days"}
	category := models.Category{Name: "Fixture category", Description: "Retained category details", ParameterSchema: json.RawMessage(`[{"key":"voltage","label":"Voltage","type":"number","unit":"V"}]`), Active: true}
	if err := db.Create(&category).Error; err != nil {
		t.Fatal(err)
	}
	product := models.Product{SKU: "FIXTURE", Name: "Fixture product", CategoryID: &category.ID, Active: true, Unit: "Stk.", Parameters: json.RawMessage(`{"voltage":230}`), Attributes: json.RawMessage(`{"color":"blue"}`)}
	if err := db.Create(&supplier).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&product).Error; err != nil {
		t.Fatal(err)
	}
	offer := models.Offer{ProductID: product.ID, SupplierID: supplier.ID, SupplierSKU: "SUP-1", PriceCents: 1234, Currency: "EUR", MinimumQuantity: 2, PackSize: 3, Active: true}
	if err := db.Create(&offer).Error; err != nil {
		t.Fatal(err)
	}
	t.Setenv("CORES_JWT_SECRET", "master-lifecycle-integration-secret-32-bytes")
	handler := auth.Middleware(commonjwt.DatabaseUserLookup(sqlDB), (&Handler{db: db}).Routes())
	call := func(ns, op string, uid uint, scope, key string, payload map[string]any) (int, map[string]any, string) {
		t.Helper()
		raw, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		signed, err := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{"uid": uid, "username": "admin", "is_admin": true, "mcp_scope": scope, "exp": time.Now().Add(time.Minute).Unix()}).SignedString(commonjwt.JWTSecret())
		if err != nil {
			t.Fatal(err)
		}
		r := httptest.NewRequest(http.MethodPost, "/mcp/master-data/"+ns+"/"+op, bytes.NewReader(raw))
		r.AddCookie(&http.Cookie{Name: "cores_token", Value: signed})
		r.Header.Set("X-Cores-Origin", "MCP/AI")
		if key != "" {
			r.Header.Set("Idempotency-Key", key)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		out := map[string]any{}
		_ = json.Unmarshal(w.Body.Bytes(), &out)
		return w.Code, out, w.Body.String()
	}
	scope := "cores:procurement:archive"
	review := func(ns, op string, id uint) map[string]any {
		t.Helper()
		status, out, raw := call(ns, op, 1, scope, "", map[string]any{"id": id, "preview": true})
		if status != 200 {
			t.Fatalf("preview: %d %s", status, raw)
		}
		return out
	}
	final := func(id uint, p map[string]any) map[string]any {
		return map[string]any{"id": id, "expected_updated_at": p["expected_updated_at"], "expected_context": p["expected_context"], "confirmation_text": p["required_confirmation_text"], "confirm_change": true}
	}
	execute := func(ns, op, key string, id uint, p map[string]any) string {
		t.Helper()
		status, out, raw := call(ns, op, 1, scope, key, final(id, p))
		want := "archived"
		if op == "restore" {
			want = "restored"
		}
		if status != 200 || out["operation_status"] != want {
			t.Fatalf("execute %s/%s: %d %s", ns, op, status, raw)
		}
		return raw
	}
	for _, uid := range []uint{2} {
		status, _, _ := call("suppliers", "archive", uid, scope, "", map[string]any{"id": supplier.ID, "preview": true})
		if status != 403 {
			t.Fatal("member accepted", status)
		}
	}
	for _, wrong := range []string{"", "cores:procurement:update"} {
		status, _, _ := call("suppliers", "archive", 1, wrong, "", map[string]any{"id": supplier.ID, "preview": true})
		if status != 403 {
			t.Fatal("wrong delegation", status)
		}
	}
	status, _, _ := call("suppliers", "archive", 1, scope, "", map[string]any{"id": supplier.ID, "preview": true, "table": "users"})
	if status != 400 {
		t.Fatal("unknown input accepted", status)
	}
	// No preview writes, including provisional receipts.
	p := review("suppliers", "archive", supplier.ID)
	for _, table := range []string{"audit_log", "proc_activities", "proc_idempotency_records"} {
		var n int64
		db.Table(table).Count(&n)
		if n != 0 {
			t.Fatal("preview mutated", table, n)
		}
	}
	// A related offer update invalidates an otherwise current supplier preview.
	must(db, "UPDATE proc_offers SET price_cents=price_cents+1 WHERE id=?", offer.ID)
	status, out, raw := call("suppliers", "archive", 1, scope, "supplier-stale-context", final(supplier.ID, p))
	if status != 200 || out["operation_status"] != "needs_input" || !strings.Contains(raw, "expected_context") {
		t.Fatal("stale dependency accepted", status, raw)
	}
	// Open orders block both supplier and product archival.
	must(db, "INSERT INTO proc_purchase_orders(number,supplier_id,status) VALUES('OPEN',?,'draft')", supplier.ID)
	must(db, "INSERT INTO proc_purchase_order_lines(purchase_order_id,product_id,description,quantity) SELECT id,?,'Fixture',1 FROM proc_purchase_orders WHERE number='OPEN'", product.ID)
	for ns, id := range map[string]uint{"suppliers": supplier.ID, "products": product.ID} {
		p := review(ns, "archive", id)
		if p["ready_to_execute"] != false || !strings.Contains(fmt.Sprint(p["required_fields"]), "active_open_orders") {
			t.Fatal("open order accepted", p)
		}
	}
	must(db, "UPDATE proc_purchase_orders SET status='received' WHERE number='OPEN'")
	// Audit failure rolls back identity, activity and receipt, permitting same-key retry.
	p = review("suppliers", "archive", supplier.ID)
	must(db, "ALTER TABLE audit_log RENAME TO missing_audit_log")
	status, _, _ = call("suppliers", "archive", 1, scope, "supplier-atomic-retry", final(supplier.ID, p))
	if status != 500 {
		t.Fatal("audit failure accepted", status)
	}
	var active bool
	if err := db.Raw("SELECT active FROM proc_suppliers WHERE id=?", supplier.ID).Row().Scan(&active); err != nil || !active {
		t.Fatal("audit failure changed row", err)
	}
	var n int64
	db.Table("proc_idempotency_records").Count(&n)
	if n != 0 {
		t.Fatal("failed receipt persisted", n)
	}
	must(db, "ALTER TABLE missing_audit_log RENAME TO audit_log")
	archived := execute("suppliers", "archive", "supplier-atomic-retry", supplier.ID, p)
	status, _, raw = call("suppliers", "archive", 1, scope, "supplier-atomic-retry", final(supplier.ID, p))
	if status != 200 || raw != archived {
		t.Fatal("receipt replay differs", status, raw)
	}
	must(db, "UPDATE users SET is_admin=false WHERE userid=1")
	status, _, _ = call("suppliers", "archive", 1, scope, "supplier-atomic-retry", final(supplier.ID, p))
	if status != 403 {
		t.Fatal("revoked administrator replay accepted", status)
	}
	must(db, "UPDATE users SET is_admin=true WHERE userid=1")
	for _, q := range []string{"DELETE FROM proc_suppliers WHERE id=1", "UPDATE proc_suppliers SET name='changed' WHERE id=1", "UPDATE proc_suppliers SET active=true,name='changed' WHERE id=1", "UPDATE proc_offers SET id=id+100 WHERE id=1"} {
		if db.Exec(q).Error == nil {
			t.Fatal("all-writer guard accepted", q)
		}
	}
	// Offer can archive under an inactive parent; restore cannot.
	execute("offers", "archive", "offer-archive-test", offer.ID, review("offers", "archive", offer.ID))
	blocked := review("offers", "restore", offer.ID)
	if blocked["ready_to_execute"] != false || !strings.Contains(fmt.Sprint(blocked["required_fields"]), "active_supplier") {
		t.Fatal("inactive offer parent accepted", blocked)
	}
	execute("suppliers", "restore", "supplier-restore-test", supplier.ID, review("suppliers", "restore", supplier.ID))
	execute("offers", "restore", "offer-restore-test", offer.ID, review("offers", "restore", offer.ID))
	execute("products", "archive", "product-archive-test", product.ID, review("products", "archive", product.ID))
	execute("products", "restore", "product-restore-test", product.ID, review("products", "restore", product.ID))
	var retained models.Supplier
	db.First(&retained, supplier.ID)
	if !retained.Active || retained.Notes != supplier.Notes || retained.PaymentTerms != supplier.PaymentTerms || retained.ID != supplier.ID {
		t.Fatal("supplier fields lost", retained)
	}
	var retainedOffer models.Offer
	db.First(&retainedOffer, offer.ID)
	if !retainedOffer.Active || retainedOffer.PriceCents != offer.PriceCents+1 || retainedOffer.PackSize != 3 || retainedOffer.SupplierSKU != offer.SupplierSKU {
		t.Fatal("offer fields lost", retainedOffer)
	}
	for _, table := range []string{"audit_log", "proc_activities", "proc_idempotency_records"} {
		db.Table(table).Count(&n)
		if n != 6 {
			t.Fatal("atomic action count", table, n)
		}
	}
	// Categories preserve complete parameter definitions and reject active dependants.
	categoryPreview := review("categories", "archive", category.ID)
	if categoryPreview["ready_to_execute"] != false || !strings.Contains(fmt.Sprint(categoryPreview["required_fields"]), "active_products") {
		t.Fatal("active category products accepted", categoryPreview)
	}
	if db.Exec("UPDATE proc_categories SET active=false WHERE id=?", category.ID).Error == nil {
		t.Fatal("native active product category archive accepted")
	}
	execute("products", "archive", "product-category-archive", product.ID, review("products", "archive", product.ID))
	categoryPreview = review("categories", "archive", category.ID)
	categoryReceipt := execute("categories", "archive", "category-archive-test", category.ID, categoryPreview)
	categoryStatus, _, categoryReplay := call("categories", "archive", 1, scope, "category-archive-test", final(category.ID, categoryPreview))
	if categoryStatus != 200 || categoryReceipt != categoryReplay {
		t.Fatal("category durable replay", categoryStatus, categoryReplay)
	}
	blockedProduct := review("products", "restore", product.ID)
	if blockedProduct["ready_to_execute"] != false || !strings.Contains(fmt.Sprint(blockedProduct["required_fields"]), "active_category") {
		t.Fatal("archived category restored product", blockedProduct)
	}
	for _, q := range []string{"DELETE FROM proc_categories WHERE id=1", "UPDATE proc_categories SET description='changed' WHERE id=1", "UPDATE proc_products SET active=true WHERE id=1"} {
		if db.Exec(q).Error == nil {
			t.Fatal("category all-writer guard", q)
		}
	}
	var keptCategory models.Category
	db.First(&keptCategory, category.ID)
	var beforeParameters, afterParameters any
	if err := json.Unmarshal(category.ParameterSchema, &beforeParameters); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(keptCategory.ParameterSchema, &afterParameters); err != nil {
		t.Fatal(err)
	}
	if keptCategory.Active || keptCategory.Description != category.Description || !reflect.DeepEqual(beforeParameters, afterParameters) {
		t.Fatal("category original fields lost", keptCategory)
	}
	execute("categories", "restore", "category-restore-test", category.ID, review("categories", "restore", category.ID))
	execute("products", "restore", "product-category-restore", product.ID, review("products", "restore", product.ID))
	// Omitted Active in a normal category edit must preserve active state.
	rawUpdate, _ := json.Marshal(map[string]any{"name": category.Name, "description": "Updated active category", "parameterSchema": beforeParameters})
	signedUpdate, _ := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{"uid": 1, "exp": time.Now().Add(time.Minute).Unix()}).SignedString(commonjwt.JWTSecret())
	nativeUpdate := httptest.NewRequest(http.MethodPut, fmt.Sprintf("/categories/%d", category.ID), bytes.NewReader(rawUpdate))
	nativeUpdate.AddCookie(&http.Cookie{Name: "cores_token", Value: signedUpdate})
	nativeResponse := httptest.NewRecorder()
	handler.ServeHTTP(nativeResponse, nativeUpdate)
	if nativeResponse.Code != 200 {
		t.Fatal("native category update", nativeResponse.Code, nativeResponse.Body.String())
	}
	db.First(&keptCategory, category.ID)
	if !keptCategory.Active {
		t.Fatal("omitted active archived category")
	}
	oversizedStatus, _, _ := call("categories", "archive", 1, scope, "", map[string]any{"id": category.ID, "preview": true, "confirmation_text": strings.Repeat("x", 9000)})
	if oversizedStatus != 400 {
		t.Fatal("oversized lifecycle object accepted", oversizedStatus)
	}
	// Native DELETE cannot silently remove history or report success.
	for ns, id := range map[string]uint{"suppliers": supplier.ID, "products": product.ID, "offers": offer.ID, "categories": category.ID} {
		signed, _ := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{"uid": 1, "exp": time.Now().Add(time.Minute).Unix()}).SignedString(commonjwt.JWTSecret())
		r := httptest.NewRequest(http.MethodDelete, fmt.Sprintf("/%s/%d", ns, id), nil)
		r.AddCookie(&http.Cookie{Name: "cores_token", Value: signed})
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != 409 {
			t.Fatal("native deletion", ns, w.Code, w.Body.String())
		}
	}
	// Two independently confirmed requests for one version commit once.
	latest := review("suppliers", "archive", supplier.ID)
	outcomes := make(chan string, 2)
	for _, key := range []string{"supplier-concurrent-first", "supplier-concurrent-second"} {
		go func(key string) {
			status, out, raw := call("suppliers", "archive", 1, scope, key, final(supplier.ID, latest))
			if status != 200 {
				outcomes <- raw
				return
			}
			outcomes <- fmt.Sprint(out["operation_status"])
		}(key)
	}
	counts := map[string]int{}
	for i := 0; i < 2; i++ {
		counts[<-outcomes]++
	}
	if counts["archived"] != 1 || counts["needs_input"] != 1 {
		t.Fatal("same-version concurrent mutations", counts)
	}
	execute("suppliers", "restore", "supplier-concurrent-restore", supplier.ID, review("suppliers", "restore", supplier.ID))
	// Older category receipts omit the newly added active field. Replay must
	// retain their response and the pre-migration request hash, not invent false.
	legacyPayload := map[string]any{"name": "Legacy category receipt", "description": "Original result", "parameterSchema": []any{}}
	legacyBody, _ := json.Marshal(legacyPayload)
	legacyCreate := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/categories", bytes.NewReader(legacyBody))
		req.AddCookie(&http.Cookie{Name: "cores_token", Value: signedUpdate})
		req.Header.Set("X-Cores-Origin", "MCP/AI")
		req.Header.Set("Idempotency-Key", "category-legacy-receipt-test")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		return w
	}
	createdLegacy := legacyCreate()
	if createdLegacy.Code != 201 {
		t.Fatal("legacy receipt fixture", createdLegacy.Code, createdLegacy.Body.String())
	}
	must(db, "UPDATE proc_idempotency_records SET response=response-'active' WHERE operation='category_create'")
	var originalLegacy json.RawMessage
	if err := db.Raw("SELECT response FROM proc_idempotency_records WHERE operation='category_create'").Row().Scan(&originalLegacy); err != nil {
		t.Fatal(err)
	}
	replayedLegacy := legacyCreate()
	if replayedLegacy.Code != 201 {
		t.Fatal("pre-migration request hash changed", replayedLegacy.Code, replayedLegacy.Body.String())
	}
	var originalObject, replayedObject any
	if err := json.Unmarshal(originalLegacy, &originalObject); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(replayedLegacy.Body.Bytes(), &replayedObject); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(originalObject, replayedObject) {
		t.Fatal("legacy category receipt altered", string(originalLegacy), replayedLegacy.Body.String())
	}
	// Receipts survive an owning service/database reconnect.
	restarted, err := database.Open(u.String())
	if err != nil {
		t.Fatal(err)
	}
	restartedSQL, _ := restarted.DB()
	defer restartedSQL.Close()
	handler = auth.Middleware(commonjwt.DatabaseUserLookup(restartedSQL), (&Handler{db: restarted}).Routes())
	status, _, raw = call("suppliers", "archive", 1, scope, "supplier-atomic-retry", final(supplier.ID, p))
	if status != 200 || raw != archived {
		t.Fatal("restart durable replay", status, raw)
	}
}
