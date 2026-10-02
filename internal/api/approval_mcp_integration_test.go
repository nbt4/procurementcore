package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"procurementcore/internal/auth"
	"procurementcore/internal/database"
	"procurementcore/internal/models"

	"github.com/go-chi/chi/v5"
	"github.com/golang-jwt/jwt/v5"
	commonjwt "github.com/nbt4/cores-common/pkg/jwt"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func TestProcurementApprovalOwnerExactContextRightsAtomicReplay(t *testing.T) {
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
	const schema = "procurement_approval_owner_test"
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
	sqlDB, _ := db.DB()
	defer sqlDB.Close()
	must(db, `CREATE TABLE users(userid BIGINT PRIMARY KEY,username TEXT,is_active BOOLEAN,is_admin BOOLEAN);INSERT INTO users VALUES(1,'admin',true,true),(2,'requester',true,false),(3,'stranger',true,false);CREATE TABLE audit_log(id BIGSERIAL PRIMARY KEY,user_id BIGINT,action TEXT,entity_type TEXT,entity_id TEXT,old_values JSONB,new_values JSONB,ip_address TEXT,user_agent TEXT);`)
	supplier := models.Supplier{Name: "Approval supplier", Code: "APPROVAL", Active: true}
	product := models.Product{SKU: "APPROVAL", Name: "Approval product", Unit: "m", Active: true, Parameters: json.RawMessage(`{}`), Attributes: json.RawMessage(`{}`)}
	if err := db.Create(&supplier).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&product).Error; err != nil {
		t.Fatal(err)
	}
	counter := 0
	newReq := func(requester uint) models.Requisition {
		t.Helper()
		counter++
		r := models.Requisition{Number: fmt.Sprintf("BAN-APP-%d", counter), Title: "Approval demand", RequesterID: requester, Status: "submitted", Justification: "private original demand", EstimatedTotalCents: 100, Lines: []models.RequisitionLine{{ProductID: &product.ID, PreferredSupplierID: &supplier.ID, Description: "Demand line", Quantity: 1, Unit: "m", EstimatedPriceCents: 100}}}
		if err := db.Create(&r).Error; err != nil {
			t.Fatal(err)
		}
		return r
	}
	newOrder := func(status string) models.PurchaseOrder {
		t.Helper()
		counter++
		o := models.PurchaseOrder{Number: fmt.Sprintf("PO-APP-%d", counter), SupplierID: supplier.ID, Status: status, Currency: "EUR", TotalCents: 100, Notes: "private original order", Lines: []models.PurchaseOrderLine{{ProductID: &product.ID, Description: "Order line", Quantity: 1, Unit: "m", UnitPriceCents: 100}}}
		if err := db.Create(&o).Error; err != nil {
			t.Fatal(err)
		}
		return o
	}
	t.Setenv("CORES_JWT_SECRET", "approval-owner-secret-at-least-32-bytes")
	h := &Handler{db: db}
	handler := auth.Middleware(commonjwt.DatabaseUserLookup(sqlDB), h.Routes())
	// This isolated harness deliberately reproduces the previous published native
	// handlers to create genuine legacy hashes/responses before the public gate.
	legacyRoutes := chi.NewRouter()
	legacyRoutes.Post("/requisitions/{id}/decision", h.decideRequisition)
	legacyRoutes.Put("/orders/{id}", h.updateOrder)
	legacy := auth.Middleware(commonjwt.DatabaseUserLookup(sqlDB), legacyRoutes)
	callHandler := func(target http.Handler, uid uint, scope, key, method, path string, payload any) (int, map[string]any, string) {
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
		target.ServeHTTP(w, r)
		out := map[string]any{}
		_ = json.Unmarshal(w.Body.Bytes(), &out)
		return w.Code, out, w.Body.String()
	}
	call := func(uid uint, scope, key, method, path string, payload any) (int, map[string]any, string) {
		return callHandler(handler, uid, scope, key, method, path, payload)
	}
	approve := func(uid uint, ns, key string, in map[string]any) (int, map[string]any, string) {
		return call(uid, "cores:procurement:approve", key, "POST", "/mcp/approvals/"+ns, in)
	}
	review := func(ns string, id uint, action, note string) map[string]any {
		t.Helper()
		in := map[string]any{"id": id, "preview": true}
		if ns == "orders" {
			in["status"] = action
			in["reason"] = note
		} else {
			in["decision"] = action
			in["note"] = note
		}
		status, out, raw := approve(1, ns, "", in)
		if status != 200 {
			t.Fatal("preview", status, raw)
		}
		return out
	}
	final := func(ns string, id uint, action, note string, p map[string]any) map[string]any {
		in := map[string]any{"id": id, "expected_updated_at": p["expected_updated_at"], "expected_context": p["expected_context"], "confirmation_text": p["required_confirmation_text"], "confirm_change": true}
		if ns == "orders" {
			in["status"] = action
			in["reason"] = note
		} else {
			in["decision"] = action
			in["note"] = note
		}
		return in
	}
	count := func() string {
		t.Helper()
		var out string
		if err := db.Raw(`SELECT jsonb_build_array((SELECT count(*) FROM audit_log),(SELECT count(*) FROM proc_activities),(SELECT count(*) FROM proc_idempotency_records))::text`).Row().Scan(&out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	req := newReq(2)
	self := newReq(1)
	for _, test := range []struct {
		uid   uint
		id    uint
		scope string
	}{{1, self.ID, "cores:procurement:approve"}, {2, req.ID, "cores:procurement:approve"}, {1, req.ID, "cores:write"}, {1, req.ID, "cores:procurement:update"}, {1, req.ID, "cores:procurement:receive"}} {
		status, _, raw := call(test.uid, test.scope, "", "POST", "/mcp/approvals/requisitions", map[string]any{"id": test.id, "decision": "approved", "preview": true})
		if status != 403 {
			t.Fatal("unauthorized approval", test, status, raw)
		}
	}
	before := count()
	p := review("requisitions", req.ID, "approved", "")
	if p["ready_to_execute"] != true || count() != before {
		t.Fatal("impure/incomplete preview", p)
	}
	in := final("requisitions", req.ID, "approved", "", p)
	// Native line edits and dependency edits both invalidate the exact final context.
	must(db, "UPDATE proc_requisition_lines SET description='Changed native demand' WHERE requisition_id=?", req.ID)
	status, out, raw := approve(1, "requisitions", "approval-stale-line", in)
	if status != 200 || out["operation_status"] != "needs_input" || count() != before {
		t.Fatal("stale native line", status, raw)
	}
	p = review("requisitions", req.ID, "approved", "")
	in = final("requisitions", req.ID, "approved", "", p)
	must(db, "UPDATE proc_suppliers SET name='Changed supplier context' WHERE id=?", supplier.ID)
	status, out, raw = approve(1, "requisitions", "approval-stale-parent", in)
	if status != 200 || out["operation_status"] != "needs_input" || count() != before {
		t.Fatal("stale supplier", status, raw)
	}
	p = review("requisitions", req.ID, "approved", "")
	in = final("requisitions", req.ID, "approved", "", p)
	wrong := final("requisitions", req.ID, "approved", "", p)
	wrong["confirmation_text"] = "APPROVE REQUISITION OTHER"
	status, _, raw = approve(1, "requisitions", "approval-wrong-phrase", wrong)
	if status != 428 || count() != before {
		t.Fatal("unbound phrase", status, raw)
	}
	must(db, `CREATE FUNCTION fail_approval_audit() RETURNS TRIGGER AS $$ BEGIN IF NEW.action='requisition.approved' THEN RAISE EXCEPTION 'forced final approval audit';END IF;RETURN NEW;END $$ LANGUAGE plpgsql;CREATE TRIGGER fail_approval_audit BEFORE INSERT ON audit_log FOR EACH ROW EXECUTE FUNCTION fail_approval_audit()`)
	status, _, raw = approve(1, "requisitions", "approval-atomic-retry", in)
	if status != 500 || count() != before {
		t.Fatal("partial approval commit", status, raw)
	}
	must(db, "DROP TRIGGER fail_approval_audit ON audit_log")
	status, out, raw = approve(1, "requisitions", "approval-atomic-retry", in)
	if status != 200 || out["requisition"].(map[string]any)["status"] != "approved" {
		t.Fatal(status, raw)
	}
	after := count()
	status, again, raw := approve(1, "requisitions", "approval-atomic-retry", in)
	if status != 200 || !equalJSON(out, again) || count() != after {
		t.Fatal("approval replay", status, raw)
	}
	must(db, "UPDATE users SET is_admin=false WHERE userid=1")
	status, _, _ = approve(1, "requisitions", "approval-atomic-retry", in)
	if status != 403 || count() != after {
		t.Fatal("revoked approval replay", status)
	}
	must(db, "UPDATE users SET is_admin=true WHERE userid=1")
	for _, decision := range []string{"rejected", "returned"} {
		r := newReq(2)
		p := review("requisitions", r.ID, decision, "Reviewed decision")
		if p["ready_to_execute"] != true {
			t.Fatal(p)
		}
		status, out, raw := approve(1, "requisitions", "approval-"+decision, final("requisitions", r.ID, decision, "Reviewed decision", p))
		if status != 200 || out["requisition"].(map[string]any)["status"] != decision {
			t.Fatal(status, raw)
		}
	}
	r := newReq(2)
	if review("requisitions", r.ID, "returned", "")["ready_to_execute"] != false {
		t.Fatal("return without reason")
	}
	// Open-order transitions retain commercial fields, all lines and received history.
	for _, test := range []struct{ from, to string }{{"draft", "sent"}, {"sent", "confirmed"}, {"draft", "cancelled"}, {"partially_confirmed", "cancelled"}, {"partially_received", "cancelled"}} {
		o := newOrder(test.from)
		p := review("orders", o.ID, test.to, "Reviewed cancellation")
		if p["ready_to_execute"] != true {
			t.Fatal(test, p)
		}
		input := final("orders", o.ID, test.to, "Reviewed cancellation", p)
		before := count()
		status, out, raw := approve(1, "orders", "approval-order-"+test.from+"-"+test.to, input)
		if status != 200 {
			t.Fatal(test, status, raw)
		}
		record := out["purchase_order"].(map[string]any)
		if record["status"] != test.to || record["totalCents"] != float64(100) || record["lines"].([]any)[0].(map[string]any)["id"] != float64(o.Lines[0].ID) {
			t.Fatal("transition lost retained fields", out)
		}
		if test.to == "sent" && record["orderDate"] == nil {
			t.Fatal("missing order date")
		}
		if count() == before {
			t.Fatal("missing atomic audit/activity/receipt")
		}
		after := count()
		status, again, raw := approve(1, "orders", "approval-order-"+test.from+"-"+test.to, input)
		if status != 200 || !equalJSON(out, again) || count() != after {
			t.Fatal("transition replay", status, raw)
		}
	}
	o := newOrder("draft")
	if review("orders", o.ID, "confirmed", "")["ready_to_execute"] != false || review("orders", o.ID, "cancelled", "")["ready_to_execute"] != false {
		t.Fatal("invalid status/cancellation")
	}
	must(db, "UPDATE proc_purchase_orders SET amazon_punchout_session_id=999 WHERE id=?", o.ID)
	p = review("orders", o.ID, "sent", "")
	if p["ready_to_execute"] != false {
		t.Fatal("Amazon submission bypass", p)
	}
	// Closed public legacy endpoints reject unbooked MCP approvals.
	status, _, raw = call(1, "cores:procurement:approve", "approval-legacy-unbound", "POST", fmt.Sprintf("/requisitions/%d/decision", r.ID), map[string]any{"decision": "approved", "expectedUpdatedAt": review("requisitions", r.ID, "approved", "")["expected_updated_at"]})
	if status != 428 {
		t.Fatal("unbound public legacy decision", status, raw)
	}
	// Original saved native responses and exact request hashes remain replayable.
	legacyReq := newReq(2)
	p = review("requisitions", legacyReq.ID, "approved", "")
	oldReq := map[string]any{"decision": "approved", "note": "Original legacy approval", "expectedUpdatedAt": p["expected_updated_at"]}
	status, savedReq, raw := callHandler(legacy, 1, "cores:procurement:approve", "approval-legacy-golden", "POST", fmt.Sprintf("/requisitions/%d/decision", legacyReq.ID), oldReq)
	if status != 200 {
		t.Fatal("old owner harness", status, raw)
	}
	legacyInput := map[string]any{"id": legacyReq.ID, "decision": "approved", "note": "Original legacy approval", "expected_updated_at": p["expected_updated_at"], "confirmation_text": fmt.Sprintf("APPROVE REQUISITION %d", legacyReq.ID), "confirm_change": true}
	before = count()
	status, out, raw = approve(1, "requisitions", "approval-legacy-golden", legacyInput)
	if status != 200 || !equalJSON(out["requisition"], savedReq) || count() != before {
		t.Fatal("old approval business response", status, raw)
	}
	legacyInput["note"] = "Changed original note"
	status, _, raw = approve(1, "requisitions", "approval-legacy-golden", legacyInput)
	if status != 409 || count() != before {
		t.Fatal("changed legacy payload", status, raw)
	}
	legacyOrder := newOrder("draft")
	p = review("orders", legacyOrder.ID, "cancelled", "Legacy cancellation")
	oldOrder := map[string]any{"status": "cancelled", "supplierOrderNumber": "", "expectedDelivery": nil, "notes": "private original order\nCancellation: Legacy cancellation", "expectedUpdatedAt": p["expected_updated_at"]}
	status, savedOrder, raw := callHandler(legacy, 1, "cores:procurement:approve", "transition-legacy-golden", "PUT", fmt.Sprintf("/orders/%d", legacyOrder.ID), oldOrder)
	if status != 200 {
		t.Fatal("old transition harness", status, raw)
	}
	legacyInput = map[string]any{"id": legacyOrder.ID, "status": "cancelled", "reason": "Legacy cancellation", "expected_updated_at": p["expected_updated_at"], "confirmation_text": fmt.Sprintf("CANCEL ORDER %d", legacyOrder.ID), "confirm_change": true}
	before = count()
	status, out, raw = approve(1, "orders", "transition-legacy-golden", legacyInput)
	if status != 200 || !equalJSON(out["purchase_order"], savedOrder) || count() != before {
		t.Fatal("old transition business response", status, raw)
	}
	legacyInput["reason"] = "Changed cancellation"
	status, _, raw = approve(1, "orders", "transition-legacy-golden", legacyInput)
	if status != 409 || count() != before {
		t.Fatal("changed legacy cancellation", status, raw)
	}
	status, out, raw = call(1, "cores:procurement:approve", "transition-legacy-golden", "PUT", fmt.Sprintf("/orders/%d", legacyOrder.ID), oldOrder)
	if status != 200 || !equalJSON(out, savedOrder) || count() != before {
		t.Fatal("public legacy replay", status, raw)
	}
	// Both received and cancelled orders restore their original status unchanged.
	for _, state := range []string{"received", "cancelled"} {
		retained := newOrder(state)
		p, err := prepareProcurementWorkflowLifecycle(db, "orders", "archive", masterLifecycleRequest{ID: int64(retained.ID)})
		if err != nil || p["ready_to_execute"] != true {
			t.Fatal(state, p, err)
		}
		must(db, "UPDATE proc_purchase_orders SET is_archived=true WHERE id=?", retained.ID)
		p, err = prepareProcurementWorkflowLifecycle(db, "orders", "restore", masterLifecycleRequest{ID: int64(retained.ID)})
		if err != nil || p["ready_to_execute"] != true || p["current"].(map[string]any)["status"] != state {
			t.Fatal("retained status restore", state, p, err)
		}
	}
	huge := newReq(2)
	must(db, "UPDATE proc_requisitions SET justification=? WHERE id=?", strings.Repeat("x", 513<<10), huge.ID)
	status, _, _ = approve(1, "requisitions", "", map[string]any{"id": huge.ID, "decision": "approved", "preview": true})
	if status != 413 {
		t.Fatal("unbounded full preview", status)
	}
	// A complete preview can be consumed by one concurrent decision only.
	concurrent := newReq(2)
	p = review("requisitions", concurrent.ID, "approved", "")
	input := final("requisitions", concurrent.ID, "approved", "", p)
	var wg sync.WaitGroup
	results := make(chan string, 2)
	for _, key := range []string{"approval-concurrent-first", "approval-concurrent-second"} {
		wg.Add(1)
		go func(k string) {
			defer wg.Done()
			status, out, raw := approve(1, "requisitions", k, input)
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
	if seen["decided"] != 1 || seen["needs_input"] != 1 {
		t.Fatal(seen)
	}
}

func equalJSON(a, b any) bool {
	one, _ := json.Marshal(a)
	two, _ := json.Marshal(b)
	return bytes.Equal(one, two)
}
