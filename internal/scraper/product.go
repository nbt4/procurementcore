package scraper

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	stdhtml "html"
	"io"
	"math"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	xhtml "golang.org/x/net/html"
)

const (
	maxPageMegabytes  = 16
	maxPageBytes      = maxPageMegabytes << 20
	adamHallAPIBase   = "https://www.adamhall.com/shop/api/shopware"
	adamHallCacheTime = 5 * time.Minute
)

type Options struct {
	AdamHallUsername string
	AdamHallPassword string
}

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
	client           *http.Client
	resolver         *net.Resolver
	adamHallClient   *http.Client
	adamHallBaseURL  string
	adamHallUsername string
	adamHallPassword string
	adamHallMu       sync.Mutex
	adamHallPrices   map[string]adamHallPrice
	adamHallExpires  time.Time
}

type adamHallPrice struct {
	Cents    int64
	Currency string
	Found    bool
}

func New(options Options) *Fetcher {
	resolver := net.DefaultResolver
	dialer := &net.Dialer{Timeout: 6 * time.Second, KeepAlive: 30 * time.Second}
	fetcher := &Fetcher{
		resolver:         resolver,
		adamHallBaseURL:  adamHallAPIBase,
		adamHallUsername: options.AdamHallUsername,
		adamHallPassword: options.AdamHallPassword,
	}
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
	fetcher.adamHallClient = &http.Client{
		Transport: transport,
		Timeout:   12 * time.Second,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
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
	preview, err := parseDownloadedPage(body, response.Request.URL, maxPageBytes)
	if err != nil {
		return ProductPreview{}, err
	}
	if isAdamHallHost(response.Request.URL.Hostname()) && f.adamHallUsername != "" && preview.SKU != "" {
		price, err := f.adamHallPrice(ctx, preview.SKU)
		if err != nil {
			return ProductPreview{}, err
		}
		if price.Found {
			preview.PriceCents = price.Cents
			preview.Currency = price.Currency
			preview.Source = "Adam Hall Shop/HTML+Preisliste"
		}
	}
	return preview, nil
}

func (f *Fetcher) adamHallPrice(ctx context.Context, sku string) (adamHallPrice, error) {
	f.adamHallMu.Lock()
	defer f.adamHallMu.Unlock()

	if f.adamHallPrices == nil || !time.Now().Before(f.adamHallExpires) {
		prices, expires, err := f.downloadAdamHallPrices(ctx)
		if err != nil {
			return adamHallPrice{}, err
		}
		f.adamHallPrices, f.adamHallExpires = prices, expires
	}
	for productNumber, price := range f.adamHallPrices {
		if strings.EqualFold(productNumber, sku) {
			return price, nil
		}
	}
	return adamHallPrice{}, nil
}

func (f *Fetcher) downloadAdamHallPrices(ctx context.Context) (map[string]adamHallPrice, time.Time, error) {
	contextToken, err := f.loginAdamHall(ctx)
	if err != nil {
		return nil, time.Time{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, f.adamHallBaseURL+"/pricelist", nil)
	if err != nil {
		return nil, time.Time{}, errors.New("Adam-Hall-Preisdienst konnte nicht aufgerufen werden")
	}
	setAdamHallHeaders(req)
	req.Header.Set("sw-context-token", contextToken)
	response, err := f.adamHallClient.Do(req)
	if err != nil {
		return nil, time.Time{}, errors.New("Adam-Hall-Preisliste konnte nicht abgerufen werden")
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden {
		return nil, time.Time{}, errors.New("Adam-Hall-Sitzung wurde abgelehnt")
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, time.Time{}, fmt.Errorf("Adam-Hall-Preisdienst antwortet mit HTTP %d", response.StatusCode)
	}

	var payload struct {
		Success         bool                      `json:"success"`
		ExpiredDateTime string                    `json:"expiredDateTime"`
		Currency        any                       `json:"currency"`
		Articles        map[string]map[string]any `json:"articles"`
	}
	decoder := json.NewDecoder(io.LimitReader(response.Body, maxPageBytes))
	decoder.UseNumber()
	if err := decoder.Decode(&payload); err != nil || !payload.Success {
		return nil, time.Time{}, errors.New("Adam-Hall-Preisliste enthält keine gültigen Daten")
	}

	prices := make(map[string]adamHallPrice, len(payload.Articles))
	for productNumber, article := range payload.Articles {
		if unavailable, _ := article["unbuyable"].(bool); unavailable {
			continue
		}
		value := valueString(article["price"])
		cents := priceCents(value)
		if value == "" || cents <= 0 {
			continue
		}
		prices[productNumber] = adamHallPrice{
			Cents:    cents,
			Currency: strings.ToUpper(first(valueString(article["currency"]), valueString(payload.Currency), "EUR")),
			Found:    true,
		}
	}
	expires := time.Now().Add(adamHallCacheTime)
	if parsed, err := time.Parse(time.RFC3339, payload.ExpiredDateTime); err == nil && parsed.After(time.Now()) {
		expires = parsed
	}
	return prices, expires, nil
}

func (f *Fetcher) loginAdamHall(ctx context.Context) (string, error) {
	jar, err := cookiejar.New(nil)
	if err != nil {
		return "", errors.New("Adam-Hall-Anmeldung konnte nicht vorbereitet werden")
	}
	client := *f.adamHallClient
	client.Jar = jar
	client.CheckRedirect = func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }

	contextToken, err := f.newAdamHallContext(ctx, &client)
	if err != nil {
		return "", err
	}
	loginURL, err := f.adamHallLoginURL(ctx, &client, contextToken)
	if err != nil {
		return "", err
	}

	verifier, challenge, err := newPKCEPair()
	if err != nil {
		return "", errors.New("Adam-Hall-Anmeldung konnte nicht vorbereitet werden")
	}
	query := loginURL.Query()
	query.Set("code_challenge", challenge)
	query.Set("code_challenge_method", "S256")
	loginURL.RawQuery = query.Encode()

	settings, err := f.loadAdamHallAuthorization(ctx, &client, loginURL)
	if err != nil {
		return "", err
	}
	code, redirectURI, err := f.submitAdamHallAuthorization(ctx, &client, loginURL, settings)
	if err != nil {
		return "", err
	}
	return f.exchangeAdamHallCode(ctx, &client, contextToken, code, verifier, redirectURI)
}

func (f *Fetcher) newAdamHallContext(ctx context.Context, client *http.Client) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, f.adamHallBaseURL+"/context", nil)
	if err != nil {
		return "", errors.New("Adam-Hall-Anmeldung konnte nicht vorbereitet werden")
	}
	setAdamHallHeaders(req)
	response, err := client.Do(req)
	if err != nil {
		return "", errors.New("Adam-Hall-Sitzung konnte nicht gestartet werden")
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return "", fmt.Errorf("Adam-Hall-Sitzung antwortet mit HTTP %d", response.StatusCode)
	}
	token := response.Header.Get("sw-context-token")
	if token == "" {
		return "", errors.New("Adam-Hall-Sitzung liefert keinen Kontext")
	}
	return token, nil
}

