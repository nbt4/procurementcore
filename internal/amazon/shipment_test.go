package amazon

import "testing"

func TestParseShipment(t *testing.T) {
	data := []byte(`<cXML payloadID="shipment-event"><Header><To><Credential domain="NetworkId"><Identity>buyer</Identity></Credential></To></Header><Request><ShipNoticeRequest><ShipNoticeHeader shipmentID="shipment-1" shipmentDate="2026-10-01T08:00:00Z" deliveryDate="2026-10-03T12:00:00Z"><ShipControl><CarrierIdentifier domain="companyName">DHL</CarrierIdentifier><ShipmentIdentifier>TRACK-1</ShipmentIdentifier></ShipControl></ShipNoticeHeader><ShipNoticePortion><OrderReference orderID="PO-1"><DocumentReference payloadID="outgoing-1"/></OrderReference></ShipNoticePortion></ShipNoticeRequest></Request></cXML>`)
	s, err := ParseShipment(data, "buyer")
	if err != nil {
		t.Fatal(err)
	}
	if s.ShipmentID != "shipment-1" || s.Carrier != "DHL" || s.TrackingNumber != "TRACK-1" || s.DeliveryDate == nil || len(s.Orders) != 1 || s.Orders[0].Number != "PO-1" {
		t.Fatalf("unexpected shipment: %+v", s)
	}
	if _, err := ParseShipment(data, "other"); err == nil {
		t.Fatal("wrong buyer accepted")
	}
}
