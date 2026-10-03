package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"procurementcore/internal/amazon"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
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

func TestAmazonSubmissionDurablePhasesNoDuplicateAndCurrentRights(t *testing.T) {
	dsn := os.Getenv("PROCUREMENT_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("disposable PostgreSQL required")
	}
	parsedURL, err := url.Parse(dsn)
	if err != nil || !strings.HasSuffix(parsedURL.Path, "_test") {
		t.Fatal("dedicated _test database required")
	}
	fixture, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	fixtureSQL, _ := fixture.DB()
	defer fixtureSQL.Close()
	const schema = "procurement_amazon_submission_owner_test"
	must := func(db *gorm.DB, q string, args ...any) {
		t.Helper()
		if err := db.Exec(q, args...).Error; err != nil {
			t.Fatal(err)
		}
	}
	must(fixture, "DROP SCHEMA IF EXISTS "+schema+" CASCADE;CREATE SCHEMA "+schema)
	defer fixture.Exec("DROP SCHEMA IF EXISTS " + schema + " CASCADE")
	params := parsedURL.Query()
	params.Set("search_path", schema)
	parsedURL.RawQuery = params.Encode()
	db, err := database.Open(parsedURL.String())
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, _ := db.DB()
	defer sqlDB.Close()
	must(db, `CREATE TABLE users(userid BIGINT PRIMARY KEY,username TEXT,is_active BOOLEAN,is_admin BOOLEAN);INSERT INTO users VALUES(1,'admin',true,true),(2,'requester',true,false),(3,'stranger',true,false);CREATE TABLE audit_log(id BIGSERIAL PRIMARY KEY,user_id BIGINT,action TEXT,entity_type TEXT,entity_id TEXT,old_values JSONB,new_values JSONB,ip_address TEXT,user_agent TEXT);`)

	supplier := models.Supplier{Name: "Exact supplier", Code: "AMAZON-BUSINESS", Active: true}
	product := models.Product{SKU: "DRAFT", Name: "Default line description", Active: true, Parameters: json.RawMessage(`{}`), Attributes: json.RawMessage(`{}`)}
	if err := db.Create(&supplier).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&product).Error; err != nil {
		t.Fatal(err)
	}
	t.Setenv("CORES_JWT_SECRET", "order-draft-owner-secret-at-least-32-bytes")
	h := &Handler{db: db}
	handler := auth.Middleware(commonjwt.DatabaseUserLookup(sqlDB), h.Routes())
	callHandler := func(target http.Handler, uid uint, scope, key, method, path string, payload any) (int, map[string]any) {
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
		if scope != "UI" {
			r.Header.Set("X-Cores-Origin", "MCP/AI")
		}
		r.Header.Set("Idempotency-Key", key)
		w := httptest.NewRecorder()
		target.ServeHTTP(w, r)
		out := map[string]any{}
		_ = json.Unmarshal(w.Body.Bytes(), &out)
		return w.Code, out
	}
	call := func(uid uint, op, key string, in map[string]any) (int, map[string]any) {
		return callHandler(handler, uid, "cores:procurement:send", key, "POST", "/mcp/orders/send-amazon", in)
	}
	clone := func(in map[string]any) map[string]any {
		out := map[string]any{}
		for k, v := range in {
			out[k] = v
		}
		return out
	}
	count := func() string {
		var out string
		if err := db.Raw("SELECT jsonb_build_array((SELECT count(*) FROM proc_purchase_orders),(SELECT count(*) FROM proc_purchase_order_lines),(SELECT count(*) FROM proc_idempotency_records),(SELECT count(*) FROM audit_log),(SELECT count(*) FROM proc_activities),(SELECT jsonb_agg(status ORDER BY id) FROM proc_purchase_orders),(SELECT jsonb_agg(status ORDER BY id) FROM proc_order_submissions))::text").Row().Scan(&out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	review := func(op string, in map[string]any) map[string]any {
		t.Helper()
		status, p := call(1, op, "", in)
		if status != 200 || p["ready_to_execute"] != true {
			t.Fatal("preview", status, p)
		}
		return p
	}
	final := func(in, p map[string]any) map[string]any {
		out := clone(in)
		out["preview"] = false
		out["confirm_change"] = true
		out["expected_context"] = p["expected_context"]
		out["expected_updated_at"] = p["expected_updated_at"]
		out["confirmation_text"] = p["required_confirmation_text"]
		return out
	}

	var requests atomic.Int64
	var fail atomic.Bool
	supplierServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error("supplier request unreadable")
		}
		if !strings.Contains(string(raw), "<OrderRequest>") || !strings.Contains(string(raw), "SupplierPartAuxiliaryID") || !strings.Contains(string(raw), "ship-street") {
			t.Error("incomplete supplier payload")
		}
		if fail.Load() {
			w.WriteHeader(503)
			return
		}
		w.Header().Set("Content-Type", "text/xml")
		_, _ = w.Write([]byte(`<cXML><Response><Status code="200" text="OK"/></Response></cXML>`))
	}))
	defer supplierServer.Close()
	configure := func(street string) {
		c, err := amazon.New(amazon.Config{FromIdentity: "fixture-buyer", SharedSecret: "never-exposed-secret", TestURL: supplierServer.URL + "/test", LiveURL: supplierServer.URL + "/live", ReturnURL: supplierServer.URL + "/return", OrderURL: supplierServer.URL + "/submit", Mode: "test", ShipTo: amazon.Address{Company: "Fixture company", Street: street, City: "Fixture city", PostalCode: "12345", Country: "DE"}, AllowHTTP: true})
		if err != nil {
			t.Fatal(err)
		}
		h.amazon = c
	}
	configure("ship-street")
	seq := 0
	newOrder := func() models.PurchaseOrder {
		seq++
		session := uint(99900 + seq)
		approver := uint(1)
		decided := time.Now()
		source := models.Requisition{Number: fmt.Sprintf("AMAZON-SOURCE-%d", seq), Title: "Approved source", Status: "ordered", RequesterID: 2, ApprovedBy: &approver, DecidedAt: &decided, AmazonPunchoutSessionID: &session, Lines: []models.RequisitionLine{{ProductID: &product.ID, Description: "Original approved item", Quantity: 3, Unit: "EA", EstimatedPriceCents: 1000, SupplierPartID: "B01", SupplierPartAuxiliaryID: "opaque-vendor-identity"}}}
		if err := db.Create(&source).Error; err != nil {
			t.Fatal(err)
		}
		row := models.PurchaseOrder{RequisitionID: &source.ID, Number: fmt.Sprintf("AMAZON-SEND-%d", seq), SupplierID: supplier.ID, Status: "draft", Currency: "EUR", AmazonPunchoutSessionID: &session, TotalCents: 3000, OrderedBy: 1, Lines: []models.PurchaseOrderLine{{ProductID: &product.ID, Description: "Original approved item", Quantity: 3, Unit: "EA", UnitPriceCents: 1000, SupplierPartID: "B01", SupplierPartAuxiliaryID: "opaque-vendor-identity"}}}
		if err := db.Create(&row).Error; err != nil {
			t.Fatal(err)
		}
		return row
	}
	order := newOrder()
	// Approval cannot be bypassed by a standalone/edited/self-approved cart.
	check := func() {
		t.Helper()
		if code, p := call(1, "send", "", map[string]any{"id": order.ID, "preview": true}); code != 200 || p["ready_to_execute"] != false || requests.Load() != 0 {
			t.Fatal("approved cart guard", code, p)
		}
	}
	must(db, "UPDATE proc_requisitions SET approved_by=requester_id WHERE id=?", *order.RequisitionID)
	check()
	must(db, "UPDATE proc_requisitions SET approved_by=1 WHERE id=?", *order.RequisitionID)
	must(db, "UPDATE proc_purchase_order_lines SET unit_price_cents=999 WHERE id=?", order.Lines[0].ID)
	check()
	must(db, "UPDATE proc_purchase_order_lines SET unit_price_cents=1000 WHERE id=?", order.Lines[0].ID)
	in := map[string]any{"id": order.ID, "preview": true}
	before := count()
	p := review("send", in)
	if count() != before || requests.Load() != 0 {
		t.Fatal("preview side effects")
	}
	raw, _ := json.Marshal(p)
	if strings.Contains(string(raw), "never-exposed-secret") || strings.Contains(string(raw), "fixture-buyer") {
		t.Fatal("credentials in preview")
	}
	if p["effects"].(map[string]any)["total_cents"] != float64(3000) {
		t.Fatal("complete paid preview", p)
	}
	a := final(in, p)
	if status, _ := call(2, "send", "member-send", a); status != 403 {
		t.Fatal("member sender", status)
	}
	if status, _ := callHandler(handler, 1, "cores:procurement:approve", "wrong-action", "POST", "/mcp/orders/send-amazon", a); status != 403 {
		t.Fatal("scope", status)
	}
	bad := clone(a)
	bad["confirmation_text"] = "SEND AMAZON"
	if status, _ := call(1, "send", "weak-phrase", bad); status != 428 || count() != before || requests.Load() != 0 {
		t.Fatal("weak paid phrase", status)
	}
	configure("changed-ship-street")
	if status, o := call(1, "send", "changed-destination", a); status != 200 || o["ready_to_execute"] != false || count() != before || requests.Load() != 0 {
		t.Fatal("destination context", status, o)
	}
	configure("ship-street")
	must(db, `CREATE OR REPLACE FUNCTION reject_send_audit() RETURNS TRIGGER AS $$ BEGIN IF NEW.action='order.amazon_submission_requested' THEN RAISE EXCEPTION 'forced supplier claim audit';END IF;RETURN NEW;END $$ LANGUAGE plpgsql;CREATE TRIGGER reject_send_audit BEFORE INSERT ON audit_log FOR EACH ROW EXECUTE FUNCTION reject_send_audit()`)
	if status, _ := call(1, "send", "claim-atomic", a); status != 500 || count() != before || requests.Load() != 0 {
		t.Fatal("claim audit before external", status)
	}
	must(db, "DROP TRIGGER reject_send_audit ON audit_log")
	status, out := call(1, "send", "claim-atomic", a)
	if status != 200 || out["operation_status"] != "sent" || requests.Load() != 1 {
		t.Fatal("paid submission", status, out, requests.Load())
	}
	booked := count()
	_, replay := call(1, "send", "claim-atomic", a)
	if !reflect.DeepEqual(out, replay) || count() != booked || requests.Load() != 1 {
		t.Fatal("send replay", replay)
	}
	if status, o := call(1, "send", "new-key-send", a); status != 200 || o["ready_to_execute"] != false || requests.Load() != 1 || count() != booked {
		t.Fatal("new key duplicate", status, o)
	}
	must(db, "UPDATE users SET is_admin=false WHERE userid=1")
	if status, _ := call(1, "send", "claim-atomic", a); status != 403 {
		t.Fatal("revoked sender replay", status)
	}
	must(db, "UPDATE users SET is_admin=true WHERE userid=1")
	if err := db.Exec("UPDATE proc_purchase_orders SET status='draft' WHERE id=?", order.ID).Error; err == nil {
		t.Fatal("legacy resubmission reset allowed")
	}
	if err := db.Exec("UPDATE proc_purchase_order_lines SET quantity=4 WHERE id=?", order.Lines[0].ID).Error; err == nil {
		t.Fatal("submitted commercial line edited")
	}
	if err := db.Exec("DELETE FROM proc_order_submissions WHERE purchase_order_id=?", order.ID).Error; err == nil {
		t.Fatal("durable barrier deleted")
	}
	// Acknowledgement is durable before final local order status/audit changes.
	order = newOrder()
	in = map[string]any{"id": order.ID, "preview": true}
	p = review("send", in)
	a = final(in, p)
	must(db, `CREATE OR REPLACE FUNCTION reject_send_audit() RETURNS TRIGGER AS $$ BEGIN IF NEW.action='order.ordered_at_amazon' THEN RAISE EXCEPTION 'forced final supplier audit';END IF;RETURN NEW;END $$ LANGUAGE plpgsql;CREATE TRIGGER reject_send_audit BEFORE INSERT ON audit_log FOR EACH ROW EXECUTE FUNCTION reject_send_audit()`)
	if status, _ := call(1, "send", "final-audit-retry", a); status != 500 || requests.Load() != 2 {
		t.Fatal("final audit failure", status)
	}
	var saved orderSubmissionRecord
	db.Where("purchase_order_id=?", order.ID).First(&saved)
	if saved.Status != "accepted" {
		t.Fatal("acknowledgement lost", saved.Status)
	}
	must(db, "DROP TRIGGER reject_send_audit ON audit_log")
	if status, o := call(1, "send", "final-audit-retry", a); status != 200 || o["operation_status"] != "sent" || requests.Load() != 2 {
		t.Fatal("finalization retry resends", status, o)
	}
	// Losing the outcome write preserves the committed pending duplicate barrier.
	order = newOrder()
	in = map[string]any{"id": order.ID, "preview": true}
	p = review("send", in)
	a = final(in, p)
	must(db, `CREATE OR REPLACE FUNCTION reject_send_audit() RETURNS TRIGGER AS $$ BEGIN IF NEW.action='order.amazon_submission_outcome' THEN RAISE EXCEPTION 'forced supplier outcome audit';END IF;RETURN NEW;END $$ LANGUAGE plpgsql;CREATE TRIGGER reject_send_audit BEFORE INSERT ON audit_log FOR EACH ROW EXECUTE FUNCTION reject_send_audit()`)
	if status, _ := call(1, "send", "outcome-audit-retry", a); status != 500 || requests.Load() != 3 {
		t.Fatal("outcome audit failure", status)
	}
	must(db, "DROP TRIGGER reject_send_audit ON audit_log")
	if status, o := call(1, "send", "outcome-audit-retry", a); status != 200 || o["operation_status"] != "pending" || requests.Load() != 3 {
		t.Fatal("pending retry resends", status, o)
	}
	// An ambiguous HTTP response never permits a retry with the same or new key.
	order = newOrder()
	in = map[string]any{"id": order.ID, "preview": true}
	p = review("send", in)
	a = final(in, p)
	fail.Store(true)
	if status, o := call(1, "send", "uncertain-response", a); status != 200 || o["operation_status"] != "submission_unknown" || requests.Load() != 4 {
		t.Fatal("unknown outcome", status, o)
	}
	fail.Store(false)
	if status, o := call(1, "send", "uncertain-response", a); status != 200 || o["operation_status"] != "submission_unknown" || requests.Load() != 4 {
		t.Fatal("unknown retry resends", status, o)
	}
	if status, o := call(1, "send", "uncertain-new-key", a); status != 200 || o["ready_to_execute"] != false || requests.Load() != 4 {
		t.Fatal("unknown new key resends", status, o)
	}
	if err := db.Exec("UPDATE proc_purchase_orders SET status='draft' WHERE id=?", order.ID).Error; err == nil {
		t.Fatal("unknown reset")
	}
	// Concurrent different keys still claim the same order once.
	order = newOrder()
	in = map[string]any{"id": order.ID, "preview": true}
	p = review("send", in)
	a = final(in, p)
	var wg sync.WaitGroup
	results := make(chan map[string]any, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			_, o := call(1, "send", fmt.Sprintf("concurrent-send-%d", n), a)
			results <- o
		}(i)
	}
	wg.Wait()
	close(results)
	sent := 0
	for o := range results {
		if o["operation_status"] == "sent" {
			sent++
		}
	}
	if sent != 1 || requests.Load() != 5 {
		t.Fatal("concurrent external duplicate", sent, requests.Load())
	}
	// The existing UI API shares the same durable claim and atomic audits.
	order = newOrder()
	if status, _ := callHandler(handler, 1, "UI", "", "POST", fmt.Sprintf("/orders/%d/amazon/submit", order.ID), map[string]any{}); status != 200 || requests.Load() != 6 {
		t.Fatal("native UI send", status)
	}
	if status, _ := callHandler(handler, 1, "UI", "", "POST", fmt.Sprintf("/orders/%d/amazon/submit", order.ID), map[string]any{}); status != 409 || requests.Load() != 6 {
		t.Fatal("native UI duplicate", status)
	}
	if status, _ := callHandler(handler, 1, "cores:procurement:send", "public-bypass", "POST", fmt.Sprintf("/orders/%d/amazon/submit", order.ID), map[string]any{}); status != 428 {
		t.Fatal("old MCP shortcut", status)
	}
}