func (f *Fetcher) adamHallLoginURL(ctx context.Context, client *http.Client, contextToken string) (*url.URL, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, f.adamHallBaseURL+"/azure/urls", nil)
	if err != nil {
		return nil, errors.New("Adam-Hall-Anmeldung konnte nicht vorbereitet werden")
	}
	setAdamHallHeaders(req)
	req.Header.Set("sw-context-token", contextToken)
	response, err := client.Do(req)
	if err != nil {
		return nil, errors.New("Adam-Hall-Anmeldedienst konnte nicht aufgerufen werden")
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, fmt.Errorf("Adam-Hall-Anmeldedienst antwortet mit HTTP %d", response.StatusCode)
	}
	var payload struct {
		LoginURL string `json:"loginUrl"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&payload); err != nil {
		return nil, errors.New("Adam-Hall-Anmeldedienst liefert keine gültige URL")
	}
	loginURL, err := url.Parse(payload.LoginURL)
	if err != nil || loginURL.Scheme != "https" || !f.allowedAdamHallIdentityHost(loginURL.Hostname()) {
		return nil, errors.New("Adam-Hall-Anmeldedienst liefert keine zulässige URL")
	}
	return loginURL, nil
}

type adamHallAuthorizationSettings struct {
	CSRF    string `json:"csrf"`
	TransID string `json:"transId"`
	Hosts   struct {
		Tenant string `json:"tenant"`
		Policy string `json:"policy"`
	} `json:"hosts"`
}

func (f *Fetcher) loadAdamHallAuthorization(ctx context.Context, client *http.Client, loginURL *url.URL) (adamHallAuthorizationSettings, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, loginURL.String(), nil)
	if err != nil {
		return adamHallAuthorizationSettings{}, errors.New("Adam-Hall-Anmeldung konnte nicht vorbereitet werden")
	}
	req.Header.Set("Accept", "text/html,application/xhtml+xml")
	req.Header.Set("User-Agent", "Mozilla/5.0 ProcurementCore/1.0")
	response, err := client.Do(req)
	if err != nil {
		return adamHallAuthorizationSettings{}, errors.New("Adam-Hall-Anmeldung konnte nicht geöffnet werden")
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return adamHallAuthorizationSettings{}, fmt.Errorf("Adam-Hall-Anmeldung antwortet mit HTTP %d", response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return adamHallAuthorizationSettings{}, errors.New("Adam-Hall-Anmeldung konnte nicht gelesen werden")
	}
	const prefix = "var SETTINGS = "
	start := strings.Index(string(body), prefix)
	if start < 0 {
		return adamHallAuthorizationSettings{}, errors.New("Adam-Hall-Anmeldung liefert keine PKCE-Konfiguration")
	}
	settingsJSON := string(body[start+len(prefix):])
	end := strings.Index(settingsJSON, ";")
	if end < 0 {
		return adamHallAuthorizationSettings{}, errors.New("Adam-Hall-Anmeldung liefert keine PKCE-Konfiguration")
	}
	var settings adamHallAuthorizationSettings
	if err := json.Unmarshal([]byte(settingsJSON[:end]), &settings); err != nil || settings.CSRF == "" || settings.TransID == "" || settings.Hosts.Tenant == "" || settings.Hosts.Policy == "" {
		return adamHallAuthorizationSettings{}, errors.New("Adam-Hall-Anmeldung liefert keine gültige PKCE-Konfiguration")
	}
	return settings, nil
}

func (f *Fetcher) submitAdamHallAuthorization(ctx context.Context, client *http.Client, loginURL *url.URL, settings adamHallAuthorizationSettings) (string, string, error) {
	identityBase := &url.URL{Scheme: loginURL.Scheme, Host: loginURL.Host}
	selfAsserted := identityBase.ResolveReference(&url.URL{Path: strings.TrimSuffix(settings.Hosts.Tenant, "/") + "/SelfAsserted"})
	query := selfAsserted.Query()
	query.Set("tx", settings.TransID)
	query.Set("p", settings.Hosts.Policy)
	selfAsserted.RawQuery = query.Encode()
	form := url.Values{"email": {f.adamHallUsername}, "password": {f.adamHallPassword}, "request_type": {"RESPONSE"}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, selfAsserted.String(), strings.NewReader(form.Encode()))
	if err != nil {
		return "", "", errors.New("Adam-Hall-Anmeldung konnte nicht vorbereitet werden")
	}
	req.Header.Set("Accept", "application/json, text/javascript, */*; q=0.01")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded; charset=UTF-8")
	req.Header.Set("User-Agent", "Mozilla/5.0 ProcurementCore/1.0")
	req.Header.Set("X-CSRF-TOKEN", settings.CSRF)
	req.Header.Set("X-Requested-With", "XMLHttpRequest")
	req.Header.Set("Referer", loginURL.String())
	response, err := client.Do(req)
	if err != nil {
		return "", "", errors.New("Adam-Hall-Anmeldung konnte nicht durchgeführt werden")
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil || response.StatusCode < 200 || response.StatusCode >= 300 || !adamHallAuthorizationAccepted(body) {
		return "", "", errors.New("Adam-Hall-Anmeldung ist fehlgeschlagen")
	}

	confirmed := identityBase.ResolveReference(&url.URL{Path: strings.TrimSuffix(settings.Hosts.Tenant, "/") + "/api/CombinedSigninAndSignup/confirmed"})
	query = confirmed.Query()
	query.Set("csrf_token", settings.CSRF)
	query.Set("tx", settings.TransID)
	query.Set("p", settings.Hosts.Policy)
	query.Set("rememberMe", "false")
	confirmed.RawQuery = query.Encode()
	req, err = http.NewRequestWithContext(ctx, http.MethodGet, confirmed.String(), nil)
	if err != nil {
		return "", "", errors.New("Adam-Hall-Anmeldung konnte nicht abgeschlossen werden")
	}
	req.Header.Set("Accept", "text/html,application/xhtml+xml")
	req.Header.Set("User-Agent", "Mozilla/5.0 ProcurementCore/1.0")
	req.Header.Set("Referer", loginURL.String())
	response, err = client.Do(req)
	if err != nil {
		return "", "", errors.New("Adam-Hall-Anmeldung konnte nicht abgeschlossen werden")
	}
	response.Body.Close()
	if response.StatusCode < 300 || response.StatusCode >= 400 {
		return "", "", errors.New("Adam-Hall-Anmeldung liefert keinen Autorisierungscode")
	}
	callback, err := response.Location()
	if err != nil || callback.Hostname() == "" || !f.allowedAdamHallCallback(callback) {
		return "", "", errors.New("Adam-Hall-Anmeldung liefert keine zulässige Rücksprungadresse")
	}
	code := callback.Query().Get("code")
	if code == "" {
		return "", "", errors.New("Adam-Hall-Anmeldung liefert keinen Autorisierungscode")
	}
	callback.RawQuery = ""
	callback.Fragment = ""
	return code, callback.String(), nil
}

func (f *Fetcher) exchangeAdamHallCode(ctx context.Context, client *http.Client, contextToken, code, verifier, redirectURI string) (string, error) {
	body, err := json.Marshal(map[string]string{
		"code":          code,
		"code_verifier": verifier,
		"redirect_uri":  redirectURI,
		"returnUrl":     "de",
	})
	if err != nil {
		return "", errors.New("Adam-Hall-Anmeldung konnte nicht vorbereitet werden")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, f.adamHallBaseURL+"/customer/login?state", strings.NewReader(string(body)))
	if err != nil {
		return "", errors.New("Adam-Hall-Anmeldung konnte nicht vorbereitet werden")
	}
	setAdamHallHeaders(req)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("sw-context-token", contextToken)
	response, err := client.Do(req)
	if err != nil {
		return "", errors.New("Adam-Hall-Autorisierungscode konnte nicht eingelöst werden")
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden {
		return "", errors.New("Adam-Hall-Anmeldung ist fehlgeschlagen")
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return "", fmt.Errorf("Adam-Hall-Anmeldung antwortet mit HTTP %d", response.StatusCode)
	}
	token := response.Header.Get("sw-context-token")
	if token == "" {
		return "", errors.New("Adam-Hall-Anmeldung liefert keine gültige Sitzung")
	}
	return token, nil
}

func newPKCEPair() (string, string, error) {
	random := make([]byte, 48)
	if _, err := rand.Read(random); err != nil {
		return "", "", err
	}
	verifier := base64.RawURLEncoding.EncodeToString(random)
	digest := sha256.Sum256([]byte(verifier))
	return verifier, base64.RawURLEncoding.EncodeToString(digest[:]), nil
}

func adamHallAuthorizationAccepted(body []byte) bool {
	var payload struct {
		Status any `json:"status"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return false
	}
	return valueString(payload.Status) == "200"
}

