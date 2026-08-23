package scraper

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	stdhtml "html"
	"io"
	"math"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	xhtml "golang.org/x/net/html"
)

const maxPageBytes = 2 << 20

type ProductPreview struct {
	Name         string            `json:"name"`
	Description  string            `json:"description"`
	SKU          string            `json:"sku"`
	Manufacturer string            `json:"manufacturer"`
	Model        string            `json:"model"`
	ImageURL     string            `json:"imageUrl"`
	PriceCents   int64             `json:"priceCents"`
	Currency     string            `json:"currency"`
	PurchaseURL  string            `json:"purchaseUrl"`
	Attributes   map[string]string `json:"attributes"`
	Source       string            `json:"source"`
}

type Fetcher struct {
	client   *http.Client
	resolver *net.Resolver
}

func New() *Fetcher {
	resolver := net.DefaultResolver
	dialer := &net.Dialer{Timeout: 6 * time.Second, KeepAlive: 30 * time.Second}
	fetcher := &Fetcher{resolver: resolver}
	transport := &http.Transport{
		DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(address)
			if err != nil {
				return nil, err
			}
			ips, err := resolvePublic(ctx, resolver, host)
			if err != nil {
				return nil, err
			}
			return dialer.DialContext(ctx, network, net.JoinHostPort(ips[0].String(), port))
		},
		TLSHandshakeTimeout:   6 * time.Second,
		ResponseHeaderTimeout: 8 * time.Second,
		DisableCompression:    false,
	}
	fetcher.client = &http.Client{
		Transport: transport,
		Timeout:   12 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 3 {
				return errors.New("zu viele Weiterleitungen")
			}
			return fetcher.validateURL(req.Context(), req.URL)
		},
	}
	return fetcher
}

