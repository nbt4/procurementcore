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

func TestParseHTMLUsesSchemaMicrodata(t *testing.T) {
	source, _ := url.Parse("https://shop.example/product")
	page := `<div itemscope itemtype="https://schema.org/Product">
      <nav itemscope itemtype="https://schema.org/BreadcrumbList"><meta itemprop="name" content="Home"></nav>
      <h1 itemprop="name">Touring Cable</h1>
      <meta itemprop="image" content="/cable.jpg">
      <span itemprop="sku">TC-10</span>
      <div itemprop="brand" itemscope itemtype="https://schema.org/Brand"><meta itemprop="name" content="Roadline"></div>
      <div itemprop="offers" itemscope itemtype="https://schema.org/Offer">
        <meta itemprop="price" content="19.90"><span itemprop="priceCurrency">€</span>
      </div>
    </div>`
	preview, err := ParseHTML(strings.NewReader(page), source)
	if err != nil {
		t.Fatal(err)
	}
	if preview.Name != "Touring Cable" || preview.SKU != "TC-10" || preview.Manufacturer != "Roadline" {
		t.Fatalf("unexpected identity: %+v", preview)
	}
	if preview.PriceCents != 1990 || preview.Currency != "EUR" || preview.ImageURL != "https://shop.example/cable.jpg" {
		t.Fatalf("unexpected offer: %+v", preview)
	}
	if preview.Source != "schema.org Microdata" {
		t.Fatalf("unexpected source: %q", preview.Source)
	}
}

func TestDecodeHTMLConvertsLegacyShopCharset(t *testing.T) {
	decoded, err := decodeHTML([]byte{'G', 'r', 0xfc, 0xdf, 'e'}, "text/html; charset=ISO-8859-1")
	if err != nil {
		t.Fatal(err)
	}
	if string(decoded) != "Grüße" {
		t.Fatalf("unexpected decoded text: %q", decoded)
	}
}

func TestParseHTMLRecognizesLTTProduct(t *testing.T) {
	source, _ := url.Parse("https://www.ltt-versand.de/technik/kabel/29358/product")
	page := `<div class="product--details" itemscope itemtype="https://schema.org/Product">
      <h1 itemprop="name">Adam Hall Cables K4 IPP 0090</h1><meta itemprop="image" content="/cable.jpg">
      <div itemprop="brand" itemscope><meta itemprop="name" content="Adam Hall Cables"></div>
      <div itemprop="offers" itemscope><meta itemprop="price" content="7.56"><meta itemprop="priceCurrency" content="EUR"></div>
      <div class="product--base-info"><div class="ordernumber"><span itemprop="sku">500103380</span></div><div class="ean"><strong>EAN:</strong> 4049521119262</div><div class="suppliernumber"><strong>MPN:</strong> K4IPP0090</div></div>
      <div class="product--description ltt--text"><p>Robustes Kabel</p><table><tr><td>Kabellänge:</td><td>0,9 m</td></tr><tr><td>Anschluss 1:</td><td>6,3 mm Klinke</td></tr></table></div>
    </div>`
	preview, err := ParseHTML(strings.NewReader(page), source)
	if err != nil {
		t.Fatal(err)
	}
	if preview.Source != "LTT/HTML" || preview.SKU != "500103380" || preview.Model != "K4IPP0090" || preview.PriceCents != 756 {
		t.Fatalf("unexpected preview: %+v", preview)
	}
	if preview.Attributes["EAN"] != "4049521119262" || preview.Attributes["Kabellänge"] != "0,9 m" {
		t.Fatalf("unexpected attributes: %+v", preview.Attributes)
	}
}

