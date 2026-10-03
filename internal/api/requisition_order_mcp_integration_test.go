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

func TestRequisitionOrderContextConcurrencyAndAtomicReplay(t *testing.T) {
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
	const schema = "procurement_requisition_order_owner_test"
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

	supplier := models.Supplier{Name: "Exact supplier", Code: "EXACT", Active: true}
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
		return callHandler(handler, uid, "cores:procurement:create", key, "POST", "/mcp/requisition-orders", in)
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
		if err := db.Raw("SELECT jsonb_build_array((SELECT count(*) FROM proc_purchase_orders),(SELECT count(*) FROM proc_purchase_order_lines),(SELECT count(*) FROM proc_idempotency_records),(SELECT count(*) FROM audit_log),(SELECT count(*) FROM proc_activities),(SELECT jsonb_agg(status ORDER BY id) FROM proc_requisitions))::text").Row().Scan(&out); err != nil {
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

	seq := 0
	newReq := func() models.Requisition {
		seq++
		row := models.Requisition{Number: fmt.Sprintf("BAN-CONVERT-%d", seq), Title: "Approved original", Status: "approved", RequesterID: 2, RequesterName: "Original requester", CostCenter: "CC-original", Justification: "Keep original", ApprovedBy: func() *uint { v := uint(1); return &v }(), ApprovedByName: "original admin", DecisionNote: "Reviewed", Lines: []models.RequisitionLine{{ProductID: &product.ID, Description: "First original", Quantity: 2.5, Unit: "m", EstimatedPriceCents: 101, SupplierPartID: "opaque1", SupplierPartAuxiliaryID: "opaque2"}, {Description: "Free line", Quantity: 1, EstimatedPriceCents: 50, PreferredSupplierID: &supplier.ID}}}
		if err := db.Create(&row).Error; err != nil {
			t.Fatal(err)
		}
		return row
	}
	req := newReq()
	offer := models.Offer{ProductID: product.ID, SupplierID: supplier.ID, PriceCents: 80, Currency: "EUR", MinimumQuantity: 1, PackSize: 0.5, Active: true, PurchaseURL: "https://supplier.example/product"}
	if err := db.Create(&offer).Error; err != nil {
		t.Fatal(err)
	}
	in := map[string]any{"id": req.ID, "supplier_id": supplier.ID, "expected_delivery": "2026-12-01T12:00:00Z", "preview": true}
	before := count()
	p := review("create", in)
	if count() != before {
		t.Fatal("preview mutated")
	}
	draft := p["draft"].(map[string]any)
	if draft["totalCents"] != float64(250) || draft["requisitionId"] != float64(req.ID) || draft["lines"].([]any)[0].(map[string]any)["supplierPartAuxiliaryId"] != "opaque2" {
		t.Fatal("derived draft", p)
	}
	a := final(in, p)
	if status, _ := call(2, "create", "member-conversion", a); status != 403 {
		t.Fatal("member", status)
	}
	if status, _ := callHandler(handler, 1, "cores:procurement:approve", "wrong-action", "POST", "/mcp/requisition-orders", a); status != 403 {
		t.Fatal("signed action", status)
	}
	if status, _ := callHandler(handler, 1, "cores:procurement:create", "legacy-shortcut", "POST", fmt.Sprintf("/requisitions/%d/order", req.ID), map[string]any{"supplierId": supplier.ID}); status != 428 {
		t.Fatal("legacy shortcut", status)
	}
	bad := clone(a)
	bad["confirmation_text"] = "CREATE ORDER"
	if status, _ := call(1, "create", "weak-phrase", bad); status != 428 || count() != before {
		t.Fatal("weak phrase", status)
	}
	bad = clone(a)
	delete(bad, "expected_updated_at")
	if status, o := call(1, "create", "missing-version", bad); status != 200 || o["ready_to_execute"] != false || count() != before {
		t.Fatal("missing version", status, o)
	}
	must(db, "UPDATE proc_offers SET price_cents=81 WHERE id=?", offer.ID)
	if status, o := call(1, "create", "stale-offer", a); status != 200 || o["ready_to_execute"] != false || count() != before {
		t.Fatal("offer context", status, o)
	}
	for _, action := range []string{"order.created_from_requisition", "requisition.ordered"} {
		p = review("create", in)
		a = final(in, p)
		must(db, fmt.Sprintf(`CREATE OR REPLACE FUNCTION reject_conversion_audit() RETURNS TRIGGER AS $$ BEGIN IF NEW.action='%s' THEN RAISE EXCEPTION 'forced conversion audit';END IF;RETURN NEW;END $$ LANGUAGE plpgsql;CREATE TRIGGER reject_conversion_audit BEFORE INSERT ON audit_log FOR EACH ROW EXECUTE FUNCTION reject_conversion_audit()`, action))
		if status, _ := call(1, "create", "audit-atomic", a); status != 500 || count() != before {
			t.Fatal("both audits atomic", action, status)
		}
		must(db, "DROP TRIGGER reject_conversion_audit ON audit_log")
	}
	status, out := call(1, "create", "audit-atomic", a)
	if status != 200 {
		t.Fatal(status, out)
	}
	original := out["requisition"].(map[string]any)
	if original["status"] != "ordered" || original["decisionNote"] != req.DecisionNote || original["costCenter"] != req.CostCenter || original["lines"].([]any)[0].(map[string]any)["id"] != float64(req.Lines[0].ID) {
		t.Fatal("retained requisition", original)
	}
	booked := count()
	_, replay := call(1, "create", "audit-atomic", a)
	if !reflect.DeepEqual(out, replay) || count() != booked {
		t.Fatal("durable replay", replay)
	}
	if status, o := call(1, "create", "second-booking", a); status != 200 || o["ready_to_execute"] != false || count() != booked {
		t.Fatal("duplicate conversion", status, o)
	}
	must(db, "UPDATE users SET is_admin=false WHERE userid=1")
	if status, _ := call(1, "create", "audit-atomic", a); status != 403 {
		t.Fatal("current replay rights", status)
	}
	must(db, "UPDATE users SET is_admin=true WHERE userid=1")
	bad = clone(a)
	bad["supplier_id"] = 999
	if status, _ := call(1, "create", "audit-atomic", bad); status != 409 {
		t.Fatal("changed replay payload", status)
	}
	req = newReq()
	in = map[string]any{"id": req.ID, "supplier_id": supplier.ID, "preview": true}
	p = review("create", in)
	a = final(in, p)
	must(db, "UPDATE proc_requisition_lines SET description='Native edited' WHERE id=?", req.Lines[0].ID)
	if status, o := call(1, "create", "stale-line", a); status != 200 || o["ready_to_execute"] != false {
		t.Fatal("native line context", status, o)
	}
	p = review("create", in)
	a = final(in, p)
	var wg sync.WaitGroup
	results := make(chan map[string]any, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			_, o := call(1, "create", fmt.Sprintf("concurrent-conversion-%d", n), a)
			results <- o
		}(i)
	}
	wg.Wait()
	close(results)
	created := 0
	for o := range results {
		if o["operation_status"] == "created" {
			created++
		}
	}
	if created != 1 {
		t.Fatal("concurrent duplicate", created)
	}
	var n int64
	db.Model(&models.PurchaseOrder{}).Where("requisition_id=?", req.ID).Count(&n)
	if n != 1 {
		t.Fatal("duplicate orders", n)
	}
	// The existing UI conversion uses the same locked derivation and both audits.
	req = newReq()
	ui := map[string]any{"supplierId": supplier.ID}
	results = make(chan map[string]any, 2)
	codes := make(chan int, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			code, o := callHandler(handler, 1, "UI", "", "POST", fmt.Sprintf("/requisitions/%d/order", req.ID), ui)
			codes <- code
			results <- o
		}()
	}
	wg.Wait()
	close(codes)
	close(results)
	created = 0
	for code := range codes {
		if code == 201 {
			created++
		} else if code != 409 {
			t.Fatal("UI conversion", code)
		}
	}
	if created != 1 {
		t.Fatal("UI concurrent duplicate", created)
	}
	req = newReq()
	in = map[string]any{"id": req.ID, "supplier_id": supplier.ID, "preview": true}
	for _, change := range []string{"minimum_quantity=3", "minimum_quantity=1,pack_size=2", "pack_size=0.5,currency='USD'", "currency='EUR',valid_until=now()-interval '1 day'"} {
		must(db, "UPDATE proc_offers SET "+change+" WHERE id=?", offer.ID)
		if status, o := call(1, "create", "", in); status != 200 || o["ready_to_execute"] != false {
			t.Fatal("offer business constraint", change, status, o)
		}
	}
	must(db, "UPDATE proc_offers SET valid_until=NULL WHERE id=?", offer.ID)
	p = review("create", in)
	must(db, "UPDATE proc_requisitions SET status='submitted' WHERE id=?", req.ID)
	if status, o := call(1, "create", "nonapproved", final(in, p)); status != 200 || o["ready_to_execute"] != false {
		t.Fatal("approval shortcut", status, o)
	}
	must(db, "UPDATE proc_requisitions SET status='approved' WHERE id=?", req.ID)
	must(db, "UPDATE proc_requisition_lines SET preferred_supplier_id=? WHERE id=?", supplier.ID, req.Lines[0].ID)
	p = review("create", in)
	if p["draft"].(map[string]any)["lines"].([]any)[0].(map[string]any)["unitPriceCents"] != float64(101) {
		t.Fatal("preferred estimate changed", p)
	}
	// Retained Amazon vendor identity requires the original Amazon Business supplier.
	req = newReq()
	must(db, "UPDATE proc_requisitions SET amazon_punchout_session_id=99999 WHERE id=?", req.ID)
	in = map[string]any{"id": req.ID, "supplier_id": supplier.ID, "preview": true}
	if status, o := call(1, "create", "", in); status != 200 || o["ready_to_execute"] != false {
		t.Fatal("Amazon supplier shortcut", status, o)
	}
	amazonSupplier := models.Supplier{Name: "Amazon Business", Code: "AMAZON-BUSINESS", Active: true}
	if err := db.Create(&amazonSupplier).Error; err != nil {
		t.Fatal(err)
	}
	in["supplier_id"] = amazonSupplier.ID
	p = review("create", in)
	if p["draft"].(map[string]any)["amazonPunchoutSessionId"] != float64(99999) || p["draft"].(map[string]any)["lines"].([]any)[0].(map[string]any)["supplierPartId"] != "opaque1" {
		t.Fatal("Amazon source identity", p)
	}
	if status, o := call(1, "create", "amazon-conversion", final(in, p)); status != 200 || o["purchase_order"].(map[string]any)["status"] != "draft" {
		t.Fatal("Amazon draft", status, o)
	}
	req = newReq()
	must(db, "UPDATE proc_requisitions SET status='rejected' WHERE id=?", req.ID)
	must(db, "UPDATE proc_requisitions SET is_archived=true,archived_at=now() WHERE id=?", req.ID)
	if status, o := call(1, "create", "", map[string]any{"id": req.ID, "supplier_id": supplier.ID, "preview": true}); status != 200 || o["ready_to_execute"] != false {
		t.Fatal("archived conversion", status, o)
	}

}
