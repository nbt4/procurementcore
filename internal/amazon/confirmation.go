package amazon

import (
	"encoding/xml"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
)

type Confirmation struct {
	PayloadID      string
	PurchaseOrder  string
	OrderPayloadID string
	AmazonOrderID  string
	Type           string
	Operation      string
	NoticeDate     *time.Time
	Lines          []ConfirmationLine
}

type ConfirmationLine struct {
	LineNumber    int
	AmazonOrderID string
	Accepted      float64
	Rejected      float64
	Delivery      *time.Time
}

type confirmationDocument struct {
	XMLName   xml.Name `xml:"cXML"`
	PayloadID string   `xml:"payloadID,attr"`
	Header    struct {
		From credential `xml:"From>Credential"`
		To   credential `xml:"To>Credential"`
	} `xml:"Header"`
	Request struct {
		Confirmation struct {
			Header struct {
				ConfirmID  string `xml:"confirmID,attr"`
				Type       string `xml:"type,attr"`
				Operation  string `xml:"operation,attr"`
				NoticeDate string `xml:"noticeDate,attr"`
			} `xml:"ConfirmationHeader"`
			Reference struct {
				OrderID  string `xml:"orderID,attr"`
				Document struct {
					PayloadID string `xml:"payloadID,attr"`
				} `xml:"DocumentReference"`
			} `xml:"OrderReference"`
			Items []struct {
				LineNumber string `xml:"lineNumber,attr"`
				Quantity   string `xml:"quantity,attr"`
				Statuses   []struct {
					Type         string `xml:"type,attr"`
					Quantity     string `xml:"quantity,attr"`
					DeliveryDate string `xml:"deliveryDate,attr"`
					Comments     []struct {
						Type string `xml:"type,attr"`
						Text string `xml:",chardata"`
					} `xml:"Comments"`
				} `xml:"ConfirmationStatus"`
			} `xml:"ConfirmationItem"`
		} `xml:"ConfirmationRequest"`
	} `xml:"Request"`
}

func parseConfirmationDate(raw string) (*time.Time, error) {
	if raw == "" {
		return nil, nil
	}
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02"} {
		if value, err := time.Parse(layout, raw); err == nil {
			return &value, nil
		}
	}
	return nil, errors.New("invalid date")
}

func confirmationQuantity(raw string) (float64, error) {
	value, err := strconv.ParseFloat(raw, 64)
	if err != nil || math.IsNaN(value) || math.IsInf(value, 0) || value <= 0 || value > 999999 {
		return 0, errors.New("invalid quantity")
	}
	return value, nil
}

// ParseConfirmation accepts Amazon's cXML Order Confirmation, including split orders.
func ParseConfirmation(data []byte, buyerIdentity string) (Confirmation, error) {
	var doc confirmationDocument
	if len(data) == 0 || len(data) > maxCXMLBytes || xml.Unmarshal(data, &doc) != nil || doc.XMLName.Local != "cXML" {
		return Confirmation{}, errors.New("invalid cXML confirmation")
	}
	if doc.Header.From.Identity != "Amazon" || doc.Header.To.Identity != buyerIdentity || doc.Header.From.Domain != "NetworkId" || doc.Header.To.Domain != "NetworkId" {
		return Confirmation{}, errors.New("unexpected cXML identities")
	}
	request := doc.Request.Confirmation
	if doc.PayloadID == "" || len(doc.PayloadID) > 255 || request.Reference.OrderID == "" || len(request.Reference.OrderID) > 40 || len(request.Header.ConfirmID) > 120 || len(request.Reference.Document.PayloadID) > 100 {
		return Confirmation{}, errors.New("missing or invalid confirmation reference")
	}
	if request.Header.Type != "accept" && request.Header.Type != "reject" && request.Header.Type != "detail" && request.Header.Type != "except" {
		return Confirmation{}, errors.New("unsupported confirmation type")
	}
	if request.Header.Operation != "new" && request.Header.Operation != "update" && request.Header.Operation != "delete" {
		return Confirmation{}, errors.New("unsupported confirmation operation")
	}
	notice, err := parseConfirmationDate(request.Header.NoticeDate)
	if err != nil {
		return Confirmation{}, err
	}
	result := Confirmation{PayloadID: doc.PayloadID, PurchaseOrder: request.Reference.OrderID, OrderPayloadID: request.Reference.Document.PayloadID, AmazonOrderID: strings.TrimSpace(request.Header.ConfirmID), Type: request.Header.Type, Operation: request.Header.Operation, NoticeDate: notice}
	if len(request.Items) > 500 {
		return Confirmation{}, errors.New("too many confirmation items")
	}
	for _, item := range request.Items {
		lineNumber, err := strconv.Atoi(item.LineNumber)
		if err != nil || lineNumber < 1 || lineNumber > 500 {
			return Confirmation{}, errors.New("invalid line number")
		}
		itemQuantity, err := confirmationQuantity(item.Quantity)
		if err != nil {
			return Confirmation{}, err
		}
		if len(item.Statuses) == 0 {
			return Confirmation{}, errors.New("missing line status")
		}
		var statusTotal float64
		for _, status := range item.Statuses {
			quantity, err := confirmationQuantity(status.Quantity)
			if err != nil {
				return Confirmation{}, err
			}
			statusTotal += quantity
			line := ConfirmationLine{LineNumber: lineNumber, AmazonOrderID: result.AmazonOrderID}
			for _, comment := range status.Comments {
				if comment.Type == "confirmID" && strings.TrimSpace(comment.Text) != "" {
					line.AmazonOrderID = strings.TrimSpace(comment.Text)
					break
				}
			}
			if len(line.AmazonOrderID) > 120 {
				return Confirmation{}, errors.New("invalid Amazon order number")
			}
			switch status.Type {
			case "accept", "detail", "allDetail":
				if line.AmazonOrderID == "" {
					return Confirmation{}, errors.New("missing Amazon order number")
				}
				line.Accepted = quantity
			case "reject":
				line.Rejected = quantity
			default:
				return Confirmation{}, fmt.Errorf("unsupported line status %q", status.Type)
			}
			line.Delivery, err = parseConfirmationDate(status.DeliveryDate)
			if err != nil {
				return Confirmation{}, err
			}
			result.Lines = append(result.Lines, line)
		}
		if statusTotal > itemQuantity+0.000001 {
			return Confirmation{}, errors.New("line status exceeds confirmation quantity")
		}
	}
	if len(result.Lines) == 0 && result.Type != "accept" && result.Type != "reject" {
		return Confirmation{}, errors.New("missing confirmation items")
	}
	if len(result.Lines) == 0 && result.Type == "accept" && result.AmazonOrderID == "" {
		return Confirmation{}, errors.New("missing Amazon order number")
	}
	return result, nil
}
