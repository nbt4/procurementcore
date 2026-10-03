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

func TestRequisitionDraftOwnerContextIdentityRightsAtomicReplay(t *testing.T) {
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
	const schema = "procurement_requisition_owner_test"
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

	t.Setenv("CORES_JWT_SECRET", "requisition-owner-secret-at-least-32-bytes")
	h := &Handler{db: db}
	handler := auth.Middleware(commonjwt.DatabaseUserLookup(sqlDB), h.Routes())
	legacyRoutes := chi.NewRouter()
	legacyRoutes.Post("/requisitions", h.createRequisition)
	legacyRoutes.Put("/requisitions/{id}", h.updateRequisition)
	legacyRoutes.Post("/requisitions/{id}/submit", h.submitRequisition)
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
		return callHandler(handler, uid, "cores:procurement:"+op, key, "POST", "/mcp/requisitions/"+op, in)
	}
	count := func() string {
		t.Helper()
		var out string
		if err := db.Raw("SELECT jsonb_build_array((SELECT count(*) FROM proc_requisitions),(SELECT count(*) FROM proc_requisition_lines),(SELECT count(*) FROM audit_log),(SELECT count(*) FROM proc_activities),(SELECT count(*) FROM proc_idempotency_records))::text").Row().Scan(&out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	clone := func(in map[string]any) map[string]any {
		out := map[string]any{}
		for k, v := range in {
			out[k] = v
		}
		return out
	}
	review := func(uid uint, op string, in map[string]any) map[string]any {
		t.Helper()
		status, p := call(uid, op, "", in)
		if status != 200 || p["ready_to_execute"] != true {
			t.Fatal("preview", status, p)
		}
		return p
	}
	final := func(in, p map[string]any) map[string]any {
		out := clone(in)
		out["preview"] = false
		out["confirm_change"] = true
		out["expected_updated_at"] = p["expected_updated_at"]
		out["expected_context"] = p["expected_context"]
		out["confirmation_text"] = p["required_confirmation_text"]
		return out
	}
	line := map[string]any{"description": "Complete draft line", "quantity": 2.5, "estimated_price_cents": 120}
	in := map[string]any{"title": "Complete draft", "cost_center": "EVENT", "justification": "Private justification", "needed_by": "2026-12-01T10:00:00+01:00", "lines": []any{line}, "preview": true}
	before := count()
	p := review(2, "create", in)
	if count() != before {
		t.Fatal("preview mutated")
	}
	if p["draft"].(map[string]any)["estimatedTotalCents"] != float64(300) || p["draft"].(map[string]any)["requesterId"] != float64(2) {
		t.Fatal(p)
	}
	a := final(in, p)
	if status, _ := callHandler(handler, 2, "cores:procurement:update", "wrong-scope", "POST", "/mcp/requisitions/create", a); status != 403 {
		t.Fatal("wrong create scope", status)
	}
	bad := clone(a)
	bad["confirmation_text"] = "CREATE REQUISITION"
	if status, _ := call(2, "create", "wrong-phrase", bad); status != 428 || count() != before {
		t.Fatal("weak phrase", status)
	}
	must(db, `CREATE OR REPLACE FUNCTION reject_requisition_audit() RETURNS TRIGGER AS $$ BEGIN IF NEW.action='requisition.created' THEN RAISE EXCEPTION 'forced draft audit';END IF;RETURN NEW;END $$ LANGUAGE plpgsql;CREATE TRIGGER reject_requisition_audit BEFORE INSERT ON audit_log FOR EACH ROW EXECUTE FUNCTION reject_requisition_audit()`)
	if status, _ := call(2, "create", "create-atomic", a); status != 500 || count() != before {
		t.Fatal("audit not atomic", status)
	}
	must(db, "DROP TRIGGER reject_requisition_audit ON audit_log")
	status, created := call(2, "create", "create-atomic", a)
	if status != 200 {
		t.Fatal(status, created)
	}
	row := created["requisition"].(map[string]any)
	id := row["id"]
	originalLine := row["lines"].([]any)[0].(map[string]any)
	lid := originalLine["id"]
	_, replayed := call(2, "create", "create-atomic", a)
	if !reflect.DeepEqual(created, replayed) {
		t.Fatal("durable creation replay", replayed)
	}
	must(db, "UPDATE users SET is_active=false WHERE userid=2")
	if status, _ := call(2, "create", "create-atomic", a); status != 401 && status != 403 {
		t.Fatal("inactive requester replay", status)
	}
	must(db, "UPDATE users SET is_active=true WHERE userid=2")
	_, dupe := call(2, "create", "", in)
	if dupe["ready_to_execute"] != false || len(dupe["duplicates"].([]any)) != 1 {
		t.Fatal("duplicate omitted", dupe)
	}
	distinct := clone(in)
	distinct["allow_duplicate"] = true
	p = review(2, "create", distinct)
	if status, out := call(2, "create", "distinct-create", final(distinct, p)); status != 200 {
		t.Fatal(status, out)
	}
	u := map[string]any{"id": id, "title": "Revised demand", "preview": true}
	if status, _ := call(3, "update", "", u); status != 403 {
		t.Fatal("unrelated requester", status)
	}
	p = review(2, "update", u)
	a = final(u, p)
	must(db, "UPDATE proc_requisition_lines SET description='Native changed line' WHERE id=?", lid)
	before = count()
	if status, out := call(2, "update", "update-stale", a); status != 200 || out["ready_to_execute"] != false || count() != before {
		t.Fatal("stale native line", status, out)
	}
	p = review(2, "update", u)
	a = final(u, p)
	if status, out := call(2, "update", "metadata-update", a); status != 200 || out["requisition"].(map[string]any)["lines"].([]any)[0].(map[string]any)["id"] != lid {
		t.Fatal("metadata changed line identity", status, out)
	}
	// Explicit replacement retains an original ID, adds a new line and shows all quantities/prices.
	replacement := map[string]any{"id": id, "lines": []any{map[string]any{"line_id": lid, "description": "Retained identity", "quantity": 3, "unit": "m", "estimated_price_cents": 200}, map[string]any{"description": "Additional line", "quantity": 1, "estimated_price_cents": 50}}, "preview": true}
	p = review(2, "update", replacement)
	if status, out := call(2, "update", "line-update", final(replacement, p)); status != 200 || out["requisition"].(map[string]any)["estimatedTotalCents"] != float64(650) || out["requisition"].(map[string]any)["lines"].([]any)[0].(map[string]any)["id"] != lid {
		t.Fatal("line replacement", status, out)
	}
	// A replacement list cannot advertise an order the native schema cannot retain.
	canonical := map[string]any{"id": id, "lines": []any{map[string]any{"description": "New before retained", "quantity": 1}, map[string]any{"line_id": lid, "description": "Retained after new input", "quantity": 2}}, "preview": true}
	p = review(2, "update", canonical)
	if p["draft"].(map[string]any)["lines"].([]any)[0].(map[string]any)["id"] != lid {
		t.Fatal("preview order differs from native identity order", p)
	}
	invalid := clone(replacement)
	invalid["lines"] = []any{map[string]any{"line_id": 999999, "description": "Foreign line", "quantity": 1}}
	if status, _ := call(2, "update", "", invalid); status != 409 {
		t.Fatal("foreign line", status)
	}
	// Current owner administrator rights are checked even with a stale signed admin claim.
	u["title"] = "Administrator revised demand"
	p = review(1, "update", u)
	adminArgs := final(u, p)
	if status, out := call(1, "update", "admin-update", adminArgs); status != 200 {
		t.Fatal(status, out)
	}
	must(db, "UPDATE users SET is_admin=false WHERE userid=1")
	if status, _ := call(1, "update", "admin-update", adminArgs); status != 403 {
		t.Fatal("revoked admin replay", status)
	}
	must(db, "UPDATE users SET is_admin=true WHERE userid=1")
	// Exact-version concurrent writes permit only one mutation and receipt.
	concurrent := map[string]any{"id": id, "cost_center": "NEW-COST", "preview": true}
	p = review(2, "update", concurrent)
	a = final(concurrent, p)
	outcomes := make(chan bool, 2)
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			status, out := call(2, "update", fmt.Sprintf("concurrent-update-%d", i), a)
			outcomes <- status == 200 && out["operation_status"] == "updated"
		}(i)
	}
	wg.Wait()
	close(outcomes)
	commits := 0
	for ok := range outcomes {
		if ok {
			commits++
		}
	}
	if commits != 1 {
		t.Fatal("concurrent commits", commits)
	}
	// Return -> revise keeps decision history until resubmission clears active decision fields.
	must(db, "UPDATE proc_requisitions SET status='returned',decision_note='Original return',approved_by=1,approved_by_name='admin',decided_at=now() WHERE id=?", id)
	p = review(2, "update", u)
	a = final(u, p)
	if status, out := call(2, "update", "returned-revision", a); status != 200 || out["requisition"].(map[string]any)["status"] != "draft" {
		t.Fatal(status, out)
	}
	submit := map[string]any{"id": id, "preview": true}
	p = review(2, "submit", submit)
	a = final(submit, p)
	status, out := call(2, "submit", "submit-original", a)
	if status != 200 {
		t.Fatal(status, out)
	}
	row = out["requisition"].(map[string]any)
	if row["status"] != "submitted" || row["decisionNote"] != "" || row["approvedBy"] != nil || row["decidedAt"] != nil || row["lines"].([]any)[0].(map[string]any)["id"] != lid {
		t.Fatal("submission identity/decision", out)
	}
	_, again := call(2, "submit", "submit-original", a)
	if !reflect.DeepEqual(out, again) {
		t.Fatal("submission replay", again)
	}
	// Actual previous private native handlers produce the original receipt hashes.
	old := models.Requisition{Title: "Legacy original", CostCenter: "OLD", Lines: []models.RequisitionLine{{Description: "Legacy line", Quantity: 2, EstimatedPriceCents: 100}}}
	status, oldCreate := callHandler(legacy, 2, "cores:procurement:create", "legacy-create", "POST", "/requisitions", old)
	if status != 201 {
		t.Fatal("old create", status, oldCreate)
	}
	oldID := int64(oldCreate["id"].(float64))
	version, err := time.Parse(time.RFC3339Nano, oldCreate["updatedAt"].(string))
	if err != nil {
		t.Fatal(err)
	}
	oldUpdate := requisitionUpdateInput{Requisition: models.Requisition{Title: "Legacy revised", CostCenter: "OLD", Lines: []models.RequisitionLine{{Description: "Legacy line", Quantity: 2, Unit: "Stk.", EstimatedPriceCents: 100}}}, ExpectedUpdatedAt: &version}
	status, oldAfter := callHandler(legacy, 2, "cores:procurement:update", "legacy-update", "PUT", fmt.Sprintf("/requisitions/%d", oldID), oldUpdate)
	if status != 200 {
		t.Fatal("old update", status, oldAfter)
	}
	submitVersion, _ := time.Parse(time.RFC3339Nano, oldAfter["updatedAt"].(string))
	oldSubmit := requisitionSubmitInput{ExpectedUpdatedAt: &submitVersion}
	status, oldSubmitted := callHandler(legacy, 2, "cores:procurement:submit", "legacy-submit", "POST", fmt.Sprintf("/requisitions/%d/submit", oldID), oldSubmit)
	if status != 200 {
		t.Fatal("old submit", status, oldSubmitted)
	}
	for _, test := range []struct {
		op, key      string
		input        map[string]any
		want         map[string]any
		method, path string
		payload      any
	}{
		{"create", "legacy-create", map[string]any{"title": "Legacy original", "cost_center": "OLD", "lines": []any{map[string]any{"description": "Legacy line", "quantity": 2, "estimated_price_cents": 100}}, "confirm_change": true}, oldCreate, "POST", "/requisitions", old},
		{"update", "legacy-update", map[string]any{"id": oldID, "title": "Legacy revised", "expected_updated_at": version.Format(time.RFC3339Nano), "confirm_change": true}, oldAfter, "PUT", fmt.Sprintf("/requisitions/%d", oldID), oldUpdate},
		{"submit", "legacy-submit", map[string]any{"id": oldID, "expected_updated_at": submitVersion.Format(time.RFC3339Nano), "confirmation_text": fmt.Sprintf("SUBMIT REQUISITION %d", oldID), "confirm_change": true}, oldSubmitted, "POST", fmt.Sprintf("/requisitions/%d/submit", oldID), oldSubmit},
	} {
		before = count()
		status, out := call(2, test.op, test.key, test.input)
		if status != 200 || !reflect.DeepEqual(out["requisition"], test.want) || count() != before {
			t.Fatal("old golden owner replay", test.op, status, out)
		}
		status, out = callHandler(handler, 2, "cores:procurement:"+test.op, test.key, test.method, test.path, test.payload)
		if status < 200 || status > 201 || !reflect.DeepEqual(out, test.want) {
			t.Fatal("public legacy replay", test.op, status, out)
		}
		if status, _ := callHandler(handler, 2, "cores:procurement:"+test.op, "unbooked-"+test.op, test.method, test.path, test.payload); status != 428 {
			t.Fatal("unbooked legacy write", test.op, status)
		}
		bad := clone(test.input)
		if test.op == "submit" {
			bad["expected_updated_at"] = "2026-01-01T00:00:00Z"
		} else {
			bad["title"] = "Changed original"
		}
		if status, _ := call(2, test.op, test.key, bad); status != 409 || count() != before {
			t.Fatal("changed old payload", test.op, status)
		}
	}
	// Every native catalog reference validates under a row lock, including INSERT.
	supplier := models.Supplier{Name: "Reference supplier", Code: "REF", Active: true}
	product := models.Product{SKU: "REF", Name: "Reference product", Active: true, Parameters: json.RawMessage(`{}`), Attributes: json.RawMessage(`{}`)}
	if err := db.Create(&supplier).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&product).Error; err != nil {
		t.Fatal(err)
	}
	must(db, "UPDATE proc_products SET active=false WHERE id=?", product.ID)
	if err := db.Create(&models.RequisitionLine{RequisitionID: uint(id.(float64)), ProductID: &product.ID, Description: "Inactive", Quantity: 1}).Error; err == nil {
		t.Fatal("native inactive product allowed")
	}
	must(db, "UPDATE proc_suppliers SET active=false WHERE id=?", supplier.ID)
	if err := db.Create(&models.RequisitionLine{RequisitionID: uint(id.(float64)), PreferredSupplierID: &supplier.ID, Description: "Inactive", Quantity: 1}).Error; err == nil {
		t.Fatal("native inactive preferred supplier allowed")
	}
	if err := db.Create(&models.PurchaseOrder{Number: "PO-INACTIVE", SupplierID: supplier.ID, Status: "draft"}).Error; err == nil {
		t.Fatal("native inactive order supplier allowed")
	}
	// Reproduce both orderings of native reference validation versus catalog archive.
	for _, archiveFirst := range []bool{true, false} {
		raceProduct := models.Product{SKU: fmt.Sprintf("RACE-%t", archiveFirst), Name: "Concurrent catalog parent", Active: true, Parameters: json.RawMessage(`{}`), Attributes: json.RawMessage(`{}`)}
		if err := db.Create(&raceProduct).Error; err != nil {
			t.Fatal(err)
		}
		nativeTx := db.Begin()
		if nativeTx.Error != nil {
			t.Fatal(nativeTx.Error)
		}
		var active bool
		if err := nativeTx.Raw("SELECT active FROM proc_products WHERE id=?", raceProduct.ID).Row().Scan(&active); err != nil || !active {
			t.Fatal("native validation", err)
		}
		row := models.RequisitionLine{RequisitionID: uint(id.(float64)), ProductID: &raceProduct.ID, Description: "Concurrent native line", Quantity: 1, Unit: "m"}
		result := make(chan error, 1)
		if archiveFirst {
			archiveTx := db.Begin()
			if err := archiveTx.Exec("UPDATE proc_products SET active=false WHERE id=?", raceProduct.ID).Error; err != nil {
				t.Fatal(err)
			}
			go func() { result <- nativeTx.Create(&row).Error }()
			select {
			case err := <-result:
				t.Fatal("reference did not wait for pending archive", err)
			case <-time.After(50 * time.Millisecond):
			}
			if err := archiveTx.Commit().Error; err != nil {
				t.Fatal(err)
			}
			select {
			case err := <-result:
				if err == nil {
					t.Fatal("native insert raced catalog archive")
				}
			case <-time.After(5 * time.Second):
				t.Fatal("reference lock timed out")
			}
			nativeTx.Rollback()
		} else {
			if err := nativeTx.Create(&row).Error; err != nil {
				t.Fatal(err)
			}
			go func() { result <- db.Exec("UPDATE proc_products SET active=false WHERE id=?", raceProduct.ID).Error }()
			select {
			case err := <-result:
				t.Fatal("archive did not wait for native reference", err)
			case <-time.After(50 * time.Millisecond):
			}
			if err := nativeTx.Commit().Error; err != nil {
				t.Fatal(err)
			}
			select {
			case err := <-result:
				if err == nil {
					t.Fatal("archive missed newly committed open demand")
				}
			case <-time.After(5 * time.Second):
				t.Fatal("archive lock timed out")
			}
		}
	}

}
