package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"procurementcore/internal/auth"
	"procurementcore/internal/database"
	"procurementcore/internal/models"
	"procurementcore/internal/scraper"

	"github.com/golang-jwt/jwt/v5"
	commonjwt "github.com/nbt4/cores-common/pkg/jwt"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func TestAdamHallClosedCheckoutDurablePhasesAndCurrentRights(t *testing.T) {
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
	const schema = "procurement_adam_hall_checkout_owner_test"
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

	supplier := models.Supplier{Name: "Adam Hall", Code: "ADAM-HALL", Active: true}
	product := models.Product{SKU: "8747X6", Name: "Default line description", Active: true, Parameters: json.RawMessage(`{}`), Attributes: json.RawMessage(`{}`)}
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
		delegatedScope := scope
		if scope == "UI" {
			delegatedScope = ""
		}
		signed, err := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{"uid": uid, "username": "fixture", "is_admin": true, "mcp_scope": delegatedScope, "exp": time.Now().Add(time.Minute).Unix()}).SignedString(commonjwt.JWTSecret())
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
		return callHandler(handler, uid, "cores:procurement:send", key, "POST", "/mcp/orders/adam-hall/"+op, in)
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
		if err := db.Raw("SELECT jsonb_build_array((SELECT count(*) FROM proc_purchase_orders),(SELECT count(*) FROM proc_purchase_order_lines),(SELECT count(*) FROM proc_idempotency_records),(SELECT count(*) FROM audit_log),(SELECT count(*) FROM proc_activities),(SELECT jsonb_agg(status ORDER BY id) FROM proc_purchase_orders),(SELECT jsonb_agg(status ORDER BY id) FROM proc_order_submissions),(SELECT count(*) FROM proc_submission_reconciliations),(SELECT count(*) FROM proc_adam_hall_checkouts),(SELECT jsonb_agg(status ORDER BY id) FROM proc_adam_hall_checkouts))::text").Row().Scan(&out); err != nil {
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

	provider := &ownerAdamHallFixture{}
	h.scraper = provider
	seq := 0
	newOrder := func() models.PurchaseOrder {
		seq++
		row := models.PurchaseOrder{Number: fmt.Sprintf("AH-OWNER-%d", seq), SupplierID: supplier.ID, Status: "draft", Currency: "EUR", TotalCents: 2000, OrderedBy: 1, Lines: []models.PurchaseOrderLine{{ProductID: &product.ID, Description: "Original draft item", Quantity: 2, Unit: "Stk", UnitPriceCents: 1000}}}
		if err := db.Create(&row).Error; err != nil {
			t.Fatal(err)
		}
		return row
	}
	order := newOrder()
	in := map[string]any{"id": order.ID, "preview": true}
	before := count()
	p := review("cart", in)
	if count() != before || provider.builds.Load() != 0 || provider.sends.Load() != 0 {
		t.Fatal("supplier called by pure preview")
	}
	if code, _ := call(1, "cart", "", map[string]any{"id": order.ID, "table": "proc_purchase_orders"}); code != 400 {
		t.Fatal("arbitrary owner fields", code)
	}
	if code, _ := callHandler(handler, 1, "cores:write", "", "POST", "/mcp/orders/adam-hall/cart", in); code != 403 {
		t.Fatal("legacy write grants cart", code)
	}
	if code, _ := call(2, "cart", "", in); code != 403 {
		t.Fatal("non-admin cart", code)
	}
	a := final(in, p)
	weak := clone(a)
	weak["confirmation_text"] = "BUILD CART"
	if code, _ := call(1, "cart", "weak-cart", weak); code != 428 || count() != before || provider.builds.Load() != 0 {
		t.Fatal("cart phrase", code)
	}
	must(db, `CREATE OR REPLACE FUNCTION reject_ah_audit() RETURNS TRIGGER AS $$ BEGIN IF NEW.action='order.adam_hall_cart_preparation_requested' THEN RAISE EXCEPTION 'forced cart claim audit';END IF;RETURN NEW;END $$ LANGUAGE plpgsql;CREATE TRIGGER reject_ah_audit BEFORE INSERT ON audit_log FOR EACH ROW EXECUTE FUNCTION reject_ah_audit()`)
	if code, _ := call(1, "cart", "cart-claim-retry", a); code != 500 || count() != before || provider.builds.Load() != 0 {
		t.Fatal("cart claim audited before remote call", code)
	}
	must(db, "DROP TRIGGER reject_ah_audit ON audit_log")
	must(db, `CREATE OR REPLACE FUNCTION reject_ah_audit() RETURNS TRIGGER AS $$ BEGIN IF NEW.action='order.adam_hall_cart_preparation_finished' THEN RAISE EXCEPTION 'forced cart final audit';END IF;RETURN NEW;END $$ LANGUAGE plpgsql;CREATE TRIGGER reject_ah_audit BEFORE INSERT ON audit_log FOR EACH ROW EXECUTE FUNCTION reject_ah_audit()`)
	if code, _ := call(1, "cart", "cart-claim-retry", a); code != 500 || provider.builds.Load() != 1 {
		t.Fatal("cart final audit", code)
	}
	must(db, "DROP TRIGGER reject_ah_audit ON audit_log")
	code, ready := call(1, "cart", "cart-claim-retry", a)
	if code != 200 || ready["operation_status"] != "ready" || provider.builds.Load() != 1 || provider.sends.Load() != 0 {
		t.Fatal("cart ready retry rebuilt", code, ready)
	}
	booked := count()
	code, replayed := call(1, "cart", "cart-claim-retry", a)
	if code != 200 || !reflect.DeepEqual(ready, replayed) || count() != booked || provider.builds.Load() != 1 {
		t.Fatal("cart replay", code, replayed)
	}
	var checkout adamHallCheckoutRecord
	if err := db.First(&checkout, ready["checkout_id"]).Error; err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(checkout.ContextCipher), "private-owner-checkout") || len(checkout.ContextCipher) == 0 {
		t.Fatal("private context encryption missing")
	}
	var audit string
	if err := db.Raw("SELECT COALESCE(jsonb_agg(new_values)::text,'') FROM audit_log WHERE entity_id=?", fmt.Sprint(order.ID)).Row().Scan(&audit); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(audit, "private-owner-checkout") || strings.Contains(audit, "context_cipher") {
		t.Fatal("private context leaked in audit")
	}
	in = map[string]any{"id": order.ID, "checkout_id": checkout.ID, "preview": true}
	before = count()
	p = review("send", in)
	if count() != before || provider.sends.Load() != 0 || p["proposed_order"].(map[string]any)["totalCents"] != float64(2470) {
		t.Fatal("paid full price preview", p)
	}
	a = final(in, p)
	must(db, `CREATE OR REPLACE FUNCTION reject_ah_audit() RETURNS TRIGGER AS $$ BEGIN IF NEW.action='order.adam_hall_submission_requested' THEN RAISE EXCEPTION 'forced paid claim audit';END IF;RETURN NEW;END $$ LANGUAGE plpgsql;CREATE TRIGGER reject_ah_audit BEFORE INSERT ON audit_log FOR EACH ROW EXECUTE FUNCTION reject_ah_audit()`)
	if code, _ := call(1, "send", "paid-claim-retry", a); code != 500 || provider.sends.Load() != 0 || count() != before {
		t.Fatal("paid/price claim rollback", code)
	}
	must(db, "DROP TRIGGER reject_ah_audit ON audit_log")
	must(db, `CREATE OR REPLACE FUNCTION reject_ah_audit() RETURNS TRIGGER AS $$ BEGIN IF NEW.action='order.ordered_at_adam_hall' THEN RAISE EXCEPTION 'forced paid final audit';END IF;RETURN NEW;END $$ LANGUAGE plpgsql;CREATE TRIGGER reject_ah_audit BEFORE INSERT ON audit_log FOR EACH ROW EXECUTE FUNCTION reject_ah_audit()`)
	if code, _ := call(1, "send", "paid-claim-retry", a); code != 500 || provider.sends.Load() != 1 {
		t.Fatal("paid final audit", code)
	}
	must(db, "DROP TRIGGER reject_ah_audit ON audit_log")
	code, sent := call(1, "send", "paid-claim-retry", a)
	if code != 200 || sent["operation_status"] != "sent" || provider.sends.Load() != 1 {
		t.Fatal("paid saved ack retry", code, sent)
	}
	po := sent["purchase_order"].(map[string]any)
	if sent["checkout_id"] != float64(checkout.ID) || po["supplierOrderNumber"] != "AH-FIXTURE-1" || po["totalCents"] != float64(2470) || po["lines"].([]any)[0].(map[string]any)["id"] != float64(order.Lines[0].ID) || po["lines"].([]any)[0].(map[string]any)["unitPriceCents"] != float64(1235) {
		t.Fatal("retained identity/confirmed supplier prices", po)
	}
	booked = count()
	_, replayed = call(1, "send", "paid-claim-retry", a)
	if !reflect.DeepEqual(sent, replayed) || count() != booked || provider.sends.Load() != 1 {
		t.Fatal("paid durable replay")
	}
	must(db, "UPDATE users SET is_admin=false WHERE userid=1")
	if code, _ := call(1, "send", "paid-claim-retry", a); code != 403 {
		t.Fatal("paid role revoked", code)
	}
	must(db, "UPDATE users SET is_admin=true WHERE userid=1")
	if err := db.Exec("UPDATE proc_purchase_orders SET status='draft' WHERE id=?", order.ID).Error; err == nil {
		t.Fatal("paid order reset")
	}
	if err := db.Exec("DELETE FROM proc_adam_hall_checkouts WHERE id=?", checkout.ID).Error; err == nil {
		t.Fatal("checkout retained")
	}
	if err := db.Exec("UPDATE proc_adam_hall_checkouts SET cart_snapshot='{}'::jsonb WHERE id=?", checkout.ID).Error; err == nil {
		t.Fatal("reviewed checkout altered")
	}
	// Lost supplier response persists uncertainty; no same-key or new-key resend.
	order = newOrder()
	in = map[string]any{"id": order.ID, "preview": true}
	p = review("cart", in)
	a = final(in, p)
	code, ready = call(1, "cart", "uncertain-cart", a)
	if code != 200 || ready["operation_status"] != "ready" {
		t.Fatal(code, ready)
	}
	in = map[string]any{"id": order.ID, "checkout_id": ready["checkout_id"], "preview": true}
	p = review("send", in)
	a = final(in, p)
	provider.fail.Store(true)
	code, uncertain := call(1, "send", "uncertain-paid", a)
	if code != 200 || uncertain["operation_status"] != "submission_unknown" || provider.sends.Load() != 2 {
		t.Fatal("uncertain paid order", code, uncertain)
	}
	provider.fail.Store(false)
	booked = count()
	_, replayed = call(1, "send", "uncertain-paid", a)
	if !reflect.DeepEqual(uncertain, replayed) || count() != booked || provider.sends.Load() != 2 {
		t.Fatal("uncertain resend")
	}
	if code, p := call(1, "send", "new-uncertain-key", a); code != 200 || p["ready_to_execute"] != false || provider.sends.Load() != 2 {
		t.Fatal("new-key resend", code, p)
	}
	// Unconfirmed legacy native APIs do not rebuild or place a cart.
	if code, _ := callHandler(handler, 1, "UI", "", "POST", fmt.Sprintf("/orders/%d/adam-hall/cart", order.ID), map[string]any{}); code != 200 {
		t.Fatal("native unconfirmed preview", code)
	}
	headerless := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { r.Header.Del("X-Cores-Origin"); handler.ServeHTTP(w, r) })
	if code, _ := callHandler(headerless, 1, "cores:procurement:send", "headerless-ah", "POST", fmt.Sprintf("/orders/%d/adam-hall/order", order.ID), map[string]any{}); code != 428 {
		t.Fatal("signed headerless native bypass", code)
	}
	if provider.sends.Load() != 2 || provider.builds.Load() != 2 {
		t.Fatal("legacy/unconfirmed action called supplier")
	}
	// Both native UI confirmations use the same owner phases and mandatory keys.
	order = newOrder()
	path := fmt.Sprintf("/orders/%d/adam-hall/", order.ID)
	code, p = callHandler(handler, 1, "UI", "", "GET", path+"review", nil)
	if code != 200 || p["ready_to_execute"] != true || provider.builds.Load() != 2 {
		t.Fatal("pure native review", code, p)
	}
	a = final(map[string]any{"id": order.ID}, p)
	if code, _ := callHandler(handler, 1, "UI", "", "POST", path+"cart", a); code != 428 || provider.builds.Load() != 2 {
		t.Fatal("native key required", code)
	}
	code, ready = callHandler(handler, 1, "UI", "native-cart", "POST", path+"cart", a)
	if code != 200 || ready["operation_status"] != "ready" || provider.builds.Load() != 3 {
		t.Fatal("native cart", code, ready)
	}
	code, p = callHandler(handler, 1, "UI", "", "GET", path+"send-review", nil)
	if code != 200 || p["ready_to_execute"] != true || provider.sends.Load() != 2 {
		t.Fatal("pure native paid review", code, p)
	}
	a = final(map[string]any{"id": order.ID, "checkout_id": ready["checkout_id"]}, p)
	code, sent = callHandler(handler, 1, "UI", "native-paid", "POST", path+"order", a)
	if code != 200 || sent["order"] == nil || sent["cart"] == nil || provider.sends.Load() != 3 {
		t.Fatal("native paid result", code, sent)
	}
	booked = count()
	code, replayed = callHandler(handler, 1, "UI", "native-paid", "POST", path+"order", a)
	if code != 200 || !reflect.DeepEqual(sent, replayed) || count() != booked || provider.sends.Load() != 3 {
		t.Fatal("native durable replay", code, replayed)
	}
	// Changed local catalog, reviewed identity and expired supplier quotes stop
	// before any paid request. Terminal checkout snapshots remain immutable.
	order = newOrder()
	in = map[string]any{"id": order.ID, "preview": true}
	a = final(in, review("cart", in))
	code, ready = call(1, "cart", "stale-catalog-cart", a)
	if code != 200 || ready["operation_status"] != "ready" {
		t.Fatal(code, ready)
	}
	in = map[string]any{"id": order.ID, "checkout_id": ready["checkout_id"], "preview": true}
	a = final(in, review("send", in))
	must(db, "UPDATE proc_products SET sku='CHANGED' WHERE id=?", product.ID)
	code, p = call(1, "send", "stale-catalog-paid", a)
	if code != 200 || p["ready_to_execute"] != false || provider.sends.Load() != 3 {
		t.Fatal("changed SKU sent", code, p)
	}
	must(db, "UPDATE proc_products SET sku='8747X6' WHERE id=?", product.ID)
	order = newOrder()
	in = map[string]any{"id": order.ID, "preview": true}
	a = final(in, review("cart", in))
	code, ready = call(1, "cart", "expired-quote-cart", a)
	if code != 200 || ready["operation_status"] != "ready" {
		t.Fatal(code, ready)
	}
	// Test fixture ages its retained record with the guard disabled only within
	// the owned schema; the public API never exposes timestamp editing.
	must(db, "ALTER TABLE proc_adam_hall_checkouts DISABLE TRIGGER proc_adam_hall_checkouts_guard_retention")
	must(db, "UPDATE proc_adam_hall_checkouts SET created_at=created_at-interval '16 minutes' WHERE id=?", ready["checkout_id"])
	must(db, "ALTER TABLE proc_adam_hall_checkouts ENABLE TRIGGER proc_adam_hall_checkouts_guard_retention")
	code, p = call(1, "send", "", map[string]any{"id": order.ID, "checkout_id": ready["checkout_id"], "preview": true})
	if code != 200 || p["ready_to_execute"] != false || provider.sends.Load() != 3 {
		t.Fatal("expired quote sent", code, p)
	}
	// A supplier acceptance whose outcome audit fails leaves a permanent pending
	// barrier. Retry cannot lose that barrier or send a second paid request.
	order = newOrder()
	in = map[string]any{"id": order.ID, "preview": true}
	a = final(in, review("cart", in))
	code, ready = call(1, "cart", "lost-outcome-cart", a)
	if code != 200 || ready["operation_status"] != "ready" {
		t.Fatal(code, ready)
	}
	in = map[string]any{"id": order.ID, "checkout_id": ready["checkout_id"], "preview": true}
	a = final(in, review("send", in))
	must(db, `CREATE OR REPLACE FUNCTION reject_ah_audit() RETURNS TRIGGER AS $$ BEGIN IF NEW.action='order.adam_hall_submission_outcome' THEN RAISE EXCEPTION 'forced outcome audit';END IF;RETURN NEW;END $$ LANGUAGE plpgsql;CREATE TRIGGER reject_ah_audit BEFORE INSERT ON audit_log FOR EACH ROW EXECUTE FUNCTION reject_ah_audit()`)
	if code, _ = call(1, "send", "lost-paid-outcome", a); code != 500 || provider.sends.Load() != 4 {
		t.Fatal("lost outcome", code)
	}
	must(db, "DROP TRIGGER reject_ah_audit ON audit_log")
	code, p = call(1, "send", "lost-paid-outcome", a)
	if code != 200 || p["operation_status"] != "pending" || provider.sends.Load() != 4 {
		t.Fatal("lost outcome resent", code, p)
	}
}

