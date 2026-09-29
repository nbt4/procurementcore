package amazon

import (
	"encoding/xml"
	"errors"
	"strings"
	"time"
)

type Shipment struct {
	PayloadID      string
	ShipmentID     string
	TrackingNumber string
	Carrier        string
	ShipmentDate   *time.Time
	DeliveryDate   *time.Time
	Orders         []ShipmentOrder
}

type ShipmentOrder struct {
	Number    string
	PayloadID string
}

type shipmentDocument struct {
	XMLName   xml.Name `xml:"cXML"`
	PayloadID string   `xml:"payloadID,attr"`
	Header    struct {
		To credential `xml:"To>Credential"`
	} `xml:"Header"`
	Request struct {
		Notice struct {
			Header struct {
				ShipmentID   string `xml:"shipmentID,attr"`
				ShipmentDate string `xml:"shipmentDate,attr"`
				DeliveryDate string `xml:"deliveryDate,attr"`
				ShipControl  struct {
					Carrier struct {
						Domain string `xml:"domain,attr"`
						Value  string `xml:",chardata"`
					} `xml:"CarrierIdentifier"`
					Tracking string `xml:"ShipmentIdentifier"`
				} `xml:"ShipControl"`
			} `xml:"ShipNoticeHeader"`
			Portions []struct {
				Reference struct {
					OrderID  string `xml:"orderID,attr"`
					Document struct {
						PayloadID string `xml:"payloadID,attr"`
					} `xml:"DocumentReference"`
				} `xml:"OrderReference"`
			} `xml:"ShipNoticePortion"`
		} `xml:"ShipNoticeRequest"`
	} `xml:"Request"`
}

func ParseShipment(data []byte, buyerIdentity string) (Shipment, error) {
	var doc shipmentDocument
	if len(data) == 0 || len(data) > maxCXMLBytes || xml.Unmarshal(data, &doc) != nil || doc.XMLName.Local != "cXML" {
		return Shipment{}, errors.New("invalid cXML shipment")
	}
	if doc.Header.To.Domain != "NetworkId" || doc.Header.To.Identity != buyerIdentity {
		return Shipment{}, errors.New("unexpected cXML recipient")
	}
	if doc.PayloadID == "" || len(doc.PayloadID) > 255 || len(doc.Request.Notice.Portions) == 0 || len(doc.Request.Notice.Portions) > 100 {
		return Shipment{}, errors.New("invalid shipment reference")
	}
	header := doc.Request.Notice.Header
	if header.ShipmentID == "" || len(header.ShipmentID) > 120 || len(header.ShipControl.Tracking) > 255 || len(header.ShipControl.Carrier.Value) > 120 {
		return Shipment{}, errors.New("invalid shipment ID")
	}
	shipmentDate, err := parseConfirmationDate(header.ShipmentDate)
	if err != nil {
		return Shipment{}, err
	}
	deliveryDate, err := parseConfirmationDate(header.DeliveryDate)
	if err != nil {
		return Shipment{}, err
	}
	result := Shipment{PayloadID: doc.PayloadID, ShipmentID: header.ShipmentID, TrackingNumber: strings.TrimSpace(header.ShipControl.Tracking), Carrier: strings.TrimSpace(header.ShipControl.Carrier.Value), ShipmentDate: shipmentDate, DeliveryDate: deliveryDate}
	seen := map[string]bool{}
	for _, portion := range doc.Request.Notice.Portions {
		ref := portion.Reference
		if ref.OrderID == "" || len(ref.OrderID) > 40 || len(ref.Document.PayloadID) > 100 {
			return Shipment{}, errors.New("invalid purchase order reference")
		}
		if !seen[ref.OrderID] {
			result.Orders = append(result.Orders, ShipmentOrder{Number: ref.OrderID, PayloadID: ref.Document.PayloadID})
			seen[ref.OrderID] = true
		}
	}
	return result, nil
}
