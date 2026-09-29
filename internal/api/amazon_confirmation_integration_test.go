package api

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"

	"procurementcore/internal/amazon"
	"procurementcore/internal/models"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func TestAmazonConfirmationReconcilesSplitOrdersAndCancellation(t *testing.T) {
	dsn := os.Getenv("PROCUREMENT_TEST_DATABASE_URL")
	parsed, err := url.Parse(dsn)
	if dsn == "" || err != nil || !strings.HasSuffix(strings.TrimPrefix(parsed.Path, "/"), "_test") {
		t.Skip("requires disposable PostgreSQL _test database")
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
	const schema = "amazon_confirmation_test"
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
	if err := db.AutoMigrate(&models.Supplier{}, &models.AmazonPunchoutSession{}, &models.PurchaseOrder{}, &models.PurchaseOrderLine{}, &models.AmazonLineConfirmation{}, &models.AmazonConfirmationEvent{}, &models.AmazonShipment{}, &models.Activity{}); err != nil {
		t.Fatal(err)
	}
	client, err := amazon.New(amazon.Config{FromIdentity: "buyer", SharedSecret: "secret", TestURL: "http://localhost/test", LiveURL: "http://localhost/live", ReturnURL: "http://localhost/return", AllowHTTP: true})
	if err != nil {
		t.Fatal(err)
	}
	h := &Handler{db: db, amazon: client}
	supplier := models.Supplier{Name: "Amazon", Code: "AMAZON", Active: true}
	if err := db.Create(&supplier).Error; err != nil {
		t.Fatal(err)
	}
	session := models.AmazonPunchoutSession{TokenHash: strings.Repeat("a", 64), Status: "returned"}
	if err := db.Create(&session).Error; err != nil {
		t.Fatal(err)
	}
	order := models.PurchaseOrder{Number: "PO-TEST-1", AmazonPayloadID: "outgoing-1", AmazonPunchoutSessionID: &session.ID, SupplierID: supplier.ID, Status: "sent", Lines: []models.PurchaseOrderLine{{Description: "A", Quantity: 2}, {Description: "B", Quantity: 1}}}
	if err := db.Create(&order).Error; err != nil {
		t.Fatal(err)
	}
	call := func(id, orderID, headerType, items string, basic bool) *httptest.ResponseRecorder {
		body := fmt.Sprintf(`<cXML payloadID="%s"><Header><From><Credential domain="NetworkId"><Identity>Amazon</Identity></Credential></From><To><Credential domain="NetworkId"><Identity>buyer</Identity></Credential></To></Header><Request><ConfirmationRequest><ConfirmationHeader confirmID="%s" operation="new" type="%s" noticeDate="2026-09-29T12:00:00Z"/><OrderReference orderID="%s"><DocumentReference payloadID="outgoing-1"/></OrderReference>%s</ConfirmationRequest></Request></cXML>`, id, orderID, headerType, order.Number, items)
		r := httptest.NewRequest(http.MethodPost, "/api/v1/amazon/confirmation", strings.NewReader(body))
		if basic {
			r.SetBasicAuth("buyer", "secret")
		}
		w := httptest.NewRecorder()
		h.HandleAmazonConfirmation(w, r)
		return w
	}
	accepted := `<ConfirmationItem lineNumber="1" quantity="2"><ConfirmationStatus type="accept" quantity="2" deliveryDate="2026-10-03T12:00:00Z"/></ConfirmationItem>`
	if w := call("event-1", "303-1111111-1111111", "detail", accepted, false); w.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated: %d", w.Code)
	}
	if w := call("event-1", "303-1111111-1111111", "detail", accepted, true); w.Code != http.StatusOK {
		t.Fatalf("first confirmation: %d %s", w.Code, w.Body.String())
	}
	if w := call("event-1", "303-1111111-1111111", "detail", accepted, true); w.Code != http.StatusOK {
		t.Fatalf("replay: %d", w.Code)
	}
	if err := db.First(&order, order.ID).Error; err != nil {
		t.Fatal(err)
	}
	if order.Status != "sent" || order.SupplierOrderNumber != "303-1111111-1111111" || order.ExpectedDelivery == nil {
		t.Fatalf("first state: %+v", order)
	}
	rejected := `<ConfirmationItem lineNumber="2" quantity="1"><ConfirmationStatus type="reject" quantity="1"/></ConfirmationItem>`
	if w := call("event-2", "303-2222222-2222222", "detail", rejected, true); w.Code != http.StatusOK {
		t.Fatalf("partial rejection: %d %s", w.Code, w.Body.String())
	}
	if err := db.First(&order, order.ID).Error; err != nil {
		t.Fatal(err)
	}
	if order.Status != "partially_confirmed" {
		t.Fatalf("partial state: %s", order.Status)
	}
	if w := call("event-3", "303-1111111-1111111", "reject", "", true); w.Code != http.StatusOK {
		t.Fatalf("full cancellation: %d %s", w.Code, w.Body.String())
	}
	if err := db.First(&order, order.ID).Error; err != nil {
		t.Fatal(err)
	}
	if order.Status != "cancelled" || order.SupplierOrderNumber != "303-1111111-1111111" {
		t.Fatalf("cancelled state: %+v", order)
	}
	var eventCount int64
	if err := db.Model(&models.AmazonConfirmationEvent{}).Count(&eventCount).Error; err != nil || eventCount != 3 {
		t.Fatalf("events: %d %v", eventCount, err)
	}
	if w := call("event-4", "303-1111111-1111111", "accept", "", true); w.Code != http.StatusConflict {
		t.Fatalf("cancellation revived: %d", w.Code)
	}
	session2 := models.AmazonPunchoutSession{TokenHash: strings.Repeat("b", 64), Status: "returned"}
	if err := db.Create(&session2).Error; err != nil {
		t.Fatal(err)
	}
	order2 := models.PurchaseOrder{Number: "PO-TEST-2", AmazonPayloadID: "outgoing-1", AmazonPunchoutSessionID: &session2.ID, SupplierID: supplier.ID, Status: "sent", Lines: []models.PurchaseOrderLine{{Description: "C", Quantity: 1}, {Description: "D", Quantity: 1}}}
	if err := db.Create(&order2).Error; err != nil {
		t.Fatal(err)
	}
	order = order2
	if w := call("event-5", "303-1111111-1111111", "detail", `<ConfirmationItem lineNumber="1" quantity="1"><ConfirmationStatus type="accept" quantity="1"/></ConfirmationItem>`, true); w.Code != http.StatusOK {
		t.Fatalf("split first: %d %s", w.Code, w.Body.String())
	}
	if w := call("event-6", "303-2222222-2222222", "detail", `<ConfirmationItem lineNumber="2" quantity="1"><ConfirmationStatus type="accept" quantity="1"/></ConfirmationItem>`, true); w.Code != http.StatusOK {
		t.Fatalf("split second: %d %s", w.Code, w.Body.String())
	}
	if err := db.First(&order2, order2.ID).Error; err != nil {
		t.Fatal(err)
	}
	if order2.Status != "confirmed" || order2.AmazonOrderNumbers != "303-1111111-1111111, 303-2222222-2222222" {
		t.Fatalf("split state: %+v", order2)
	}
	shipmentBody := `<cXML payloadID="shipment-event"><Header><To><Credential domain="NetworkId"><Identity>buyer</Identity></Credential></To></Header><Request><ShipNoticeRequest><ShipNoticeHeader shipmentID="shipment-1" deliveryDate="2026-10-05T12:00:00Z"><ShipControl><CarrierIdentifier domain="companyName">DHL</CarrierIdentifier><ShipmentIdentifier>TRACK-1</ShipmentIdentifier></ShipControl></ShipNoticeHeader><ShipNoticePortion><OrderReference orderID="PO-TEST-2"><DocumentReference payloadID="outgoing-1"/></OrderReference></ShipNoticePortion></ShipNoticeRequest></Request></cXML>`
	shipmentRequest := httptest.NewRequest(http.MethodPost, "/api/v1/amazon/shipment", strings.NewReader(shipmentBody))
	shipmentRequest.SetBasicAuth("buyer", "secret")
	shipmentResponse := httptest.NewRecorder()
	h.HandleAmazonShipment(shipmentResponse, shipmentRequest)
	if shipmentResponse.Code != http.StatusOK {
		t.Fatalf("shipment: %d %s", shipmentResponse.Code, shipmentResponse.Body.String())
	}
	if err := db.First(&order2, order2.ID).Error; err != nil {
		t.Fatal(err)
	}
	if order2.ExpectedDelivery == nil || order2.ExpectedDelivery.Format("2006-01-02") != "2026-10-05" {
		t.Fatalf("delivery from shipment: %+v", order2.ExpectedDelivery)
	}
}
