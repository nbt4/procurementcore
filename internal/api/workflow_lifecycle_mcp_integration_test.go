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
	"sync"
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

func TestProcurementWorkflowOwnerRetainedLifecycleAndRevision(t *testing.T) {
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
	fixtureSQL, _ := fixture.DB()
	defer fixtureSQL.Close()
	const schema = "procurement_workflow_lifecycle_test"
	must := func(db *gorm.DB, q string, args ...any) {
		t.Helper()
		if err := db.Exec(q, args...).Error; err != nil {
			t.Fatal(err)
		}
	}
	var db *gorm.DB
	reject := func(q string, args ...any) {
		t.Helper()
		if err := db.Exec(q, args...).Error; err == nil {
			t.Fatal("unsafe native change accepted", q)
		}
	}
	must(fixture, "DROP SCHEMA IF EXISTS "+schema+" CASCADE;CREATE SCHEMA "+schema)
	defer fixture.Exec("DROP SCHEMA IF EXISTS " + schema + " CASCADE")
	params := u.Query()
	params.Set("search_path", schema)
	u.RawQuery = params.Encode()
	db, err = database.Open(u.String())
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, _ := db.DB()
	defer sqlDB.Close()
	must(db, `CREATE TABLE users(userid BIGINT PRIMARY KEY,username TEXT,is_active BOOLEAN,is_admin BOOLEAN);INSERT INTO users VALUES(1,'admin',true,true),(2,'requester',true,false),(3,'stranger',true,false);
 CREATE TABLE audit_log(id BIGSERIAL PRIMARY KEY,user_id BIGINT,action TEXT,entity_type TEXT,entity_id TEXT,old_values JSONB,new_values JSONB,ip_address TEXT,user_agent TEXT);
 CREATE TABLE warehouse_tasks(task_id BIGINT PRIMARY KEY,task_type TEXT,status TEXT,is_archived BOOLEAN,updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP);`)
	supplier := models.Supplier{Name: "Workflow supplier", Code: "WORKFLOW", Active: true}
	product := models.Product{SKU: "WORKFLOW", Name: "Workflow product", Unit: "m", Active: true, Parameters: json.RawMessage(`{}`), Attributes: json.RawMessage(`{}`)}
	if err := db.Create(&supplier).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&product).Error; err != nil {
		t.Fatal(err)
	}
	requisition := models.Requisition{Number: "BAN-WORKFLOW", Title: "Retained demand", RequesterID: 2, RequesterName: "requester", Status: "draft", Justification: "private original justification", EstimatedTotalCents: 400, Lines: []models.RequisitionLine{{ProductID: &product.ID, PreferredSupplierID: &supplier.ID, Description: "Demand line", Quantity: 4, Unit: "m", EstimatedPriceCents: 100}}}
	if err := db.Create(&requisition).Error; err != nil {
		t.Fatal(err)
	}
	order := models.PurchaseOrder{Number: "PO-WORKFLOW", SupplierID: supplier.ID, RequisitionID: &requisition.ID, Status: "draft", Currency: "EUR", TotalCents: 400, Notes: "private original order notes", Lines: []models.PurchaseOrderLine{{ProductID: &product.ID, Description: "Order line", Quantity: 4, Unit: "m", UnitPriceCents: 100}}}
	if err := db.Create(&order).Error; err != nil {
		t.Fatal(err)
	}
	t.Setenv("CORES_JWT_SECRET", "workflow-owner-integration-secret-32-bytes")
	handler := auth.Middleware(commonjwt.DatabaseUserLookup(sqlDB), (&Handler{db: db}).Routes())
	call := func(uid uint, scope, key, method, path string, payload any) (int, map[string]any, string) {
		t.Helper()
		raw, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		signed, err := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{"uid": uid, "username": "fixture", "is_admin": true, "mcp_scope": scope, "exp": time.Now().Add(time.Minute).Unix()}).SignedString(commonjwt.JWTSecret())
		if err != nil {
			t.Fatal(err)
		}
		r := httptest.NewRequest(method, path, bytes.NewReader(raw))
		r.AddCookie(&http.Cookie{Name: "cores_token", Value: signed})
		r.Header.Set("X-Cores-Origin", "MCP/AI")
		r.Header.Set("Idempotency-Key", key)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		out := map[string]any{}
		_ = json.Unmarshal(w.Body.Bytes(), &out)
		return w.Code, out, w.Body.String()
	}
	lifecycle := func(uid uint, ns, op, key string, in map[string]any) (int, map[string]any, string) {
		return call(uid, "cores:procurement:archive", key, "POST", "/mcp/workflows/"+ns+"/"+op, in)
	}
	review := func(uid uint, ns, op string, id uint) map[string]any {
		t.Helper()
		status, out, raw := lifecycle(uid, ns, op, "", map[string]any{"id": id, "preview": true})
		if status != 200 {
			t.Fatal("preview", status, raw)
		}
		return out
	}
	final := func(id uint, p map[string]any) map[string]any {
		return map[string]any{"id": id, "expected_updated_at": p["expected_updated_at"], "expected_context": p["expected_context"], "confirmation_text": p["required_confirmation_text"], "confirm_change": true}
	}
	count := func() string {
		t.Helper()
		var result string
		if err := db.Raw(`SELECT jsonb_build_array((SELECT count(*) FROM audit_log),(SELECT count(*) FROM proc_activities),(SELECT count(*) FROM proc_idempotency_records))::text`).Row().Scan(&result); err != nil {
			t.Fatal(err)
		}
		return result
	}
	compare := func(before, after map[string]any) {
		t.Helper()
		for _, field := range []string{"updatedAt", "archivedAt", "isArchived"} {
			delete(before, field)
			delete(after, field)
		}
		if !reflect.DeepEqual(before, after) {
			t.Fatal("lifecycle changed business fields", before, after)
		}
	}
	p := review(2, "requisitions", "archive", requisition.ID)
	if p["ready_to_execute"] != false {
		t.Fatal("open order not blocked", p)
	}
	reject("UPDATE proc_requisitions SET is_archived=true WHERE id=?", requisition.ID)
	for _, test := range []struct {
		uid    uint
		entity string
		id     uint
	}{{2, "orders", order.ID}, {3, "requisitions", requisition.ID}} {
		status, _, _ := lifecycle(test.uid, test.entity, "archive", "", map[string]any{"id": test.id, "preview": true})
		if status != 403 {
			t.Fatal("wrong owner accepted", test, status)
		}
	}
	for _, scope := range []string{"cores:procurement:update", "cores:procurement:approve", "cores:write", ""} {
		status, _, _ := call(1, scope, "", "POST", "/mcp/workflows/orders/archive", map[string]any{"id": order.ID, "preview": true})
		if status != 403 {
			t.Fatal("wrong archive delegation", scope, status)
		}
	}
	before := count()
	p = review(1, "orders", "archive", order.ID)
	if p["ready_to_execute"] != true || count() != before {
		t.Fatal("preview changed state", p)
	}
	// Every native line change advances the parent's exact version.
	must(db, "UPDATE proc_purchase_order_lines SET description='Changed order line' WHERE purchase_order_id=?", order.ID)
	status, out, raw := lifecycle(1, "orders", "archive", "workflow-stale-line", final(order.ID, p))
	if status != 200 || out["operation_status"] != "needs_input" || count() != before {
		t.Fatal("native child stale version not detected", status, raw)
	}
	p = review(1, "orders", "archive", order.ID)
	input := final(order.ID, p)
	must(db, `CREATE FUNCTION fail_workflow_audit() RETURNS TRIGGER AS $$ BEGIN IF NEW.action='order.archive' THEN RAISE EXCEPTION 'forced final workflow audit rollback';END IF;RETURN NEW;END $$ LANGUAGE plpgsql;CREATE TRIGGER fail_workflow_audit BEFORE INSERT ON audit_log FOR EACH ROW EXECUTE FUNCTION fail_workflow_audit()`)
	status, _, raw = lifecycle(1, "orders", "archive", "workflow-order-atomic-retry", input)
	if status != 500 || count() != before {
		t.Fatal("partial lifecycle commit", status, raw)
	}
	must(db, "DROP TRIGGER fail_workflow_audit ON audit_log")
	status, out, raw = lifecycle(1, "orders", "archive", "workflow-order-atomic-retry", input)
	if status != 200 || out["operation_status"] != "archived" {
		t.Fatal(status, raw)
	}
	archived := out["record"].(map[string]any)
	compare(p["current"].(map[string]any), archived)
	saved := count()
	status, out, raw = lifecycle(1, "orders", "archive", "workflow-order-atomic-retry", input)
	if status != 200 || out["operation_status"] != "archived" || count() != saved {
		t.Fatal("duplicate archive", status, raw)
	}
	must(db, "UPDATE users SET is_admin=false WHERE userid=1")
	status, _, _ = lifecycle(1, "orders", "archive", "workflow-order-atomic-retry", input)
	if status != 403 || count() != saved {
		t.Fatal("revoked administrator replay", status)
	}
	must(db, "UPDATE users SET is_admin=true WHERE userid=1")
	for _, q := range []string{"DELETE FROM proc_purchase_orders WHERE id=?", "UPDATE proc_purchase_orders SET notes='forbidden' WHERE id=?", "UPDATE proc_purchase_orders SET is_archived=false,notes='forbidden' WHERE id=?"} {
		reject(q, order.ID)
	}
	reject("UPDATE proc_purchase_order_lines SET quantity=5 WHERE purchase_order_id=?", order.ID)
	reject("DELETE FROM proc_purchase_order_lines WHERE purchase_order_id=?", order.ID)
	p = review(2, "requisitions", "archive", requisition.ID)
	if p["ready_to_execute"] != true {
		t.Fatal("archived draft order still blocked demand", p)
	}
	status, out, raw = lifecycle(2, "requisitions", "archive", "workflow-requester-archive", final(requisition.ID, p))
	if status != 200 || out["operation_status"] != "archived" {
		t.Fatal("own requester archive", status, raw)
	}
	reject("UPDATE proc_requisition_lines SET quantity=5 WHERE requisition_id=?", requisition.ID)
	reject("DELETE FROM proc_requisitions WHERE id=?", requisition.ID)
	// Archived draft demand/order no longer keep a catalog product active.
	must(db, "UPDATE proc_products SET active=false WHERE id=?", product.ID)
	p = review(2, "requisitions", "restore", requisition.ID)
	if p["ready_to_execute"] != false {
		t.Fatal("inactive product allowed restore", p)
	}
	reject("UPDATE proc_requisitions SET is_archived=false WHERE id=?", requisition.ID)
	p = review(1, "orders", "restore", order.ID)
	if p["ready_to_execute"] != false {
		t.Fatal("inactive original requisition allowed restore", p)
	}
	must(db, "UPDATE proc_products SET active=true WHERE id=?", product.ID)
	p = review(2, "requisitions", "restore", requisition.ID)
	status, out, raw = lifecycle(2, "requisitions", "restore", "workflow-requester-restore", final(requisition.ID, p))
	if status != 200 || out["operation_status"] != "restored" {
		t.Fatal(status, raw)
	}
	p = review(1, "orders", "restore", order.ID)
	if p["ready_to_execute"] != true {
		t.Fatal(p)
	}
	status, out, raw = lifecycle(1, "orders", "restore", "workflow-order-restore", final(order.ID, p))
	if status != 200 || out["operation_status"] != "restored" {
		t.Fatal(status, raw)
	}
	// Completion of a receipt putaway task is independently bound to the archive.
	completed := models.PurchaseOrder{Number: "PO-PUTAWAY", SupplierID: supplier.ID, Status: "received", Currency: "EUR", TotalCents: 100, Lines: []models.PurchaseOrderLine{{ProductID: &product.ID, Description: "Received", Quantity: 1, ReceivedQuantity: 1, Unit: "m", UnitPriceCents: 100}}}
	if err := db.Create(&completed).Error; err != nil {
		t.Fatal(err)
	}
	taskID := int64(900)
	must(db, "INSERT INTO warehouse_tasks(task_id,task_type,status,is_archived) VALUES(?,'putaway','open',false)", taskID)
	receipt := models.Receipt{PurchaseOrderID: completed.ID, PurchaseOrderLineID: completed.Lines[0].ID, Quantity: 1, ReceivedAt: time.Now(), PutawayTaskID: &taskID}
	if err := db.Create(&receipt).Error; err != nil {
		t.Fatal(err)
	}
	p = review(1, "orders", "archive", completed.ID)
	if p["ready_to_execute"] != false {
		t.Fatal("open putaway not blocked", p)
	}
	reject("UPDATE proc_purchase_orders SET is_archived=true WHERE id=?", completed.ID)
	must(db, "UPDATE warehouse_tasks SET status='done',updated_at=clock_timestamp() WHERE task_id=?", taskID)
	status, out, raw = lifecycle(1, "orders", "archive", "workflow-putaway-stale", final(completed.ID, p))
	if status != 200 || out["operation_status"] != "needs_input" {
		t.Fatal("putaway context not bound", status, raw)
	}
	p = review(1, "orders", "archive", completed.ID)
	if p["ready_to_execute"] != true {
		t.Fatal(p)
	}
	status, out, raw = lifecycle(1, "orders", "archive", "workflow-putaway-resolved", final(completed.ID, p))
	if status != 200 || out["operation_status"] != "archived" {
		t.Fatal(status, raw)
	}
	// Native return -> explicit revision -> draft -> resubmission clears old decision.
	must(db, "UPDATE proc_requisitions SET status='submitted' WHERE id=?", requisition.ID)
	version := review(2, "requisitions", "archive", requisition.ID)["expected_updated_at"]
	status, out, raw = call(1, "cores:procurement:approve", "workflow-return-revision", "POST", fmt.Sprintf("/requisitions/%d/decision", requisition.ID), map[string]any{"decision": "returned", "note": "Revise the requested amount", "expectedUpdatedAt": version})
	if status != 200 || out["status"] != "returned" {
		t.Fatal("return failed", status, raw)
	}
	update := map[string]any{"title": "Revised retained demand", "costCenter": "NEW", "justification": "Revision reviewed", "lines": []any{map[string]any{"productId": product.ID, "description": "Revised line", "quantity": 2, "unit": "m", "estimatedPriceCents": 100}}, "expectedUpdatedAt": out["updatedAt"]}
	status, out, raw = call(2, "cores:procurement:update", "workflow-revision-update", "PUT", fmt.Sprintf("/requisitions/%d", requisition.ID), update)
	if status != 200 || out["status"] != "draft" {
		t.Fatal("returned demand not editable", status, raw)
	}
	status, out, raw = call(2, "cores:procurement:submit", "workflow-revision-submit", "POST", fmt.Sprintf("/requisitions/%d/submit", requisition.ID), map[string]any{"expectedUpdatedAt": out["updatedAt"]})
	if status != 200 || out["status"] != "submitted" || out["approvedBy"] != nil || out["decidedAt"] != nil || out["decisionNote"] != "" {
		t.Fatal("resubmission kept stale approval", status, raw)
	}
	// Exact version permits a single concurrent archive only.
	other := models.Requisition{Number: "BAN-CONCURRENT", Title: "Concurrent", RequesterID: 2, Status: "draft", EstimatedTotalCents: 100, Lines: []models.RequisitionLine{{Description: "Line", Quantity: 1, Unit: "m", EstimatedPriceCents: 100}}}
	if err := db.Create(&other).Error; err != nil {
		t.Fatal(err)
	}
	p = review(2, "requisitions", "archive", other.ID)
	payload := final(other.ID, p)
	var wg sync.WaitGroup
	results := make(chan string, 2)
	for _, key := range []string{"workflow-concurrent-first", "workflow-concurrent-second"} {
		wg.Add(1)
		go func(k string) {
			defer wg.Done()
			status, out, raw := lifecycle(2, "requisitions", "archive", k, payload)
			if status != 200 {
				results <- raw
				return
			}
			results <- fmt.Sprint(out["operation_status"])
		}(key)
	}
	wg.Wait()
	close(results)
	seen := map[string]int{}
	for result := range results {
		seen[result]++
	}
	if seen["archived"] != 1 || seen["needs_input"] != 1 {
		t.Fatal(seen)
	}
}