func TestParseHTMLRecognizesHussProduct(t *testing.T) {
	source, _ := url.Parse("https://www.huss-licht-ton.de/product_info.php/info/57377.html")
	page := `<div class="product-wrapper" itemscope itemtype="http://schema.org/Product">
      <h1 itemprop="name">Adam Hall Adapterkabel - K4TPP0300</h1><meta itemprop="image" content="/57377.jpg">
      <span itemprop="description"><ul><li>Kabellänge: 3 m</li><li>Gewicht: 0.256 kg</li></ul></span>
      <div class="ad_merkmale_content_element_wrapper"><div class="ad_merkmale_content_element_left">Von Stecker A</div><div class="ad_merkmale_content_element_right">2x Klinke 6.3mm Mono</div></div>
      <div itemprop="offers" itemscope><span itemprop="price">17.6</span><span itemprop="priceCurrency">€</span></div>
      <div itemprop="sku">AHAK4TPP0300</div><div itemprop="brand">Adam Hall</div><span itemprop="gtin13">4049521119859</span>
    </div>`
	preview, err := ParseHTML(strings.NewReader(page), source)
	if err != nil {
		t.Fatal(err)
	}
	if preview.Source != "Huss Licht & Ton/HTML" || preview.SKU != "AHAK4TPP0300" || preview.Model != "K4TPP0300" || preview.PriceCents != 1760 {
		t.Fatalf("unexpected preview: %+v", preview)
	}
	if preview.Attributes["Kabellänge"] != "3 m" || preview.Attributes["Von Stecker A"] != "2x Klinke 6.3mm Mono" || preview.Attributes["EAN"] != "4049521119859" {
		t.Fatalf("unexpected attributes: %+v", preview.Attributes)
	}
}

func TestParseHTMLRecognizesThomannProduct(t *testing.T) {
	source, _ := url.Parse("https://www.thomann.de/de/pro_snake_cable.htm")
	page := `<div itemscope itemtype="https://schema.org/Product">
      <h1 itemprop="name">pro snake Speaker Cable Jack 10</h1><img itemprop="image" src="/137281.jpg">
      <div class="product-text" itemprop="description"><ul><li class="list-item__text">Farbe: Schwarz</li></ul>
        <ul><li class="keyfeature"><div><span class="keyfeature__label">Artikelnummer</span><span>137281</span></div></li><li class="keyfeature"><div><span class="keyfeature__label">Länge</span><span>10,00 m</span></div></li></ul>
      </div>
      <div itemprop="brand" itemscope><meta itemprop="name" content="pro snake"></div>
      <div itemprop="offers" itemscope><meta itemprop="price" content="40"><meta itemprop="priceCurrency" content="EUR"></div>
    </div>`
	preview, err := ParseHTML(strings.NewReader(page), source)
	if err != nil {
		t.Fatal(err)
	}
	if preview.Source != "Thomann/HTML" || preview.SKU != "137281" || preview.Manufacturer != "pro snake" || preview.PriceCents != 4000 {
		t.Fatalf("unexpected preview: %+v", preview)
	}
	if preview.Attributes["Länge"] != "10,00 m" || preview.Attributes["Farbe"] != "Schwarz" {
		t.Fatalf("unexpected attributes: %+v", preview.Attributes)
	}
}

