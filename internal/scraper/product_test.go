package scraper

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return fn(request)
}

func TestParseHTMLUsesJSONLDProduct(t *testing.T) {
	source, _ := url.Parse("https://shop.example/products/lamp")
	page := `<html><head><script type="application/ld+json">{
      "@context":"https://schema.org", "@type":"Product", "name":"LED &amp; Lampe",
      "description":"Helle Lampe", "sku":"L-42", "brand":{"name":"Lumo"}, "model":"Pro",
      "image":"/lamp.jpg", "offers":{"@type":"Offer","price":"1.299,95","priceCurrency":"EUR"},
      "additionalProperty":[{"@type":"PropertyValue","name":"Leistung","value":"80 W"}]
    }</script></head></html>`
	preview, err := ParseHTML(strings.NewReader(page), source)
	if err != nil {
		t.Fatal(err)
	}
	if preview.Name != "LED & Lampe" || preview.SKU != "L-42" || preview.Manufacturer != "Lumo" || preview.Model != "Pro" {
		t.Fatalf("unexpected preview: %+v", preview)
	}
	if preview.PriceCents != 129995 || preview.Currency != "EUR" || preview.Attributes["Leistung"] != "80 W" {
		t.Fatalf("unexpected offer or attributes: %+v", preview)
	}
	if preview.ImageURL != "https://shop.example/lamp.jpg" || preview.PurchaseURL != source.String() {
		t.Fatalf("unexpected URLs: %+v", preview)
	}
}

func TestParseHTMLFallsBackToOpenGraph(t *testing.T) {
	source, _ := url.Parse("https://shop.example/item")
	page := `<html><head><meta property="og:title" content="Kabel"><meta name="description" content="10 Meter"><meta property="product:price:amount" content="12.50"><meta property="product:price:currency" content="EUR"></head></html>`
	preview, err := ParseHTML(strings.NewReader(page), source)
	if err != nil {
		t.Fatal(err)
	}
	if preview.Name != "Kabel" || preview.Description != "10 Meter" || preview.PriceCents != 1250 || preview.Source != "OpenGraph/HTML" {
		t.Fatalf("unexpected fallback: %+v", preview)
	}
}

func TestParseDownloadedPageAcceptsLargePagesWithEarlyMetadata(t *testing.T) {
	source, _ := url.Parse("https://shop.example/item")
	page := []byte(`<html><head><meta property="og:title" content="Großer Artikel"></head><body>` + strings.Repeat("x", 300))
	preview, err := parseDownloadedPage(page, source, 128)
	if err != nil {
		t.Fatal(err)
	}
	if preview.Name != "Großer Artikel" {
		t.Fatalf("unexpected preview: %+v", preview)
	}
}

func TestSTEX24SitemapFallbackUsesVerifiedProductURL(t *testing.T) {
	source, _ := url.Parse("https://stex24.com/de/136382-schrumpfschlauch-2zu1-wsb2-tr-160-80mm-5-8-zoll-136382")
	productSitemap := new(bytes.Buffer)
	compressed := gzip.NewWriter(productSitemap)
	_, _ = compressed.Write([]byte(`<urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9"><url><loc>` + source.String() + `</loc></url></urlset>`))
	_ = compressed.Close()

	fetcher := &Fetcher{client: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		var body []byte
		switch request.URL.Path {
		case "/de/sitemap.xml":
			body = []byte(`<sitemapindex xmlns="http://www.sitemaps.org/schemas/sitemap/0.9"><sitemap><loc>https://stex24.com/sitemap/products.xml.gz</loc></sitemap></sitemapindex>`)
		case "/sitemap/products.xml.gz":
			body = productSitemap.Bytes()
		default:
			t.Fatalf("unexpected sitemap request: %s", request.URL)
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewReader(body)), Header: make(http.Header), Request: request}, nil
	})}}

	preview, err := fetcher.scrapeSTEX24Sitemap(t.Context(), source)
	if err != nil {
		t.Fatal(err)
	}
	if preview.Name != "Schrumpfschlauch 2:1 WSB2 transparent 16,0/8,0 mm 5/8 Zoll" {
		t.Fatalf("unexpected product name: %q", preview.Name)
	}
	if preview.SKU != "136382" || preview.Model != "136382" || preview.Manufacturer != "STEX24" {
		t.Fatalf("unexpected identity: %+v", preview)
	}
	if preview.Attributes["Baugröße"] != "16,0/8,0 mm" || preview.Attributes["Farbe"] != "transparent" || preview.Attributes["Zollgröße"] != "5/8 Zoll" {
		t.Fatalf("unexpected URL attributes: %+v", preview.Attributes)
	}
}