func (f *Fetcher) allowedAdamHallIdentityHost(host string) bool {
	base, err := url.Parse(f.adamHallBaseURL)
	if err == nil && strings.EqualFold(host, base.Hostname()) {
		return true
	}
	return strings.EqualFold(host, "ahgb2c.b2clogin.com")
}

func (f *Fetcher) allowedAdamHallCallback(callback *url.URL) bool {
	base, err := url.Parse(f.adamHallBaseURL)
	if err != nil || callback.Scheme != base.Scheme || !strings.EqualFold(callback.Host, base.Host) {
		return false
	}
	return strings.HasPrefix(callback.Path, "/shop/") && strings.HasSuffix(callback.Path, "/customer/authorize")
}

func setAdamHallHeaders(req *http.Request) {
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "ProcurementCore Product Import/1.0")
	req.Header.Set("sw-access-key", "proxy")
}

func parseDownloadedPage(body []byte, sourceURL *url.URL, limit int) (ProductPreview, error) {
	truncated := len(body) > limit
	if truncated {
		body = body[:limit]
	}
	preview, err := ParseHTML(strings.NewReader(string(body)), sourceURL)
	if err == nil {
		return preview, nil
	}
	if truncated {
		return ProductPreview{}, fmt.Errorf("in den ersten %d MB wurden keine Produktdaten gefunden", maxPageMegabytes)
	}
	return ProductPreview{}, err
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
	if isAdamHallHost(sourceURL.Hostname()) {
		applyAdamHallPage(&preview, document)
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

func isAdamHallHost(host string) bool {
	host = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(host), "."))
	return host == "adamhall.com" || strings.HasSuffix(host, ".adamhall.com")
}