func TestParseHTMLRecognizesInfrastructureShops(t *testing.T) {
	tests := []struct {
		name       string
		url        string
		html       string
		wantSource string
		wantSKU    string
		wantPrice  int64
		attribute  string
		value      string
	}{
		{
			name: "Steinigke", url: "https://www.steinigke.de/mpn30245695-product.html", wantSource: "Steinigke/JSON-LD+HTML", wantSKU: "30245695", wantPrice: 1450, attribute: "Schutzart", value: "IP44",
			html: `<script type="application/ld+json">{"@type":"Product","name":"EUROLITE Verlängerung","sku":"30245695","brand":{"name":"EUROLITE"},"offers":{"price":"14.50","priceCurrency":"EUR"}}</script><table class="technicalData"><tr><td>Schutzart:</td><td>IP44</td></tr></table>`,
		},
		{
			name: "Eurobox", url: "https://www.ab-in-die-box.de/b2bde/eurobox.html", wantSource: "ab-in-die-BOX/HTML", wantSKU: "IN64-32F-XX", wantPrice: 1840, attribute: "Material", value: "PP",
			html: `<script type="application/ld+json">{"@type":"Product","name":"falsches Zubehör","offers":{"price":"1.60","priceCurrency":"EUR"}}</script><span data-ui-id="page-title-wrapper">Eurobox 600x400x320mm</span><div class="product-description">Front offen</div><span class="product-detail-value-sku">IN64-32F-XX</span><div class="price-final_price"><span data-price-amount="18.404"></span></div><div class="aidb-product-attributes"><ul><li title="Material: PP"><div></div></li></ul></div>`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			source, _ := url.Parse(test.url)
			preview, err := ParseHTML(strings.NewReader(test.html), source)
			if err != nil {
				t.Fatal(err)
			}
			if preview.Source != test.wantSource || preview.SKU != test.wantSKU || preview.PriceCents != test.wantPrice || preview.Attributes[test.attribute] != test.value {
				t.Fatalf("unexpected preview: %+v", preview)
			}
		})
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

func TestEventTechnologyShopsLive(t *testing.T) {
	if os.Getenv("SHOP_SCRAPER_LIVE_TEST") != "1" {
		t.Skip("set SHOP_SCRAPER_LIVE_TEST=1 to test supported shops")
	}
	tests := []struct {
		name      string
		url       string
		wantName  string
		wantSKU   string
		wantPrice bool
	}{
		{"LTT", "https://www.ltt-versand.de/technik/kabel/instrumentenkabel/pedalboard-patchkabel/29358/adam-hall-cables-k4-ipp-0090-instrumentenkabel-rean-6-3-mm-klinke-mono-auf", "K4 IPP 0090", "500103380", true},
		{"Huss", "https://www.huss-licht-ton.de/product_info.php/Adam-Hall-4-Star-Adapterkabel-30m-Klinke-Mono/info/57377.html", "Adapterkabel", "AHAK4TPP0300", true},
		{"Huss connector", "https://www.huss-licht-ton.de/product_info.php/XLR-Stecker-Eco-Version-5-pol-Kabel-maennlich/info/664.html", "XLR Stecker", "XMK105NB", true},
		{"Thomann", "https://www.thomann.de/de/pro_snake_gitarrenlautsprecherkabel_10.htm", "Gitarren-Lautsprecherkabel", "137281", true},
		{"Thomann flightcase", "https://www.thomann.de/de/thon_turntable_flightcase.htm", "Flightcase", "363507", true},
		{"Steinigke", "https://www.steinigke.de/mpn30245695-eurolite-verlaengerung-3x15-3m.html", "EUROLITE", "30245695", true},
		{"Eurobox", "https://www.ab-in-die-box.de/b2bde/catalog/product/view/_ignore_category/1/id/2001/s/euroboxen-eurokisten-eurokaesten-nextgen-insight-front-offen-600x400x320", "Eurobox", "IN64-32F-XX", true},
		{"Caseman", "https://www.caseman-berlin.de/de/aluminium-profile/deckelrahmen/rahmenprofil-4mm-37x20x2-5-passend-zu-0315-1.html", "Rahmenprofil", "EG-0400-2M", true},
	}
	fetcher := New(Options{})
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			preview, err := fetcher.Scrape(t.Context(), test.url)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(preview.Name, test.wantName) || preview.SKU != test.wantSKU || (test.wantPrice && preview.PriceCents <= 0) {
				t.Fatalf("unexpected live preview: %+v", preview)
			}
			t.Logf("%s: %s, %s, %.2f %s, %d attributes", preview.Source, preview.Name, preview.SKU, float64(preview.PriceCents)/100, preview.Currency, len(preview.Attributes))
		})
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