func TestParseHTMLRecognizesAdamHallShopProduct(t *testing.T) {
	source, _ := url.Parse("https://www.adamhall.com/shop/de/ready-made-cables/pdu-6-8747x6")
	page := `<html><head>
      <meta name="og:title" content="PDU 6 - Standard Steckdosenleisten | Adam Hall Shop">
      <meta name="og:description" content="Stromverteiler 6-Fach | 1,4m">
    </head><body>
      <div data-testid="cms-element-buy-box">
        <a href="/shop/de/marken/adam-hall-cables" title="Adam Hall Cables"></a>
        <h1>PDU 6</h1>
        <p><strong>Artikel Nr.:</strong> 8747X6</p>
        <p><strong>EAN:</strong> 4049521208041</p>
        <p><strong>Einheit:</strong> Stück</p>
      </div>
      <div id="product-specifications">
        <div><div class="py-2 font-bold">Kabellänge</div><div>1.4 m</div></div>
        <div><div class="font-bold bg-gray-100">Anzahl Ausgänge</div><div>6</div></div>
      </div>
    </body></html>`

	preview, err := ParseHTML(strings.NewReader(page), source)
	if err != nil {
		t.Fatal(err)
	}
	if preview.Name != "PDU 6" || preview.SKU != "8747X6" || preview.Model != "PDU 6" {
		t.Fatalf("unexpected product identity: %+v", preview)
	}
	if preview.Manufacturer != "Adam Hall Cables" || preview.Source != "Adam Hall Shop/HTML" {
		t.Fatalf("unexpected source or manufacturer: %+v", preview)
	}
	if preview.Attributes["EAN"] != "4049521208041" || preview.Attributes["Einheit"] != "Stück" {
		t.Fatalf("unexpected product facts: %+v", preview.Attributes)
	}
	if preview.Attributes["Kabellänge"] != "1.4 m" || preview.Attributes["Anzahl Ausgänge"] != "6" {
		t.Fatalf("unexpected specifications: %+v", preview.Attributes)
	}
}

func TestParseHTMLDoesNotApplyAdamHallMarkupToOtherHosts(t *testing.T) {
	source, _ := url.Parse("https://shop.example/product")
	page := `<html><head><meta property="og:title" content="Original title"></head><body>
      <div data-testid="cms-element-buy-box"><h1>Adam-style title</h1><p><strong>Artikel Nr.:</strong> A-1</p></div>
    </body></html>`

	preview, err := ParseHTML(strings.NewReader(page), source)
	if err != nil {
		t.Fatal(err)
	}
	if preview.Name != "Original title" || preview.SKU != "" || preview.Source != "OpenGraph/HTML" {
		t.Fatalf("Adam Hall parser leaked into another host: %+v", preview)
	}
}