func (f *Fetcher) Scrape(ctx context.Context, rawURL string) (ProductPreview, error) {
	target, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return ProductPreview{}, errors.New("Produktlink ist ungültig")
	}
	if err := f.validateURL(ctx, target); err != nil {
		return ProductPreview{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	if err != nil {
		return ProductPreview{}, errors.New("Produktlink ist ungültig")
	}
	req.Header.Set("User-Agent", "ProcurementCore Product Import/1.0")
	req.Header.Set("Accept", "text/html,application/xhtml+xml")
	response, err := f.client.Do(req)
	if err != nil {
		return ProductPreview{}, fmt.Errorf("Produktseite konnte nicht abgerufen werden: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return ProductPreview{}, fmt.Errorf("Produktseite antwortet mit HTTP %d", response.StatusCode)
	}
	contentType := strings.ToLower(response.Header.Get("Content-Type"))
	if contentType != "" && !strings.Contains(contentType, "text/html") && !strings.Contains(contentType, "application/xhtml+xml") {
		return ProductPreview{}, errors.New("Produktlink liefert keine HTML-Seite")
	}
	limited := io.LimitReader(response.Body, maxPageBytes+1)
	body, err := io.ReadAll(limited)
	if err != nil {
		return ProductPreview{}, errors.New("Produktseite konnte nicht gelesen werden")
	}
	if len(body) > maxPageBytes {
		return ProductPreview{}, errors.New("Produktseite ist größer als 2 MB")
	}
	preview, err := ParseHTML(strings.NewReader(string(body)), response.Request.URL)
	if err != nil {
		return ProductPreview{}, err
	}
	return preview, nil
}

func (f *Fetcher) validateURL(ctx context.Context, target *url.URL) error {
	if target.Scheme != "http" && target.Scheme != "https" {
		return errors.New("nur HTTP- und HTTPS-Produktlinks sind erlaubt")
	}
	if target.Hostname() == "" || target.User != nil {
		return errors.New("Produktlink ist ungültig")
	}
	if port := target.Port(); port != "" && port != "80" && port != "443" {
		return errors.New("Produktlinks dürfen nur Standardports verwenden")
	}
	_, err := resolvePublic(ctx, f.resolver, target.Hostname())
	return err
}

func resolvePublic(ctx context.Context, resolver *net.Resolver, host string) ([]net.IP, error) {
	addresses, err := resolver.LookupIPAddr(ctx, host)
	if err != nil || len(addresses) == 0 {
		return nil, errors.New("Host des Produktlinks konnte nicht aufgelöst werden")
	}
	ips := make([]net.IP, 0, len(addresses))
	for _, address := range addresses {
		if !isPublicIP(address.IP) {
			return nil, errors.New("interne oder lokale Netzwerkziele sind nicht erlaubt")
		}
		ips = append(ips, address.IP)
	}
	return ips, nil
}

func isPublicIP(ip net.IP) bool {
	if ip == nil || !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified() {
		return false
	}
	cgnat := &net.IPNet{IP: net.IPv4(100, 64, 0, 0), Mask: net.CIDRMask(10, 32)}
	return !cgnat.Contains(ip)
}

func ParseHTML(reader io.Reader, sourceURL *url.URL) (ProductPreview, error) {
	document, err := xhtml.Parse(reader)
	if err != nil {
		return ProductPreview{}, errors.New("Produktseite enthält ungültiges HTML")
	}
	meta := map[string]string{}
	var title string
	var scripts []string
	var walk func(*xhtml.Node)
	walk = func(node *xhtml.Node) {
		if node.Type == xhtml.ElementNode {
			switch node.Data {
			case "meta":
				var key, content string
				for _, attribute := range node.Attr {
					switch strings.ToLower(attribute.Key) {
					case "property", "name", "itemprop":
						key = strings.ToLower(strings.TrimSpace(attribute.Val))
					case "content":
						content = strings.TrimSpace(attribute.Val)
					}
				}
				if key != "" && content != "" && meta[key] == "" {
					meta[key] = content
				}
			case "title":
				if node.FirstChild != nil {
					title = strings.TrimSpace(node.FirstChild.Data)
				}
			case "script":
				for _, attribute := range node.Attr {
					if strings.EqualFold(attribute.Key, "type") && strings.EqualFold(strings.TrimSpace(attribute.Val), "application/ld+json") && node.FirstChild != nil {
						scripts = append(scripts, node.FirstChild.Data)
					}
				}
			}
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(document)

	preview := ProductPreview{Attributes: map[string]string{}, PurchaseURL: sourceURL.String(), Currency: "EUR"}
	for _, script := range scripts {
		var value any
		decoder := json.NewDecoder(strings.NewReader(script))
		decoder.UseNumber()
		if decoder.Decode(&value) != nil {
			continue
		}
		if product := findProduct(value); product != nil {
			applyProduct(&preview, product)
			preview.Source = "JSON-LD"
			break
		}
	}
	if preview.Name == "" {
		preview.Name = first(meta["og:title"], meta["twitter:title"], title)
	}
	if preview.Description == "" {
		preview.Description = first(meta["og:description"], meta["description"], meta["twitter:description"])
	}
	if preview.ImageURL == "" {
		preview.ImageURL = first(meta["og:image"], meta["twitter:image"])
	}
	if preview.PriceCents == 0 {
		preview.PriceCents = priceCents(first(meta["product:price:amount"], meta["price"]))
	}
	if preview.Source != "JSON-LD" {
		preview.Currency = strings.ToUpper(first(meta["product:price:currency"], meta["pricecurrency"], preview.Currency, "EUR"))
	}
	if preview.Source == "" && preview.Name != "" {
		preview.Source = "OpenGraph/HTML"
	}
	preview.Name = cleanText(preview.Name)
	preview.Description = cleanText(preview.Description)
	preview.ImageURL = absoluteURL(sourceURL, preview.ImageURL)
	if preview.Name == "" {
		return ProductPreview{}, errors.New("auf der Seite wurden keine erkennbaren Produktdaten gefunden")
	}
	return preview, nil
}

func findProduct(value any) map[string]any {
	switch typed := value.(type) {
	case []any:
		for _, item := range typed {
			if product := findProduct(item); product != nil {
				return product
			}
		}
	case map[string]any:
		if hasType(typed["@type"], "Product") {
			return typed
		}
		for _, key := range []string{"@graph", "mainEntity", "itemListElement"} {
			if product := findProduct(typed[key]); product != nil {
				return product
			}
		}
	}
	return nil
}

func hasType(value any, expected string) bool {
	switch typed := value.(type) {
	case string:
		parts := strings.Split(typed, "/")
		return strings.EqualFold(parts[len(parts)-1], expected)
	case []any:
		for _, item := range typed {
			if hasType(item, expected) {
				return true
			}
		}
	}
	return false
}

func applyProduct(preview *ProductPreview, product map[string]any) {
	preview.Name = valueString(product["name"])
	preview.Description = valueString(product["description"])
	preview.SKU = first(valueString(product["sku"]), valueString(product["gtin13"]), valueString(product["gtin"]))
	preview.Manufacturer = first(valueString(product["manufacturer"]), valueString(product["brand"]))
	preview.Model = first(valueString(product["model"]), valueString(product["mpn"]))
	preview.ImageURL = valueString(product["image"])
	offer := firstObject(product["offers"])
	if offer != nil {
		preview.PriceCents = priceCents(first(valueString(offer["price"]), valueString(offer["lowPrice"])))
		preview.Currency = strings.ToUpper(first(valueString(offer["priceCurrency"]), "EUR"))
	}
	for _, property := range objectList(product["additionalProperty"]) {
		name := valueString(property["name"])
		value := valueString(property["value"])
		if name != "" && value != "" {
			preview.Attributes[cleanText(name)] = cleanText(value)
		}
	}
}

func objectList(value any) []map[string]any {
	switch typed := value.(type) {
	case map[string]any:
		return []map[string]any{typed}
	case []any:
		result := make([]map[string]any, 0, len(typed))
		for _, item := range typed {
			if object, ok := item.(map[string]any); ok {
				result = append(result, object)
			}
		}
		return result
	}
	return nil
}

func firstObject(value any) map[string]any {
	items := objectList(value)
	if len(items) == 0 {
		return nil
	}
	return items[0]
}

func valueString(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case json.Number:
		return typed.String()
	case float64:
		return strconv.FormatFloat(typed, 'f', -1, 64)
	case map[string]any:
		for _, key := range []string{"name", "value", "@value", "url"} {
			if result := valueString(typed[key]); result != "" {
				return result
			}
		}
	case []any:
		if len(typed) > 0 {
			return valueString(typed[0])
		}
	}
	return ""
}

func priceCents(value string) int64 {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0
	}
	var cleaned strings.Builder
	for _, char := range value {
		if (char >= '0' && char <= '9') || char == '.' || char == ',' || char == '-' {
			cleaned.WriteRune(char)
		}
	}
	number := cleaned.String()
	if strings.Contains(number, ",") && strings.Contains(number, ".") {
		if strings.LastIndex(number, ",") > strings.LastIndex(number, ".") {
			number = strings.ReplaceAll(number, ".", "")
			number = strings.ReplaceAll(number, ",", ".")
		} else {
			number = strings.ReplaceAll(number, ",", "")
		}
	} else if strings.Contains(number, ",") {
		number = strings.ReplaceAll(number, ",", ".")
	}
	parsed, err := strconv.ParseFloat(number, 64)
	if err != nil || parsed < 0 {
		return 0
	}
	return int64(math.Round(parsed * 100))
}

func cleanText(value string) string {
	return strings.Join(strings.Fields(stdhtml.UnescapeString(value)), " ")
}

func absoluteURL(base *url.URL, value string) string {
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil || value == "" {
		return ""
	}
	return base.ResolveReference(parsed).String()
}

func first(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