type ownerAdamHallFixture struct {
	builds atomic.Int64
	sends  atomic.Int64
	fail   atomic.Bool
}

func (f *ownerAdamHallFixture) Scrape(context.Context, string) (scraper.ProductPreview, error) {
	return scraper.ProductPreview{}, errors.New("not used by closed supplier workflow")
}
func (f *ownerAdamHallFixture) AdamHallConfigured() bool           { return true }
func (f *ownerAdamHallFixture) AdamHallAccountFingerprint() string { return strings.Repeat("f", 64) }
func (f *ownerAdamHallFixture) PrepareAdamHallReviewedCheckout(ctx context.Context, items []scraper.AdamHallItem) (scraper.AdamHallReviewedCheckout, error) {
	f.builds.Add(1)
	return scraper.AdamHallReviewedCheckout{ContextToken: "private-owner-checkout", Cart: scraper.AdamHallCart{Lines: []scraper.AdamHallCartLine{{ProductNumber: "8747X6", Description: "Reviewed supplier item", Quantity: 2, UnitPriceCents: 1235, TotalCents: 2470}}, TotalCents: 2470, Currency: "EUR", Customer: "Configured business account", ShippingAddress: "Fixture Company, Business delivery street, 12345 Fixture city", BillingAddress: "Fixture Company, Business billing street, 12345 Fixture city", ShippingMethod: "Standard", PaymentMethod: "Invoice", ReviewFingerprint: strings.Repeat("c", 64)}}, nil
}
func (f *ownerAdamHallFixture) SubmitReviewedAdamHallCheckout(ctx context.Context, token string, items []scraper.AdamHallItem, cart scraper.AdamHallCart, comment string) (scraper.AdamHallOrder, error) {
	n := f.sends.Add(1)
	if token != "private-owner-checkout" || cart.TotalCents != 2470 || len(items) != 1 || items[0].ProductNumber != "8747X6" || comment == "" {
		return scraper.AdamHallOrder{}, errors.New("reviewed private checkout mismatch")
	}
	if f.fail.Load() {
		return scraper.AdamHallOrder{}, errors.New("fixture uncertain provider response")
	}
	return scraper.AdamHallOrder{OrderNumber: fmt.Sprintf("AH-FIXTURE-%d", n), Cart: cart}, nil
}
