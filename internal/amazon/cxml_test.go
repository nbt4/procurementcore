package amazon

import (
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPunchoutRoundTrip(t *testing.T) {
	var requests []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read request: %v", err)
			return
		}
		requests = append(requests, string(body))
		w.Header().Set("Content-Type", "text/xml")
		if strings.Contains(string(body), "PunchOutSetupRequest") {
			fmt.Fprint(w, `<cXML><Response><Status code="200" text="OK"/><PunchOutSetupResponse><StartPage><URL>https://www.amazon.de/punchout/start</URL></StartPage></PunchOutSetupResponse></Response></cXML>`)
		} else {
			fmt.Fprint(w, `<cXML><Response><Status code="200" text="OK"/></Response></cXML>`)
		}
	}))
	defer server.Close()
	client, err := New(Config{FromIdentity: "buyer", SharedSecret: "private", TestURL: server.URL, LiveURL: server.URL, OrderURL: server.URL, ReturnURL: server.URL, Mode: "test", AllowHTTP: true, ShipTo: Address{Company: "Tsunami Events UG", Street: "Ringstraße 12", City: "Haiger", PostalCode: "35708", Country: "DE"}})
	if err != nil {
		t.Fatal(err)
	}
	url, err := client.Start(context.Background(), "noah.tielmann@tsunami-events.de", "cookie")
	if err != nil || !strings.HasPrefix(url, "https://www.amazon.de/") {
		t.Fatalf("start: %q %v", url, err)
	}
	if err := client.Submit(context.Background(), Order{Number: "PO-1", Lines: []OrderLine{{SupplierPartID: "B01", SupplierPartAuxiliaryID: "opaque-token", Description: "Adapter", Quantity: 2, Unit: "EA", UnitPriceCents: 1234}}}, "unique@procurementcore"); err != nil {
		t.Fatal(err)
	}
	if len(requests) != 2 {
		t.Fatalf("got %d requests", len(requests))
	}
	for _, value := range []string{"<SharedSecret>private</SharedSecret>", `<Extrinsic name="UserEmail">noah.tielmann@tsunami-events.de</Extrinsic>`, "<BuyerCookie>cookie</BuyerCookie>"} {
		if !strings.Contains(requests[0], value) {
			t.Errorf("setup missing %s", value)
		}
	}
	for _, value := range []string{`type="new"`, "<SupplierPartAuxiliaryID>opaque-token</SupplierPartAuxiliaryID>", "<Street>Ringstraße 12</Street>", `<Money currency="EUR">24.68</Money>`} {
		if !strings.Contains(requests[1], value) {
			t.Errorf("order missing %s", value)
		}
	}
	var parsed any
	if err := xml.Unmarshal([]byte(requests[1]), &parsed); err != nil {
		t.Fatal(err)
	}
}

func TestParseCartRequiresAuxiliaryID(t *testing.T) {
	data := []byte(`<cXML><Message><PunchOutOrderMessage><BuyerCookie>cookie</BuyerCookie><PunchOutOrderMessageHeader><Total><Money currency="EUR">12.34</Money></Total></PunchOutOrderMessageHeader><ItemIn quantity="1"><ItemID><SupplierPartID>B01</SupplierPartID><SupplierPartAuxiliaryID>opaque-token</SupplierPartAuxiliaryID></ItemID><ItemDetail><UnitPrice><Money currency="EUR">12.34</Money></UnitPrice><Description xml:lang="de">Adapter</Description><UnitOfMeasure>EA</UnitOfMeasure></ItemDetail></ItemIn></PunchOutOrderMessage></Message></cXML>`)
	cart, err := ParseCart(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(cart.Lines) != 1 || cart.Lines[0].UnitPriceCents != 1234 || cart.Lines[0].SupplierPartAuxiliaryID != "opaque-token" {
		t.Fatalf("unexpected cart: %+v", cart)
	}
	broken := strings.Replace(string(data), "<SupplierPartAuxiliaryID>opaque-token</SupplierPartAuxiliaryID>", "", 1)
	if _, err := ParseCart([]byte(broken)); err == nil {
		t.Fatal("missing auxiliary ID accepted")
	}
	withDescriptionID := strings.Replace(broken, "Adapter</Description>", "Adapter|asid-12345</Description>", 1)
	fromDescription, err := ParseCart([]byte(withDescriptionID))
	if err != nil || fromDescription.Lines[0].SupplierPartAuxiliaryID != "asid-12345" || fromDescription.Lines[0].Description != "Adapter" {
		t.Fatalf("SPAID in description: %+v %v", fromDescription, err)
	}
}
