package scraper

import (
	"net"
	"net/url"
	"strings"
	"testing"
)

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
