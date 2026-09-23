package scraper

import (
	"net/url"
	"strings"
	"testing"
)

func TestParseHTMLExtractsGenericTechnicalLayouts(t *testing.T) {
	source, _ := url.Parse("https://manufacturer.example/products/fixture")
	page := `<html><head><script type="application/ld+json">{"@type":"Product","name":"Stage Fixture","sku":"CL2025","additionalProperty":[{"name":"CRI","value":"90"}]}</script></head>
	<body><nav><ul><li><strong>Contact:</strong> Sales team</li></ul></nav>
	<main><h1>Stage Fixture</h1>
	<section class="technical-specifications"><ul>
	<li><strong>Source type:</strong> 27 LEDs RGBWW x 40W</li>
	<li><strong>CRI:</strong> Up to 80</li>
	<li>Operating Voltage: 100–240V, 50/60 Hz</li>
	<li><strong>Beam angle:</strong><ul><li>7,1° : 50%</li><li>13,7° : 10%</li></ul></li>
	</ul></section>
	<table><tr><th>Weight</th><td>10,44 kg</td></tr></table>
	<dl><dt>Ingress protection</dt><dd>IP66</dd></dl>
	<p><strong>DMX channels:</strong> 10 / 32 / 26</p>
	<div class="product-details"><div class="attribute-row"><span class="attribute-label">Power consumption</span><span class="attribute-value">700 W</span></div></div>
	</main><footer><ul><li><strong>Newsletter:</strong> Subscribe</li></ul></footer></body></html>`
	preview, err := ParseHTML(strings.NewReader(page), source)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"Source type":        "27 LEDs RGBWW x 40W",
		"CRI":                "90",
		"Operating Voltage":  "100–240V, 50/60 Hz",
		"Beam angle":         "7,1° : 50% 13,7° : 10%",
		"Weight":             "10,44 kg",
		"Ingress protection": "IP66",
		"DMX channels":       "10 / 32 / 26",
		"Power consumption":  "700 W",
	}
	for name, value := range want {
		if preview.Attributes[name] != value {
			t.Errorf("attribute %q = %q, want %q", name, preview.Attributes[name], value)
		}
	}
	if _, exists := preview.Attributes["Contact"]; exists {
		t.Fatal("navigation was imported as a product attribute")
	}
	if _, exists := preview.Attributes["Newsletter"]; exists {
		t.Fatal("footer was imported as a product attribute")
	}
	if preview.Source != "JSON-LD + HTML specs" {
		t.Fatalf("source = %q", preview.Source)
	}
}

func TestParseHTMLHandlesClaypakyStyleSpecsWithoutShopAdapter(t *testing.T) {
	source, _ := url.Parse("https://example.org/products/tambora-rays/")
	page := `<script type="application/ld+json">{"@type":"Product","name":"Tambora Rays","sku":"CL2025","brand":{"name":"Claypaky"}}</script>
	<h1>Tambora Rays</h1><p class="datasheet-title">LIGHT SOURCE</p>
	<ul class="datasheet-list"><li><strong>Source type: </strong>27 LEDs RGBW W x 40W</li><li><strong>CRI: </strong>Up to 80</li></ul>
	<p class="datasheet-title">ELECTRICAL</p><ul class="datasheet-list"><li><strong>Max Power consumption:</strong><ul><li>720 W @100V 7,2A</li><li>700 W @240V 3,2A</li></ul></li></ul>`
	preview, err := ParseHTML(strings.NewReader(page), source)
	if err != nil {
		t.Fatal(err)
	}
	if preview.Name != "Tambora Rays" || preview.SKU != "CL2025" || preview.Manufacturer != "Claypaky" {
		t.Fatalf("product identity changed: %+v", preview)
	}
	if preview.Attributes["Source type"] != "27 LEDs RGBW W x 40W" || preview.Attributes["CRI"] != "Up to 80" || preview.Attributes["Max Power consumption"] != "720 W @100V 7,2A 700 W @240V 3,2A" {
		t.Fatalf("technical attributes missing: %+v", preview.Attributes)
	}
}