func applyAdamHallPage(preview *ProductPreview, document *xhtml.Node) {
	buyBox := findElement(document, func(node *xhtml.Node) bool {
		return attribute(node, "data-testid") == "cms-element-buy-box"
	})
	if buyBox == nil {
		return
	}

	if heading := findElement(buyBox, func(node *xhtml.Node) bool { return node.Data == "h1" }); heading != nil {
		preview.Name = nodeText(heading)
		if preview.Model == "" {
			preview.Model = preview.Name
		}
	}
	if brand := findElement(buyBox, func(node *xhtml.Node) bool {
		return node.Data == "a" && strings.Contains(attribute(node, "href"), "/marken/")
	}); brand != nil && preview.Manufacturer == "" {
		preview.Manufacturer = first(attribute(brand, "title"), nodeText(brand))
	}

	forEachElement(buyBox, func(node *xhtml.Node) {
		if node.Data != "strong" || node.Parent == nil {
			return
		}
		labelText := nodeText(node)
		label := strings.TrimSpace(strings.TrimSuffix(labelText, ":"))
		value := strings.TrimSpace(strings.TrimPrefix(nodeText(node.Parent), labelText))
		if label == "" || value == "" {
			return
		}
		switch strings.ToLower(label) {
		case "artikel nr.", "artikelnummer", "article no.", "article number":
			if preview.SKU == "" {
				preview.SKU = value
			}
		default:
			preview.Attributes[label] = value
		}
	})

	specifications := findElement(document, func(node *xhtml.Node) bool {
		return attribute(node, "id") == "product-specifications"
	})
	forEachElement(specifications, func(node *xhtml.Node) {
		if node.Data != "div" || !hasClass(node, "font-bold") {
			return
		}
		valueNode := nextElementSibling(node)
		if valueNode == nil || valueNode.Data != "div" {
			return
		}
		name, value := nodeText(node), nodeText(valueNode)
		if name != "" && value != "" {
			preview.Attributes[name] = value
		}
	})
	preview.Source = "Adam Hall Shop/HTML"
}

