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

	"github.com/golang-jwt/jwt/v5"
	commonjwt "github.com/nbt4/cores-common/pkg/jwt"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func TestGoodsReceiptOwnerPhysicalAtomicContextAndReplay(t *testing.T) {
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
	const schema = "procurement_goods_receipt_test"
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
	must(db, `CREATE TABLE users(userid BIGINT PRIMARY KEY,username TEXT,is_active BOOLEAN,is_admin BOOLEAN);
 INSERT INTO users VALUES(1,'admin',true,true),(2,'member',true,false);
 CREATE TABLE audit_log(id BIGSERIAL PRIMARY KEY,user_id BIGINT,action TEXT,entity_type TEXT,entity_id TEXT,old_values JSONB,new_values JSONB,ip_address TEXT,user_agent TEXT);
 CREATE TABLE products(productid BIGINT PRIMARY KEY,name TEXT,product_code TEXT,lifecycle_status TEXT DEFAULT 'active',tracking_mode TEXT,stock_quantity NUMERIC(10,3) DEFAULT 0,updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP);
 CREATE SEQUENCE fixture_device_seq;
 CREATE TABLE devices(deviceid TEXT PRIMARY KEY DEFAULT ('DEVICE-'||nextval('fixture_device_seq')),productid BIGINT,serialnumber TEXT,status TEXT,condition_status TEXT,current_location TEXT,updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP);
 CREATE TABLE storage_zones(zone_id BIGINT PRIMARY KEY,code TEXT,name TEXT,is_active BOOLEAN,is_storable BOOLEAN,operational_status TEXT,updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP);
 INSERT INTO storage_zones VALUES(1,'ZONE','Zone',true,true,'available',CURRENT_TIMESTAMP);
 CREATE TABLE product_locations(location_id BIGSERIAL PRIMARY KEY,product_id BIGINT,zone_id BIGINT,quantity NUMERIC(10,3),updated_at TIMESTAMP);
 CREATE UNIQUE INDEX fixture_product_zone ON product_locations(product_id,zone_id) NULLS NOT DISTINCT;
 CREATE FUNCTION fixture_stock() RETURNS TRIGGER AS $$ BEGIN UPDATE products SET stock_quantity=(SELECT SUM(quantity) FROM product_locations WHERE product_id=NEW.product_id),updated_at=clock_timestamp() WHERE productid=NEW.product_id;RETURN NULL;END $$ LANGUAGE plpgsql;
 CREATE TRIGGER fixture_sync_stock AFTER INSERT OR UPDATE ON product_locations FOR EACH ROW EXECUTE FUNCTION fixture_stock();
 CREATE TABLE warehouse_tasks(task_id BIGSERIAL PRIMARY KEY,task_type TEXT,status TEXT,priority INT,to_zone_id BIGINT,product_id BIGINT,quantity NUMERIC(12,3),notes TEXT,is_archived BOOLEAN DEFAULT false,updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP);
 CREATE TABLE warehouse_task_events(event_id BIGSERIAL PRIMARY KEY,task_id BIGINT,event_type TEXT,to_status TEXT,actor_id BIGINT);`)
	supplier := models.Supplier{Name: "Receipt supplier", Code: "RCPT", Active: true}
	if err := db.Create(&supplier).Error; err != nil {
		t.Fatal(err)
	}
	t.Setenv("CORES_JWT_SECRET", "receipt-owner-test-real-user-secret-32-bytes")
	handler := auth.Middleware(commonjwt.DatabaseUserLookup(sqlDB), (&Handler{db: db}).Routes())
	call := func(uid uint, scope, key, path string, input any) (int, map[string]any, string) {
		raw, err := json.Marshal(input)
		if err != nil {
			t.Fatal(err)
		}
		signed, err := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{"uid": uid, "username": "admin", "is_admin": true, "mcp_scope": scope, "exp": time.Now().Add(time.Minute).Unix()}).SignedString(commonjwt.JWTSecret())
		if err != nil {
			t.Fatal(err)
		}
		r := httptest.NewRequest("POST", path, bytes.NewReader(raw))
		r.AddCookie(&http.Cookie{Name: "cores_token", Value: signed})
		r.Header.Set("X-Cores-Origin", "MCP/AI")
		r.Header.Set("Idempotency-Key", key)
		w := httptest.NewRecorder()
		if strings.HasPrefix(path, "/legacy-fixture/") {
			// Seed exactly the previously published owner transaction, then verify
			// upgrade replay through the new public handlers.
			var orderID uint
			fmt.Sscanf(path, "/legacy-fixture/%d", &orderID)
			seed := auth.Middleware(commonjwt.DatabaseUserLookup(sqlDB), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var native goodsReceiptInput
				if !decode(w, r, &native) {
					return
				}
				receipt := models.Receipt{PurchaseOrderID: orderID, PurchaseOrderLineID: native.LineID, Quantity: native.Quantity, ReceivedBy: auth.CurrentUser(r).ID, ReceivedAt: time.Now(), Note: native.Note}
				err := db.Transaction(func(tx *gorm.DB) error {
					record, _, err := beginIdempotentMutation(tx, r, "purchase_order_receipt", map[string]any{"id": orderID, "input": native})
					if err != nil {
						return err
					}
					if err := (&Handler{db: db}).applyGoodsReceipt(tx, r, orderID, native, &receipt); err != nil {
						return err
					}
					return completeIdempotentMutation(tx, record, 201, receipt)
				})
				if err != nil {
					writeProcurementFlowError(w, err)
					return
				}
				writeJSON(w, 201, receipt)
			}))
			seed.ServeHTTP(w, r)
		} else {
			handler.ServeHTTP(w, r)
		}
		out := map[string]any{}
		_ = json.Unmarshal(w.Body.Bytes(), &out)
		return w.Code, out, w.Body.String()
	}
	receive := func(key string, input goodsReceiptMCPRequest) (int, map[string]any, string) {
		return call(1, "cores:procurement:receive", key, "/mcp/orders/receive", input)
	}
	sequence := int64(0)
	create := func(mode string, amount float64) goodsReceiptMCPRequest {
		t.Helper()
		sequence++
		order := models.PurchaseOrder{Number: fmt.Sprintf("RECEIPT-%d", sequence), SupplierID: supplier.ID, Status: "sent", Currency: "EUR", TotalCents: 7777}
		if err := db.Create(&order).Error; err != nil {
			t.Fatal(err)
		}
		line := models.PurchaseOrderLine{PurchaseOrderID: order.ID, Description: "Line", Quantity: amount, Unit: "m", UnitPriceCents: 777}
		if mode != "" {
			p := models.Product{SKU: fmt.Sprintf("RECEIPT-%d", sequence), Name: "Product", Unit: "m", Active: true}
			if err := db.Create(&p).Error; err != nil {
				t.Fatal(err)
			}
			line.ProductID = &p.ID
			must(db, "INSERT INTO products(productid,name,product_code,tracking_mode) VALUES(?,?,?,?)", sequence, "Warehouse product", fmt.Sprint(sequence), mode)
			must(db, "INSERT INTO core_product_links(procurement_product_id,warehouse_product_id) VALUES(?,?)", p.ID, sequence)
		}
		if err := db.Create(&line).Error; err != nil {
			t.Fatal(err)
		}
		return goodsReceiptMCPRequest{OrderID: int64(order.ID), LineID: int64(line.ID), Quantity: 1}
	}
	review := func(input goodsReceiptMCPRequest) (goodsReceiptMCPRequest, map[string]any) {
		t.Helper()
		input.Preview = true
		input.ConfirmReceipt = false
		input.ExpectedContext = ""
		input.ExpectedUpdatedAt = ""
		status, p, raw := receive("", input)
		if status != 200 {
			t.Fatalf("preview %d %s", status, raw)
		}
		input.Preview = false
		input.ConfirmReceipt = true
		input.ExpectedContext = p["expected_context"].(string)
		input.ExpectedUpdatedAt = p["expected_updated_at"].(string)
		input.ConfirmationText = p["required_confirmation_text"].(string)
		return input, p
	}
	counts := func() string {
		t.Helper()
		var result string
		if err := db.Raw(`SELECT jsonb_build_array((SELECT count(*) FROM proc_receipts),(SELECT count(*) FROM proc_idempotency_records),(SELECT count(*) FROM audit_log),(SELECT count(*) FROM proc_activities),(SELECT count(*) FROM devices),(SELECT count(*) FROM warehouse_tasks),(SELECT count(*) FROM warehouse_task_events),(SELECT COALESCE(sum(quantity),0) FROM product_locations),(SELECT COALESCE(sum(received_quantity),0) FROM proc_purchase_order_lines))::text`).Row().Scan(&result); err != nil {
			t.Fatal(err)
		}
		return result
	}
	for _, mode := range []string{"", "quantity", "individual", "none"} {
		t.Run("mode_"+mode, func(t *testing.T) {
			input := create(mode, 3)
			input.Note = "received physical goods"
			if mode == "individual" {
				input.SerialNumbers = []string{fmt.Sprintf(" SERIAL-%d ", sequence)}
			}
			if mode != "" {
				zone := int64(1)
				input.TargetZoneID = &zone
			}
			before := counts()
			input, p := review(input)
			if p["ready_to_execute"] != true || counts() != before {
				t.Fatal("preview changed data", p)
			}
			dry := input
			dry.Preview = true
			status, _, raw := receive("dry-receipt-request", dry)
			if status != 200 || counts() != before {
				t.Fatal("dry run", status, raw)
			}
			status, out, raw := receive("physical-receipt-"+mode, input)
			if status != 200 || out["operation_status"] != "received" {
				t.Fatal(status, raw)
			}
			after := counts()
			status, out, raw = receive("physical-receipt-"+mode, input)
			if status != 200 || out["operation_status"] != "received" || counts() != after {
				t.Fatal("duplicate receipt", status, raw)
			}
			var order models.PurchaseOrder
			db.First(&order, input.OrderID)
			if order.Status != "partially_received" || order.TotalCents != 7777 {
				t.Fatal("partial status or money changed", order)
			}
			if mode != "" {
				var n int64
				db.Table("audit_log").Where("entity_type='product' AND old_values IS NOT NULL AND new_values->>'updated_at' IS NOT NULL").Count(&n)
				if n == 0 {
					t.Fatal("missing versioned product before/after audit")
				}
			}
			must(db, "UPDATE users SET is_admin=false WHERE userid=1")
			status, _, _ = receive("physical-receipt-"+mode, input)
			if status != 403 || counts() != after {
				t.Fatal("revoked cached replay", status)
			}
			must(db, "UPDATE users SET is_admin=true WHERE userid=1")
		})
	}
	t.Run("stale_context_and_confirmation", func(t *testing.T) {
		input := create("quantity", 4)
		input, p := review(input)
		if p["ready_to_execute"] != true {
			t.Fatal(p)
		}
		before := counts()
		wrong := input
		wrong.Quantity = 2
		status, out, raw := receive("changed-quantity", wrong)
		if status != 200 || out["operation_status"] != "needs_input" || counts() != before {
			t.Fatal("unbound quantity", status, raw)
		}
		must(db, "UPDATE proc_purchase_order_lines SET description='changed' WHERE id=?", input.LineID)
		status, out, raw = receive("changed-line-context", input)
		if status != 200 || out["operation_status"] != "needs_input" || counts() != before {
			t.Fatal("unbound line", status, raw)
		}
		input, _ = review(input)
		input.ConfirmationText = "RECEIVE ORDER"
		status, _, _ = receive("wrong-receipt-phrase", input)
		if status != 428 || counts() != before {
			t.Fatal("weak confirmation", status)
		}
	})
	t.Run("validation", func(t *testing.T) {
		for _, test := range []struct {
			name, mode string
			qty        float64
			serials    []string
			allow      bool
			required   string
		}{
			{"overdelivery", "quantity", 5, nil, false, "allow_overdelivery"}, {"precision", "quantity", 1.0001, nil, false, "warehouse_quantity_precision"},
			{"whole_devices", "individual", 1.5, []string{"S1"}, false, "exact_bounded_device_serials"}, {"duplicate_serial", "individual", 2, []string{"A", "a"}, false, "unique_valid_serial_numbers"},
			{"extra_serial", "none", 1, []string{"EXTRA"}, false, "serials_only_for_individual_tracking"}, {"capacity", "quantity", 10000000, nil, true, "warehouse_stock_capacity"},
		} {
			input := create(test.mode, 3)
			input.Quantity = test.qty
			input.SerialNumbers = test.serials
			input.AllowOverdelivery = test.allow
			_, p := review(input)
			raw, _ := json.Marshal(p)
			if p["ready_to_execute"] != false || !strings.Contains(string(raw), test.required) {
				t.Fatal(test.name, string(raw))
			}
		}
		input := create("quantity", 3)
		input.Quantity = 4
		input.AllowOverdelivery = true
		input, p := review(input)
		if p["ready_to_execute"] != true || !strings.Contains(input.ConfirmationText, "OVERDELIVERY") {
			t.Fatal(p)
		}
		status, out, raw := receive("explicit-overdelivery", input)
		if status != 200 || out["operation_status"] != "received" {
			t.Fatal(status, raw)
		}
		input = create("individual", 3)
		input.SerialNumbers = []string{" OLD-SERIAL "}
		must(db, "INSERT INTO devices(productid,serialnumber) VALUES(?,?)", sequence, " old-serial ")
		_, p = review(input)
		rawBytes, _ := json.Marshal(p)
		if !strings.Contains(string(rawBytes), "unused_serial_numbers") {
			t.Fatal(p)
		}
		input = create("quantity", 3)
		z := int64(1)
		input.TargetZoneID = &z
		input, _ = review(input)
		must(db, "UPDATE storage_zones SET operational_status='blocked' WHERE zone_id=1")
		status, out, raw = receive("stale-target-zone", input)
		if status != 200 || out["operation_status"] != "needs_input" {
			t.Fatal(status, raw)
		}
		must(db, "UPDATE storage_zones SET operational_status='available' WHERE zone_id=1")
	})
	t.Run("amazon_partially_confirmed_ceiling", func(t *testing.T) {
		input := create("quantity", 5)
		session := models.AmazonPunchoutSession{TokenHash: "receipt-amazon-fixture", UserID: 1, Status: "ordered", ExpiresAt: time.Now().Add(time.Hour)}
		if err := db.Create(&session).Error; err != nil {
			t.Fatal(err)
		}
		must(db, "UPDATE proc_purchase_orders SET amazon_punchout_session_id=?,status='partially_confirmed' WHERE id=?", session.ID, input.OrderID)
		confirmation := models.AmazonLineConfirmation{PurchaseOrderID: uint(input.OrderID), PurchaseOrderLineID: uint(input.LineID), AmazonOrderNumber: "AMAZON-RECEIPT", AcceptedQuantity: 2, RejectedQuantity: 3}
		if err := db.Create(&confirmation).Error; err != nil {
			t.Fatal(err)
		}
		input.Quantity = 3
		input.AllowOverdelivery = true
		_, p := review(input)
		raw, _ := json.Marshal(p)
		if p["ready_to_execute"] != false || !strings.Contains(string(raw), "amazon_confirmed_quantity") {
			t.Fatal(p)
		}
		input.Quantity = 1
		input, p = review(input)
		if p["ready_to_execute"] != true {
			t.Fatal(p)
		}
		status, out, rawText := receive("amazon-partial-receipt", input)
		if status != 200 || out["operation_status"] != "received" {
			t.Fatal(status, rawText)
		}
	})
	t.Run("atomic_final_audit_and_same_key_retry", func(t *testing.T) {
		input := create("individual", 3)
		input.SerialNumbers = []string{"ROLLBACK-SERIAL"}
		input, _ = review(input)
		before := counts()
		for index, entity := range []string{"procurement_order", "warehouse_task"} {
			must(db, fmt.Sprintf(`CREATE OR REPLACE FUNCTION fail_receipt_audit() RETURNS TRIGGER AS $$ BEGIN IF NEW.entity_type='%s' THEN RAISE EXCEPTION 'forced receipt audit rollback';END IF;RETURN NEW;END $$ LANGUAGE plpgsql`, entity))
			if index == 0 {
				must(db, `CREATE TRIGGER fail_receipt_audit BEFORE INSERT ON audit_log FOR EACH ROW EXECUTE FUNCTION fail_receipt_audit()`)
			}
			status, _, raw := receive("receipt-final-audit-retry", input)
			if status != 500 || counts() != before {
				t.Fatal("partial physical commit", entity, status, raw, counts(), before)
			}
		}
		must(db, "DROP TRIGGER fail_receipt_audit ON audit_log")
		status, out, raw := receive("receipt-final-audit-retry", input)
		if status != 200 || out["operation_status"] != "received" {
			t.Fatal("same-key retry", status, raw)
		}
	})
	t.Run("legacy_receipt_upgrade", func(t *testing.T) {
		input := create("quantity", 3)
		input, _ = review(input)
		native, err := receiptNativeInput(input)
		if err != nil {
			t.Fatal(err)
		}
		status, _, raw := call(1, "cores:procurement:receive", "legacy-receipt-upgrade", fmt.Sprintf("/legacy-fixture/%d", input.OrderID), native)
		if status != 201 {
			t.Fatal(status, raw)
		}
		before := counts()
		status, _, raw = call(1, "cores:procurement:receive", "legacy-receipt-upgrade", fmt.Sprintf("/orders/%d/receipt", input.OrderID), native)
		if status != 201 || counts() != before {
			t.Fatal("legacy public replay", status, raw)
		}
		status, _, _ = call(1, "cores:procurement:receive", "legacy-new-unbound-request", fmt.Sprintf("/orders/%d/receipt", input.OrderID), native)
		if status != 428 || counts() != before {
			t.Fatal("legacy bypassed context", status)
		}
		input.ExpectedContext = ""
		input.ConfirmationText = "RECEIVE ORDER LEGACY"
		status, out, raw := receive("legacy-receipt-upgrade", input)
		if status != 200 || out["replayed_from_legacy"] != true || counts() != before {
			t.Fatal("legacy receipt booked twice", status, raw)
		}
		input.Quantity = 2
		status, _, _ = receive("legacy-receipt-upgrade", input)
		if status != 409 || counts() != before {
			t.Fatal("legacy changed payload", status)
		}
	})
	t.Run("concurrent_exact_preview", func(t *testing.T) {
		input := create("quantity", 3)
		input, _ = review(input)
		var wg sync.WaitGroup
		results := make(chan string, 2)
		for _, key := range []string{"receipt-concurrent-key-one", "receipt-concurrent-key-two"} {
			wg.Add(1)
			go func(k string) {
				defer wg.Done()
				status, out, raw := receive(k, input)
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
		for value := range results {
			seen[value]++
		}
		if seen["received"] != 1 || seen["needs_input"] != 1 {
			t.Fatal(seen)
		}
	})
	t.Run("signed_exact_scope_and_current_role", func(t *testing.T) {
		input := create("quantity", 3)
		input.Preview = true
		for _, scope := range []string{"cores:write", "cores:procurement:approve", "cores:procurement:update", ""} {
			status, _, _ := call(1, scope, "", "/mcp/orders/receive", input)
			if status != 403 {
				t.Fatal("wrong delegation accepted", scope, status)
			}
		}
		status, _, _ := call(2, "cores:procurement:receive", "", "/mcp/orders/receive", input)
		if status != 403 {
			t.Fatal("member accepted", status)
		}
	})
}