func TestAdamHallPriceUsesAuthenticatedPricelistAndCache(t *testing.T) {
	var loginCalls, priceCalls atomic.Int32
	var server *httptest.Server
	var expectedChallenge string
	server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/context":
			w.Header().Set("sw-context-token", "guest-context")
			_ = json.NewEncoder(w).Encode(map[string]any{"token": "guest-context"})
		case "/azure/urls":
			if r.Header.Get("sw-context-token") != "guest-context" {
				t.Fatal("missing guest context")
			}
			loginURL := server.URL + "/oauth/authorize?redirect_uri=" + url.QueryEscape(server.URL+"/shop/en/customer/authorize")
			_ = json.NewEncoder(w).Encode(map[string]string{"loginUrl": loginURL})
		case "/oauth/authorize":
			expectedChallenge = r.URL.Query().Get("code_challenge")
			if expectedChallenge == "" || r.URL.Query().Get("code_challenge_method") != "S256" {
				t.Fatal("missing PKCE challenge")
			}
			http.SetCookie(w, &http.Cookie{Name: "b2c-session", Value: "active", Path: "/"})
			_, _ = w.Write([]byte(`<!doctype html><script>var SETTINGS = {"csrf":"csrf-value","transId":"transaction-value","hosts":{"tenant":"/tenant/policy","policy":"test-policy"}};</script>`))
		case "/tenant/policy/SelfAsserted":
			loginCalls.Add(1)
			if cookie, err := r.Cookie("b2c-session"); err != nil || cookie.Value != "active" {
				t.Fatal("missing B2C cookie")
			}
			if r.Header.Get("X-CSRF-TOKEN") != "csrf-value" || r.URL.Query().Get("tx") != "transaction-value" {
				t.Fatal("missing B2C authorization data")
			}
			if err := r.ParseForm(); err != nil {
				t.Fatal(err)
			}
			if r.Form.Get("email") != "buyer@example.com" || r.Form.Get("password") != "shop-secret" || r.Form.Get("request_type") != "RESPONSE" {
				t.Fatal("unexpected credentials")
			}
			_ = json.NewEncoder(w).Encode(map[string]string{"status": "200"})
		case "/tenant/policy/api/CombinedSigninAndSignup/confirmed":
			if r.URL.Query().Get("csrf_token") != "csrf-value" || r.URL.Query().Get("p") != "test-policy" {
				t.Fatal("missing B2C confirmation data")
			}
			http.Redirect(w, r, server.URL+"/shop/en/customer/authorize?code=authorization-code", http.StatusFound)
		case "/customer/login":
			if r.URL.RawQuery != "state" || r.Header.Get("sw-context-token") != "guest-context" {
				t.Fatal("missing Shopware login state")
			}
			var exchange map[string]string
			if err := json.NewDecoder(r.Body).Decode(&exchange); err != nil {
				t.Fatal(err)
			}
			digest := sha256.Sum256([]byte(exchange["code_verifier"]))
			if exchange["code"] != "authorization-code" || exchange["redirect_uri"] != server.URL+"/shop/en/customer/authorize" || base64.RawURLEncoding.EncodeToString(digest[:]) != expectedChallenge {
				t.Fatal("invalid authorization-code exchange")
			}
			w.Header().Set("sw-context-token", "authenticated-context")
			_ = json.NewEncoder(w).Encode(map[string]string{"redirectUrl": "en"})
		case "/pricelist":
			priceCalls.Add(1)
			if r.Header.Get("sw-context-token") != "authenticated-context" {
				t.Fatal("missing authenticated context")
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"success":         true,
				"expiredDateTime": time.Now().Add(time.Hour).Format(time.RFC3339),
				"currency":        "EUR",
				"articles": map[string]any{
					"8747X6": map[string]any{"price": 12.345},
				},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	fetcher := &Fetcher{
		adamHallClient:   server.Client(),
		adamHallBaseURL:  server.URL,
		adamHallUsername: "buyer@example.com",
		adamHallPassword: "shop-secret",
	}
	for range 2 {
		price, err := fetcher.adamHallPrice(t.Context(), "8747x6")
		if err != nil {
			t.Fatal(err)
		}
		if !price.Found || price.Cents != 1235 || price.Currency != "EUR" {
			t.Fatalf("unexpected price: %+v", price)
		}
	}
	if loginCalls.Load() != 1 || priceCalls.Load() != 1 {
		t.Fatalf("expected cached pricelist, got %d logins and %d price calls", loginCalls.Load(), priceCalls.Load())
	}
}

func TestAdamHallPriceReportsRejectedLogin(t *testing.T) {
	var server *httptest.Server
	server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/context":
			w.Header().Set("sw-context-token", "guest-context")
		case "/azure/urls":
			_ = json.NewEncoder(w).Encode(map[string]string{"loginUrl": server.URL + "/oauth/authorize"})
		case "/oauth/authorize":
			_, _ = w.Write([]byte(`<script>var SETTINGS = {"csrf":"csrf-value","transId":"transaction-value","hosts":{"tenant":"/tenant/policy","policy":"test-policy"}};</script>`))
		case "/tenant/policy/SelfAsserted":
			_ = json.NewEncoder(w).Encode(map[string]string{"status": "400"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	fetcher := &Fetcher{
		adamHallClient:   server.Client(),
		adamHallBaseURL:  server.URL,
		adamHallUsername: "buyer@example.com",
		adamHallPassword: "wrong-secret",
	}

	if _, err := fetcher.adamHallPrice(t.Context(), "8747X6"); err == nil || !strings.Contains(err.Error(), "Anmeldung") {
		t.Fatalf("expected a login error, got %v", err)
	}
}

func TestAdamHallLivePrice(t *testing.T) {
	if os.Getenv("ADAMHALL_LIVE_TEST") != "1" {
		t.Skip("set ADAMHALL_LIVE_TEST=1 to test the live account-price flow")
	}
	fetcher := New(Options{
		AdamHallUsername: os.Getenv("ADAMHALL_USERNAME"),
		AdamHallPassword: os.Getenv("ADAMHALL_PASSWORD"),
	})
	preview, err := fetcher.Scrape(t.Context(), "https://www.adamhall.com/shop/de/ready-made-cables/pdu-6-8747x6")
	if err != nil {
		t.Fatal(err)
	}
	if preview.SKU != "8747X6" || preview.PriceCents <= 0 || preview.Currency != "EUR" {
		t.Fatalf("unexpected live Adam Hall preview: %+v", preview)
	}
}

func TestSTEX24LiveSitemapFallback(t *testing.T) {
	if os.Getenv("STEX24_LIVE_TEST") != "1" {
		t.Skip("set STEX24_LIVE_TEST=1 to test the live sitemap fallback")
	}
	fetcher := New(Options{})
	preview, err := fetcher.Scrape(t.Context(), "https://stex24.com/de/136382-schrumpfschlauch-2zu1-wsb2-tr-160-80mm-5-8-zoll-136382")
	if err != nil {
		t.Fatal(err)
	}
	if preview.SKU != "136382" || preview.Attributes["Baugröße"] != "16,0/8,0 mm" || preview.Source != "STEX24 Sitemap (eingeschränkte Vorschau)" {
		t.Fatalf("unexpected live STEX24 preview: %+v", preview)
	}
}

func TestIsPublicIPBlocksInternalNetworks(t *testing.T) {
	blocked := []string{"127.0.0.1", "10.0.0.2", "172.16.0.1", "192.168.1.2", "169.254.169.254", "100.64.0.1", "::1"}
	for _, value := range blocked {
		if isPublicIP(net.ParseIP(value)) {
			t.Fatalf("%s must be blocked", value)
		}
	}
	if !isPublicIP(net.ParseIP("8.8.8.8")) {
		t.Fatal("public address was blocked")
	}
}
