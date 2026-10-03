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

	"github.com/go-chi/chi/v5"
	"github.com/golang-jwt/jwt/v5"
	commonjwt "github.com/nbt4/cores-common/pkg/jwt"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func TestOrderDraftOwnerContextIdentityRightsAtomicReplay(t *testing.T) {
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
	const schema = "procurement_order_draft_owner_test"
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
	legacyRoutes := chi.NewRouter()
	legacyRoutes.Post("/orders", h.createOrder)
	legacyRoutes.Put("/orders/{id}/draft", h.updateOrderDraft)
	legacy := auth.Middleware(commonjwt.DatabaseUserLookup(sqlDB), legacyRoutes)
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
		r.Header.Set("X-Cores-Origin", "MCP/AI")
		r.Header.Set("Idempotency-Key", key)
		w := httptest.NewRecorder()
		target.ServeHTTP(w, r)
		out := map[string]any{}
		_ = json.Unmarshal(w.Body.Bytes(), &out)
		return w.Code, out
	}
	call := func(uid uint, op, key string, in map[string]any) (int, map[string]any) {
		return callHandler(handler, uid, "cores:procurement:"+op, key, "POST", "/mcp/order-drafts/"+op, in)
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
		if err := db.Raw("SELECT jsonb_build_array((SELECT count(*) FROM proc_purchase_orders),(SELECT count(*) FROM proc_purchase_order_lines),(SELECT count(*) FROM proc_idempotency_records),(SELECT count(*) FROM audit_log),(SELECT count(*) FROM proc_activities))::text").Row().Scan(&out); err != nil {
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
	in := map[string]any{"supplier_query": "EXACT", "supplier_order_number": "REVIEWED-1", "currency": "eur", "notes": "Private draft note", "order_date": "2026-10-03", "expected_delivery": "2026-12-01", "lines": []any{map[string]any{"product_id": product.ID, "quantity": 2.5, "unit": "m", "unit_price_cents": 101}}, "preview": true}
	before := count()
	p := review("create", in)
	if count() != before {
		t.Fatal("preview mutated")
	}
	draft := p["draft"].(map[string]any)
	if draft["totalCents"] != float64(252) || draft["orderedBy"] != float64(1) || draft["lines"].([]any)[0].(map[string]any)["description"] != product.Name {
		t.Fatal("canonical draft", p)
	}
	a := final(in, p)
	if status, _ := call(2, "create", "member-create", a); status != 403 {
		t.Fatal("nonadmin create", status)
	}
	if status, _ := callHandler(handler, 1, "cores:procurement:update", "wrong-create-scope", "POST", "/mcp/order-drafts/create", a); status != 403 {
		t.Fatal("wrong action", status)
	}
	bad := clone(a)
	bad["confirmation_text"] = "CREATE ORDER DRAFT"
	if status, _ := call(1, "create", "weak-phrase", bad); status != 428 || count() != before {
		t.Fatal("weak phrase", status)
	}
	must(db, "UPDATE proc_suppliers SET name='Native changed supplier' WHERE id=?", supplier.ID)
	if status, out := call(1, "create", "stale-supplier", a); status != 200 || out["ready_to_execute"] != false || count() != before {
		t.Fatal("supplier context omitted", status, out)
	}
	p = review("create", in)
	a = final(in, p)
	must(db, `CREATE OR REPLACE FUNCTION reject_order_draft_audit() RETURNS TRIGGER AS $$ BEGIN IF NEW.action='order.created' THEN RAISE EXCEPTION 'forced final draft audit';END IF;RETURN NEW;END $$ LANGUAGE plpgsql;CREATE TRIGGER reject_order_draft_audit BEFORE INSERT ON audit_log FOR EACH ROW EXECUTE FUNCTION reject_order_draft_audit()`)
	if status, _ := call(1, "create", "create-atomic", a); status != 500 || count() != before {
		t.Fatal("creation audit not atomic", status)
	}
	must(db, "DROP TRIGGER reject_order_draft_audit ON audit_log")
	status, out := call(1, "create", "create-atomic", a)
	if status != 200 {
		t.Fatal("creation", status, out)
	}
	row := out["purchase_order"].(map[string]any)
	id := row["id"]
	lid := row["lines"].([]any)[0].(map[string]any)["id"]
	_, replay := call(1, "create", "create-atomic", a)
	if !reflect.DeepEqual(out, replay) {
		t.Fatal("creation replay", replay)
	}
	_, dupe := call(1, "create", "", in)
	if dupe["ready_to_execute"] != false || len(dupe["duplicates"].([]any)) != 1 {
		t.Fatal("duplicate supplier number", dupe)
	}
	bad = clone(in)
	bad["status"] = "confirmed"
	if status, _ := call(1, "create", "", bad); status != 400 {
		t.Fatal("initial confirmed status", status)
	}
	u := map[string]any{"id": id, "notes": "Revised note", "order_date": "", "expected_delivery": "", "currency": "CHF", "preview": true}
	p = review("update", u)
	a = final(u, p)
	must(db, "UPDATE proc_purchase_order_lines SET description='Native edited line' WHERE id=?", lid)
	before = count()
	if status, out := call(1, "update", "stale-line", a); status != 200 || out["ready_to_execute"] != false || count() != before {
		t.Fatal("native line context", status, out)
	}
	p = review("update", u)
	a = final(u, p)
	must(db, `CREATE OR REPLACE FUNCTION reject_order_draft_audit() RETURNS TRIGGER AS $$ BEGIN IF NEW.action='order.draft_updated' THEN RAISE EXCEPTION 'forced final update audit';END IF;RETURN NEW;END $$ LANGUAGE plpgsql;CREATE TRIGGER reject_order_draft_audit BEFORE INSERT ON audit_log FOR EACH ROW EXECUTE FUNCTION reject_order_draft_audit()`)
	if status, _ := call(1, "update", "update-atomic", a); status != 500 || count() != before {
		t.Fatal("update audit not atomic", status)
	}
	must(db, "DROP TRIGGER reject_order_draft_audit ON audit_log")
	status, out = call(1, "update", "update-atomic", a)
	if status != 200 {
		t.Fatal(status, out)
	}
	row = out["purchase_order"].(map[string]any)
	if row["lines"].([]any)[0].(map[string]any)["id"] != lid || row["orderDate"] != nil || row["expectedDelivery"] != nil || row["currency"] != "CHF" || row["status"] != "draft" {
		t.Fatal("metadata/identity", out)
	}
	replacement := map[string]any{"id": id, "lines": []any{map[string]any{"description": "New input first", "quantity": 1, "unit_price_cents": 50}, map[string]any{"line_id": lid, "product_id": product.ID, "description": "Retained line", "quantity": 3, "unit": "m", "unit_price_cents": 200}}, "preview": true}
	p = review("update", replacement)
	if p["draft"].(map[string]any)["lines"].([]any)[0].(map[string]any)["id"] != lid {
		t.Fatal("canonical native line order", p)
	}
	status, out = call(1, "update", "replace-lines", final(replacement, p))
	if status != 200 || out["purchase_order"].(map[string]any)["totalCents"] != float64(650) || out["purchase_order"].(map[string]any)["lines"].([]any)[0].(map[string]any)["id"] != lid {
		t.Fatal("retained replacement", status, out)
	}
	bad = clone(replacement)
	bad["lines"] = []any{map[string]any{"line_id": 999999, "description": "Foreign", "quantity": 1}}
	if status, _ := call(1, "update", "", bad); status != 409 {
		t.Fatal("foreign line", status)
	}
	concurrent := map[string]any{"id": id, "notes": "Concurrent final note", "preview": true}
	p = review("update", concurrent)
	a = final(concurrent, p)
	ch := make(chan bool, 2)
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			status, out := call(1, "update", fmt.Sprintf("concurrent-%d", i), a)
			ch <- status == 200 && out["operation_status"] == "updated"
		}(i)
	}
	wg.Wait()
	close(ch)
	commits := 0
	for ok := range ch {
		if ok {
			commits++
		}
	}
	if commits != 1 {
		t.Fatal("concurrent commits", commits)
	}
	// Ordinary status, archive and Amazon state cannot be changed through draft edits.
	must(db, "UPDATE proc_purchase_orders SET status='sent' WHERE id=?", id)
	_, blocked := call(1, "update", "", u)
	if blocked["ready_to_execute"] != false {
		t.Fatal("sent order editable", blocked)
	}
	must(db, "UPDATE proc_purchase_orders SET status='cancelled',is_archived=false WHERE id=?", id)
	must(db, "UPDATE proc_purchase_orders SET is_archived=true WHERE id=?", id)
	_, blocked = call(1, "update", "", u)
	if blocked["ready_to_execute"] != false {
		t.Fatal("archived order editable", blocked)
	}
	// Legacy hashes are generated by the actual previous private native functions.
	old := models.PurchaseOrder{SupplierID: supplier.ID, Status: "draft", Currency: "EUR", SupplierOrderNumber: "OLD-1", Notes: "Legacy original", Lines: []models.PurchaseOrderLine{{ProductID: &product.ID, Description: "Legacy line", Quantity: 2.5, Unit: "m", UnitPriceCents: 101}}}
	status, oldCreated := callHandler(legacy, 1, "cores:procurement:create", "old-create", "POST", "/orders", old)
	if status != 201 {
		t.Fatal("old create", status, oldCreated)
	}
	oid := int64(oldCreated["id"].(float64))
	version, _ := time.Parse(time.RFC3339Nano, oldCreated["updatedAt"].(string))
	oldUpdate := orderDraftUpdateInput{PurchaseOrder: models.PurchaseOrder{SupplierID: supplier.ID, Status: "draft", Currency: "EUR", SupplierOrderNumber: "OLD-1", Notes: "Legacy revised", Lines: old.Lines}, ExpectedUpdatedAt: &version}
	status, oldAfter := callHandler(legacy, 1, "cores:procurement:update", "old-update", "PUT", fmt.Sprintf("/orders/%d/draft", oid), oldUpdate)
	if status != 200 {
		t.Fatal("old update", status, oldAfter)
	}
	for _, test := range []struct {
		op, key      string
		input        map[string]any
		want         map[string]any
		method, path string
		payload      any
	}{
		{"create", "old-create", map[string]any{"supplier_id": supplier.ID, "supplier_order_number": "OLD-1", "currency": "EUR", "notes": "Legacy original", "lines": []any{map[string]any{"product_id": product.ID, "description": "Legacy line", "quantity": 2.5, "unit": "m", "unit_price_cents": 101}}, "confirm_change": true}, oldCreated, "POST", "/orders", old},
		{"update", "old-update", map[string]any{"id": oid, "notes": "Legacy revised", "expected_updated_at": version.Format(time.RFC3339Nano), "confirm_change": true}, oldAfter, "PUT", fmt.Sprintf("/orders/%d/draft", oid), oldUpdate},
	} {
		before = count()
		status, out := call(1, test.op, test.key, test.input)
		if status != 200 || !reflect.DeepEqual(out["purchase_order"], test.want) || count() != before {
			t.Fatal("old owner replay", test.op, status, out)
		}
		status, out = callHandler(handler, 1, "cores:procurement:"+test.op, test.key, test.method, test.path, test.payload)
		if status < 200 || status > 201 || !reflect.DeepEqual(out, test.want) {
			t.Fatal("public old replay", test.op, status, out)
		}
		if status, _ := callHandler(handler, 1, "cores:procurement:"+test.op, "unbooked-"+test.op, test.method, test.path, test.payload); status != 428 {
			t.Fatal("unbooked public write", test.op, status)
		}
		bad := clone(test.input)
		bad["notes"] = "Different original"
		if status, _ := call(1, test.op, test.key, bad); status != 409 || count() != before {
			t.Fatal("changed old payload", test.op, status)
		}
	}

	// Imported Amazon carts and drafts with retained receipts/confirmations are not editable.
	must(db, "UPDATE proc_purchase_orders SET amazon_punchout_session_id=999 WHERE id=?", oid)
	_, blocked = call(1, "update", "", map[string]any{"id": oid, "notes": "Unapproved cart edit", "preview": true})
	if blocked["ready_to_execute"] != false {
		t.Fatal("Amazon cart editable", blocked)
	}
	must(db, "UPDATE proc_purchase_orders SET amazon_punchout_session_id=NULL WHERE id=?", oid)
	oldLine := oldAfter["lines"].([]any)[0].(map[string]any)["id"]
	receipt := models.Receipt{PurchaseOrderID: uint(oid), PurchaseOrderLineID: uint(oldLine.(float64)), Quantity: 1, ReceivedAt: time.Now()}
	if err := db.Create(&receipt).Error; err != nil {
		t.Fatal(err)
	}
	_, blocked = call(1, "update", "", map[string]any{"id": oid, "notes": "Unapproved receipt edit", "preview": true})
	if blocked["ready_to_execute"] != false {
		t.Fatal("receipted draft editable", blocked)
	}
	// Two confirmed creations using the same reviewed supplier number commit once.
	creationRace := clone(in)
	creationRace["supplier_order_number"] = "CONCURRENT-CREATE"
	p = review("create", creationRace)
	createArgs := final(creationRace, p)
	ch = make(chan bool, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			status, out := call(1, "create", fmt.Sprintf("concurrent-create-%d", i), createArgs)
			ch <- status == 200 && out["operation_status"] == "created"
		}(i)
	}
	wg.Wait()
	close(ch)
	commits = 0
	for ok := range ch {
		if ok {
			commits++
		}
	}
	if commits != 1 {
		t.Fatal("concurrent duplicate creations", commits)
	}
	must(db, "UPDATE users SET is_admin=false WHERE userid=1")
	if status, _ := call(1, "create", "create-atomic", map[string]any{"confirm_change": true}); status != 403 {
		t.Fatal("revoked admin replay", status)
	}
	must(db, "UPDATE users SET is_admin=true WHERE userid=1")
}