func findElement(root *xhtml.Node, matches func(*xhtml.Node) bool) *xhtml.Node {
	if root == nil {
		return nil
	}
	if root.Type == xhtml.ElementNode && matches(root) {
		return root
	}
	for child := root.FirstChild; child != nil; child = child.NextSibling {
		if match := findElement(child, matches); match != nil {
			return match
		}
	}
	return nil
}

func forEachElement(root *xhtml.Node, visit func(*xhtml.Node)) {
	if root == nil {
		return
	}
	if root.Type == xhtml.ElementNode {
		visit(root)
	}
	for child := root.FirstChild; child != nil; child = child.NextSibling {
		forEachElement(child, visit)
	}
}

func nextElementSibling(node *xhtml.Node) *xhtml.Node {
	for sibling := node.NextSibling; sibling != nil; sibling = sibling.NextSibling {
		if sibling.Type == xhtml.ElementNode {
			return sibling
		}
	}
	return nil
}

func attribute(node *xhtml.Node, name string) string {
	if node == nil {
		return ""
	}
	for _, item := range node.Attr {
		if strings.EqualFold(item.Key, name) {
			return strings.TrimSpace(item.Val)
		}
	}
	return ""
}

func hasClass(node *xhtml.Node, class string) bool {
	for _, value := range strings.Fields(attribute(node, "class")) {
		if value == class {
			return true
		}
	}
	return false
}

func nodeText(node *xhtml.Node) string {
	if node == nil {
		return ""
	}
	var value strings.Builder
	var walk func(*xhtml.Node)
	walk = func(current *xhtml.Node) {
		if current.Type == xhtml.TextNode {
			value.WriteString(" ")
			value.WriteString(current.Data)
		}
		for child := current.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(node)
	return cleanText(value.String())
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
