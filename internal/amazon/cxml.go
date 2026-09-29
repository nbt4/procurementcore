package amazon

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const maxCXMLBytes = 1 << 20

type Config struct {
	FromIdentity string
	SharedSecret string
	TestURL      string
	LiveURL      string
	OrderURL     string
	ReturnURL    string
	Mode         string
	ShipTo       Address
	BillTo       Address
	AllowHTTP    bool // tests only
}

type Address struct {
	Company    string `json:"company"`
	Recipient  string `json:"recipient"`
	Street     string `json:"street"`
	City       string `json:"city"`
	Region     string `json:"region"`
	PostalCode string `json:"postalCode"`
	Country    string `json:"country"`
	Email      string `json:"email"`
}

func (a Address) Complete() bool {
	return strings.TrimSpace(a.Company) != "" && strings.TrimSpace(a.Street) != "" && strings.TrimSpace(a.City) != "" && strings.TrimSpace(a.PostalCode) != "" && len(strings.TrimSpace(a.Country)) == 2
}

type Client struct {
	cfg  Config
	http *http.Client
}

func New(cfg Config) (*Client, error) {
	if cfg.FromIdentity == "" || cfg.SharedSecret == "" || cfg.ReturnURL == "" {
		return nil, errors.New("Amazon PunchOut benötigt From Identity, Shared Secret und öffentliche Rückgabe-URL")
	}
	if cfg.Mode == "" {
		cfg.Mode = "test"
	}
	if cfg.Mode != "test" && cfg.Mode != "production" {
		return nil, errors.New("Amazon PunchOut-Modus muss test oder production sein")
	}
	for _, endpoint := range []string{cfg.TestURL, cfg.LiveURL, cfg.OrderURL, cfg.ReturnURL} {
		if endpoint == "" {
			continue
		}
		parsed, err := url.Parse(endpoint)
		if err != nil || parsed.Hostname() == "" || (parsed.Scheme != "https" && !(cfg.AllowHTTP && parsed.Scheme == "http")) {
			return nil, errors.New("Amazon PunchOut-URL muss eine gültige HTTPS-URL sein")
		}
	}
	if cfg.TestURL == "" || cfg.LiveURL == "" {
		return nil, errors.New("Amazon PunchOut-Test- und Live-URL fehlen")
	}
	if !cfg.AllowHTTP {
		for _, endpoint := range []string{cfg.TestURL, cfg.LiveURL} {
			if host := mustHost(endpoint); host != "abintegrations.amazon.de" {
				return nil, errors.New("Amazon PunchOut-URL muss zu amazon.de gehören")
			}
		}
		if cfg.OrderURL != "" && mustHost(cfg.OrderURL) != "https-eu-ats.amazonsedi.com" {
			return nil, errors.New("Amazon Bestell-URL hat einen unerwarteten Host")
		}
	}
	if cfg.BillTo.Company == "" {
		cfg.BillTo = cfg.ShipTo
	}
	return &Client{cfg: cfg, http: &http.Client{Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}

func mustHost(raw string) string {
	parsed, _ := url.Parse(raw)
	return strings.ToLower(parsed.Hostname())
}
func (c *Client) Mode() string { return c.cfg.Mode }
func (c *Client) ReadyToOrder() bool {
	return c != nil && c.cfg.OrderURL != "" && c.cfg.ShipTo.Complete() && c.cfg.BillTo.Complete()
}
func (c *Client) ShipTo() Address { return c.cfg.ShipTo }
func (c *Client) VerifyConfirmationCredentials(username, password string) bool {
	if c == nil || username == "" || password == "" {
		return false
	}
	userHash, expectedUserHash := sha256.Sum256([]byte(username)), sha256.Sum256([]byte(c.cfg.FromIdentity))
	passHash, expectedPassHash := sha256.Sum256([]byte(password)), sha256.Sum256([]byte(c.cfg.SharedSecret))
	return subtle.ConstantTimeCompare(userHash[:], expectedUserHash[:]) == 1 && subtle.ConstantTimeCompare(passHash[:], expectedPassHash[:]) == 1
}
func (c *Client) BuyerIdentity() string { return c.cfg.FromIdentity }

// VerifyConfirmationCXML validates the credentials Amazon places in an
// inbound cXML Header when "cXML Authentication" is selected in its UI.
func (c *Client) VerifyConfirmationCXML(data []byte) bool {
	return c.ConfirmationCXMLAuthStatus(data) == "valid"
}

// ConfirmationCXMLAuthStatus reports only a safe validation category. It never
// includes the received credentials or cXML contents in diagnostic logs.
func (c *Client) ConfirmationCXMLAuthStatus(data []byte) string {
	if c == nil || len(data) == 0 || len(data) > maxCXMLBytes {
		return "missing_or_oversized_cxml"
	}
	var doc struct {
		XMLName xml.Name `xml:"cXML"`
		Header  struct {
			To     credential       `xml:"To>Credential"`
			Sender senderCredential `xml:"Sender>Credential"`
		} `xml:"Header"`
	}
	if xml.Unmarshal(data, &doc) != nil || doc.XMLName.Local != "cXML" {
		return "invalid_cxml"
	}
	if doc.Header.Sender.SharedSecret == "" {
		return "missing_sender_secret"
	}
	// Amazon may put the configured identity in To or Sender, depending on
	// the cXML message type. The SharedSecret must always be in Sender.
	identity := doc.Header.To.Identity == c.cfg.FromIdentity || doc.Header.Sender.Identity == c.cfg.FromIdentity
	secretHash, expectedHash := sha256.Sum256([]byte(doc.Header.Sender.SharedSecret)), sha256.Sum256([]byte(c.cfg.SharedSecret))
	secretMatches := subtle.ConstantTimeCompare(secretHash[:], expectedHash[:]) == 1
	if !identity {
		return "buyer_identity_mismatch"
	}
	if !secretMatches {
		return "shared_secret_mismatch"
	}
	return "valid"
}

type credential struct {
	Domain   string `xml:"domain,attr"`
	Identity string `xml:"Identity"`
}
type senderCredential struct {
	Domain       string `xml:"domain,attr"`
	Identity     string `xml:"Identity"`
	SharedSecret string `xml:"SharedSecret"`
}
type header struct {
	From   credential `xml:"From>Credential"`
	To     credential `xml:"To>Credential"`
	Sender struct {
		Credential senderCredential `xml:"Credential"`
		UserAgent  string           `xml:"UserAgent"`
	} `xml:"Sender"`
}
type money struct {
	Currency string `xml:"currency,attr"`
	Amount   string `xml:",chardata"`
}
type setupDocument struct {
	XMLName   xml.Name `xml:"cXML"`
	PayloadID string   `xml:"payloadID,attr"`
	Timestamp string   `xml:"timestamp,attr"`
	Version   string   `xml:"version,attr"`
	Header    header   `xml:"Header"`
	Request   struct {
		DeploymentMode string `xml:"deploymentMode,attr"`
		Setup          struct {
			Operation   string `xml:"operation,attr"`
			BuyerCookie string `xml:"BuyerCookie"`
			UserEmail   struct {
				Name  string `xml:"name,attr"`
				Value string `xml:",chardata"`
			} `xml:"Extrinsic"`
			BrowserFormPost struct {
				URL string `xml:"URL"`
			} `xml:"BrowserFormPost"`
		} `xml:"PunchOutSetupRequest"`
	} `xml:"Request"`
}
type setupResponse struct {
	Response struct {
		Status struct {
			Code string `xml:"code,attr"`
			Text string `xml:"text,attr"`
		} `xml:"Status"`
		Setup struct {
			StartPage struct {
				URL string `xml:"URL"`
			} `xml:"StartPage"`
		} `xml:"PunchOutSetupResponse"`
	} `xml:"Response"`
}

func (c *Client) Start(ctx context.Context, buyerEmail, buyerCookie string) (string, error) {
	if c == nil {
		return "", errors.New("Amazon PunchOut ist nicht konfiguriert")
	}
	if _, err := url.Parse("mailto:" + buyerEmail); err != nil || !strings.Contains(buyerEmail, "@") || strings.ContainsAny(buyerEmail, " \n\r\t") {
		return "", errors.New("Amazon Business benötigt eine gültige Benutzer-E-Mail")
	}
	if buyerCookie == "" {
		return "", errors.New("PunchOut-Sitzung fehlt")
	}
	payloadID, err := newPayloadID()
	if err != nil {
		return "", err
	}
	doc := setupDocument{PayloadID: payloadID, Timestamp: time.Now().UTC().Format(time.RFC3339), Version: "1.2.014", Header: c.header()}
	doc.Request.DeploymentMode = c.cfg.Mode
	doc.Request.Setup.Operation = "create"
	doc.Request.Setup.BuyerCookie = buyerCookie
	doc.Request.Setup.UserEmail.Name, doc.Request.Setup.UserEmail.Value = "UserEmail", buyerEmail
	doc.Request.Setup.BrowserFormPost.URL = c.cfg.ReturnURL
	endpoint := c.cfg.TestURL
	if c.cfg.Mode == "production" {
		endpoint = c.cfg.LiveURL
	}
	response, err := c.post(ctx, endpoint, doc)
	if err != nil {
		return "", err
	}
	var parsed setupResponse
	if err := xml.Unmarshal(response, &parsed); err != nil {
		return "", errors.New("Amazon hat keine gültige cXML-Antwort geliefert")
	}
	if parsed.Response.Status.Code != "200" {
		return "", fmt.Errorf("Amazon PunchOut antwortet mit Status %s", parsed.Response.Status.Code)
	}
	startURL := strings.TrimSpace(parsed.Response.Setup.StartPage.URL)
	urlValue, err := url.Parse(startURL)
	if err != nil || urlValue.Scheme != "https" || !amazonHost(urlValue.Hostname()) {
		return "", errors.New("Amazon hat eine ungültige Start-URL geliefert")
	}
	return startURL, nil
}

func amazonHost(host string) bool {
	host = strings.ToLower(host)
	return host == "amazon.de" || strings.HasSuffix(host, ".amazon.de") || host == "amazon.com" || strings.HasSuffix(host, ".amazon.com")
}

func (c *Client) header() header {
	var value header
	value.From = credential{Domain: "NetworkId", Identity: c.cfg.FromIdentity}
	value.To = credential{Domain: "NetworkId", Identity: "Amazon"}
	value.Sender.Credential = senderCredential{Domain: "NetworkId", Identity: c.cfg.FromIdentity, SharedSecret: c.cfg.SharedSecret}
	value.Sender.UserAgent = "ProcurementCore"
	return value
}

type CartLine struct {
	SupplierPartID          string `json:"supplierPartId"`
	SupplierPartAuxiliaryID string `json:"supplierPartAuxiliaryId"`
	Description             string `json:"description"`
	Quantity                int    `json:"quantity"`
	Unit                    string `json:"unit"`
	UnitPriceCents          int64  `json:"unitPriceCents"`
	URL                     string `json:"url"`
}
type Cart struct {
	BuyerCookie string     `json:"-"`
	Currency    string     `json:"currency"`
	TotalCents  int64      `json:"totalCents"`
	Lines       []CartLine `json:"lines"`
}
type orderMessage struct {
	Message struct {
		Order struct {
			BuyerCookie string `xml:"BuyerCookie"`
			Header      struct {
				Total struct {
					Money money `xml:"Money"`
				} `xml:"Total"`
			} `xml:"PunchOutOrderMessageHeader"`
			Items []struct {
				Quantity string `xml:"quantity,attr"`
				ItemID   struct {
					PartID      string `xml:"SupplierPartID"`
					AuxiliaryID string `xml:"SupplierPartAuxiliaryID"`
				} `xml:"ItemID"`
				Detail struct {
					UnitPrice struct {
						Money money `xml:"Money"`
					} `xml:"UnitPrice"`
					Description string `xml:"Description"`
					Unit        string `xml:"UnitOfMeasure"`
					URL         string `xml:"URL"`
				} `xml:"ItemDetail"`
			} `xml:"ItemIn"`
		} `xml:"PunchOutOrderMessage"`
	} `xml:"Message"`
}

var decimalAmount = regexp.MustCompile(`^\d+(?:\.\d{1,2})?$`)
var amazonSPAID = regexp.MustCompile(`\basid-[A-Za-z0-9_-]+\b`)

func parseCents(raw string) (int64, error) {
	raw = strings.TrimSpace(raw)
	if !decimalAmount.MatchString(raw) {
		return 0, errors.New("ungültiger Geldbetrag")
	}
	parts := strings.SplitN(raw, ".", 2)
	whole, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || whole > 1000000000 {
		return 0, errors.New("Geldbetrag ist zu groß")
	}
	frac := "00"
	if len(parts) == 2 {
		frac = (parts[1] + "00")[:2]
	}
	cents, _ := strconv.ParseInt(frac, 10, 64)
	return whole*100 + cents, nil
}

func ParseCart(data []byte) (Cart, error) {
	if len(data) == 0 || len(data) > maxCXMLBytes {
		return Cart{}, errors.New("Amazon-Warenkorb fehlt oder ist zu groß")
	}
	var message orderMessage
	if err := xml.Unmarshal(data, &message); err != nil {
		return Cart{}, errors.New("Amazon-Warenkorb enthält ungültiges cXML")
	}
	source := message.Message.Order
	if source.BuyerCookie == "" || len(source.Items) == 0 || len(source.Items) > 50 {
		return Cart{}, errors.New("Amazon-Warenkorb enthält keine gültige Sitzung oder Positionen")
	}
	cart := Cart{BuyerCookie: source.BuyerCookie, Currency: source.Header.Total.Money.Currency, Lines: make([]CartLine, 0, len(source.Items))}
	if cart.Currency != "EUR" {
		return Cart{}, errors.New("Amazon-Warenkorb muss EUR verwenden")
	}
	var err error
	cart.TotalCents, err = parseCents(source.Header.Total.Money.Amount)
	if err != nil {
		return Cart{}, err
	}
	for _, item := range source.Items {
		quantity, parseErr := strconv.Atoi(strings.TrimSpace(item.Quantity))
		if parseErr != nil || quantity < 1 || quantity > 999 {
			return Cart{}, errors.New("Amazon-Position enthält eine ungültige Menge")
		}
		partID := strings.TrimSpace(item.ItemID.PartID)
		auxiliaryID := strings.TrimSpace(item.ItemID.AuxiliaryID)
		description := strings.TrimSpace(item.Detail.Description)
		if auxiliaryID == "" {
			if before, after, found := strings.Cut(description, "|"); found && amazonSPAID.MatchString(strings.TrimSpace(after)) {
				auxiliaryID = amazonSPAID.FindString(after)
				description = strings.TrimSpace(before)
			}
		}
		if auxiliaryID == "" {
			auxiliaryID = amazonSPAID.FindString(string(data))
		}
		if partID == "" || auxiliaryID == "" || description == "" || len(partID) > 120 || len(auxiliaryID) > 500 || len([]rune(description)) > 500 {
			return Cart{}, errors.New("Amazon-Position enthält keine bestellbare Artikelkennung")
		}
		if item.Detail.UnitPrice.Money.Currency != "EUR" {
			return Cart{}, errors.New("Amazon-Position hat eine unerwartete Währung")
		}
		price, parseErr := parseCents(item.Detail.UnitPrice.Money.Amount)
		if parseErr != nil {
			return Cart{}, parseErr
		}
		unit := strings.TrimSpace(item.Detail.Unit)
		if unit == "" {
			unit = "EA"
		}
		if len(unit) > 30 {
			return Cart{}, errors.New("Amazon-Einheit ist zu lang")
		}
		link := strings.TrimSpace(item.Detail.URL)
		if link != "" {
			u, parseErr := url.Parse(link)
			if parseErr != nil || u.Scheme != "https" || !amazonHost(u.Hostname()) || len(link) > 2000 {
				link = ""
			}
		}
		cart.Lines = append(cart.Lines, CartLine{SupplierPartID: partID, SupplierPartAuxiliaryID: auxiliaryID, Description: description, Quantity: quantity, Unit: unit, UnitPriceCents: price, URL: link})
	}
	return cart, nil
}

type OrderLine struct {
	SupplierPartID          string
	SupplierPartAuxiliaryID string
	Description             string
	Quantity                int
	Unit                    string
	UnitPriceCents          int64
}
type Order struct {
	Number string
	Lines  []OrderLine
}
type orderDocument struct {
	XMLName   xml.Name `xml:"cXML"`
	PayloadID string   `xml:"payloadID,attr"`
	Timestamp string   `xml:"timestamp,attr"`
	Version   string   `xml:"version,attr"`
	Header    header   `xml:"Header"`
	Request   struct {
		DeploymentMode string `xml:"deploymentMode,attr"`
		Order          struct {
			Header struct {
				OrderID   string `xml:"orderID,attr"`
				OrderDate string `xml:"orderDate,attr"`
				Type      string `xml:"type,attr"`
				Total     struct {
					Money money `xml:"Money"`
				} `xml:"Total"`
				ShipTo xmlAddress `xml:"ShipTo>Address"`
				BillTo xmlAddress `xml:"BillTo>Address"`
			} `xml:"OrderRequestHeader"`
			Items []struct {
				Quantity   int `xml:"quantity,attr"`
				LineNumber int `xml:"lineNumber,attr"`
				ItemID     struct {
					PartID      string `xml:"SupplierPartID"`
					AuxiliaryID string `xml:"SupplierPartAuxiliaryID"`
				} `xml:"ItemID"`
				Detail struct {
					UnitPrice struct {
						Money money `xml:"Money"`
					} `xml:"UnitPrice"`
					Description struct {
						Language string `xml:"xml:lang,attr"`
						Value    string `xml:",chardata"`
					} `xml:"Description"`
					Unit string `xml:"UnitOfMeasure"`
				} `xml:"ItemDetail"`
			} `xml:"ItemOut"`
		} `xml:"OrderRequest"`
	} `xml:"Request"`
}
type xmlEmail struct {
	Name  string `xml:"name,attr"`
	Value string `xml:",chardata"`
}

type xmlAddress struct {
	CountryCode string `xml:"isoCountryCode,attr"`
	Name        struct {
		Language string `xml:"xml:lang,attr"`
		Value    string `xml:",chardata"`
	} `xml:"Name"`
	Postal struct {
		Name       string `xml:"name,attr"`
		DeliverTo  string `xml:"DeliverTo"`
		Street     string `xml:"Street"`
		City       string `xml:"City"`
		Region     string `xml:"State,omitempty"`
		PostalCode string `xml:"PostalCode"`
		Country    struct {
			Code  string `xml:"isoCountryCode,attr"`
			Value string `xml:",chardata"`
		} `xml:"Country"`
	} `xml:"PostalAddress"`
	Email *xmlEmail `xml:"Email,omitempty"`
}

func addressXML(a Address) xmlAddress {
	value := xmlAddress{CountryCode: strings.ToUpper(a.Country)}
	value.Name.Language, value.Name.Value = "de", a.Company
	value.Postal.Name, value.Postal.Street, value.Postal.City, value.Postal.Region, value.Postal.PostalCode = "default", a.Street, a.City, a.Region, a.PostalCode
	value.Postal.DeliverTo = strings.TrimSpace(a.Recipient)
	if value.Postal.DeliverTo == "" {
		value.Postal.DeliverTo = a.Company
	}
	value.Postal.Country.Code = strings.ToUpper(a.Country)
	value.Postal.Country.Value = map[string]string{"DE": "Deutschland"}[value.Postal.Country.Code]
	if value.Postal.Country.Value == "" {
		value.Postal.Country.Value = value.Postal.Country.Code
	}
	if a.Email != "" {
		value.Email = &xmlEmail{Name: "default", Value: a.Email}
	}
	return value
}

func (c *Client) Submit(ctx context.Context, order Order, payloadID string) error {
	if !c.ReadyToOrder() {
		return errors.New("Amazon-Bestellung benötigt eine Bestell-URL und vollständige Lieferadresse")
	}
	if len(order.Lines) == 0 || len(order.Lines) > 50 || strings.TrimSpace(order.Number) == "" || payloadID == "" {
		return errors.New("Amazon-Bestellung enthält ungültige Positionen")
	}
	doc := orderDocument{PayloadID: payloadID, Timestamp: time.Now().UTC().Format(time.RFC3339), Version: "1.2.014", Header: c.header()}
	doc.Request.DeploymentMode = c.cfg.Mode
	doc.Request.Order.Header.OrderID, doc.Request.Order.Header.OrderDate, doc.Request.Order.Header.Type = order.Number, time.Now().UTC().Format(time.RFC3339), "new"
	doc.Request.Order.Header.ShipTo, doc.Request.Order.Header.BillTo = addressXML(c.cfg.ShipTo), addressXML(c.cfg.BillTo)
	var total int64
	for index, line := range order.Lines {
		if line.Quantity < 1 || line.Quantity > 999 || line.SupplierPartID == "" || line.SupplierPartAuxiliaryID == "" || line.UnitPriceCents < 0 || line.UnitPriceCents > 1000000000 {
			return errors.New("Amazon-Bestellposition ist ungültig")
		}
		total += int64(line.Quantity) * line.UnitPriceCents
		var item struct {
			Quantity   int `xml:"quantity,attr"`
			LineNumber int `xml:"lineNumber,attr"`
			ItemID     struct {
				PartID      string `xml:"SupplierPartID"`
				AuxiliaryID string `xml:"SupplierPartAuxiliaryID"`
			} `xml:"ItemID"`
			Detail struct {
				UnitPrice struct {
					Money money `xml:"Money"`
				} `xml:"UnitPrice"`
				Description struct {
					Language string `xml:"xml:lang,attr"`
					Value    string `xml:",chardata"`
				} `xml:"Description"`
				Unit string `xml:"UnitOfMeasure"`
			} `xml:"ItemDetail"`
		}
		item.Quantity, item.LineNumber = line.Quantity, index+1
		item.ItemID.PartID, item.ItemID.AuxiliaryID = line.SupplierPartID, line.SupplierPartAuxiliaryID
		item.Detail.UnitPrice.Money = money{Currency: "EUR", Amount: centsString(line.UnitPriceCents)}
		item.Detail.Description.Language, item.Detail.Description.Value = "de", line.Description
		item.Detail.Unit = line.Unit
		doc.Request.Order.Items = append(doc.Request.Order.Items, item)
	}
	doc.Request.Order.Header.Total.Money = money{Currency: "EUR", Amount: centsString(total)}
	response, err := c.post(ctx, c.cfg.OrderURL, doc)
	if err != nil {
		return err
	}
	var parsed setupResponse
	if err := xml.Unmarshal(response, &parsed); err != nil {
		return errors.New("Amazon hat keine eindeutige Bestellantwort geliefert")
	}
	if parsed.Response.Status.Code != "200" {
		return fmt.Errorf("Amazon hat die Bestellung nicht eindeutig bestätigt (Status %s)", parsed.Response.Status.Code)
	}
	return nil
}

func centsString(cents int64) string { return fmt.Sprintf("%d.%02d", cents/100, cents%100) }

func newPayloadID() (string, error) {
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(random[:]) + "@procurementcore", nil
}

func (c *Client) post(ctx context.Context, endpoint string, doc any) ([]byte, error) {
	data, err := xml.Marshal(doc)
	if err != nil {
		return nil, err
	}
	data = append([]byte(xml.Header), data...)
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", "text/xml; charset=UTF-8")
	request.Header.Set("Accept", "text/xml")
	response, err := c.http.Do(request)
	if err != nil {
		return nil, fmt.Errorf("Amazon cXML-Verbindung fehlgeschlagen: %w", err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, maxCXMLBytes+1))
	if err != nil || len(body) > maxCXMLBytes {
		return nil, errors.New("Amazon cXML-Antwort ist zu groß oder unlesbar")
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, fmt.Errorf("Amazon cXML antwortet mit HTTP %d", response.StatusCode)
	}
	return body, nil
}
