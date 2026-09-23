package scraper

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"procurementcore/internal/jev"
)

func TestJevSelectsMainProductFromConflictingPageEvidence(t *testing.T) {
	page := `<html><head><title>Main Product | Shop</title>
	<script type="application/ld+json">{"@graph":[
	{"@type":"Product","name":"Replacement Case","sku":"CASE-1","offers":{"price":"9.90"}},
	{"@type":"Product","name":"Main Product","sku":"MAIN-2","brand":{"name":"Maker"},"offers":{"price":"49.90"}}
	]}</script></head><body><h1>Main Product</h1></body></html>`
	source, _ := url.Parse("https://shop.example/products/main-product?tracking=secret")
	fallback, err := ParseHTML(strings.NewReader(page), source)
	if err != nil {
		t.Fatal(err)
	}
	if fallback.SKU != "CASE-1" {
		t.Fatalf("fixture must expose the first-product error, got %+v", fallback)
	}

	tests := []struct {
		name       string
		confidence float64
		status     int
		choice     string
		wantSKU    string
	}{
		{name: "confident main product", confidence: 0.94, status: http.StatusOK, wantSKU: "MAIN-2"},
		{name: "low confidence retains parser", confidence: 0.55, status: http.StatusOK, wantSKU: "CASE-1"},
		{name: "unknown choice retains parser", confidence: 0.94, status: http.StatusOK, choice: "invented", wantSKU: "CASE-1"},
		{name: "upstream failure retains parser", status: http.StatusBadGateway, wantSKU: "CASE-1"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var request struct {
					State     map[string]string `json:"state"`
					Questions map[string]struct {
						Criteria map[string]string `json:"criteria"`
					} `json:"questions"`
				}
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					t.Error(err)
				}
				if strings.Contains(fmt.Sprint(request.State), "tracking") || strings.Contains(fmt.Sprint(request.State), "secret") {
					t.Errorf("query string leaked into Jev state: %+v", request.State)
				}
				for _, summary := range request.Questions["match"].Criteria {
					if strings.Contains(summary, "price=") {
						t.Errorf("price leaked into Jev criteria: %s", summary)
					}
				}
				choice := test.choice
				if choice == "" {
					for key, summary := range request.Questions["match"].Criteria {
						if strings.Contains(summary, `sku="MAIN-2"`) {
							choice = key
						}
					}
				}
				if choice == "" {
					t.Error("main product not offered as a candidate")
				}
				w.WriteHeader(test.status)
				_ = json.NewEncoder(w).Encode(map[string]any{"model": "typesafe/jev-1.13", "answers": map[string]any{"match": map[string]any{"type": "choice", "choice": choice, "confidence": test.confidence}}})
			}))
			defer server.Close()
			client, err := jev.New(jev.Config{APIKey: "test", Endpoint: server.URL, Timeout: time.Second})
			if err != nil {
				t.Fatal(err)
			}
			fetcher := &Fetcher{jev: client}
			preview := fetcher.enrichProductPreview(context.Background(), []byte(page), source, fallback)
			if preview.SKU != test.wantSKU {
				t.Fatalf("SKU = %q, want %q; preview: %+v", preview.SKU, test.wantSKU, preview)
			}
			if test.wantSKU == "MAIN-2" && (preview.PriceCents != 0 || preview.Manufacturer != "Maker" || preview.Source != "JSON-LD + Jev") {
				t.Fatalf("Jev selection changed product evidence: %+v", preview)
			}
		})
	}
}

func TestParseHTMLUsesVisibleHeadingWithoutStructuredData(t *testing.T) {
	source, _ := url.Parse("https://shop.example/product")
	preview, err := ParseHTML(strings.NewReader(`<html><head><title>Shop | Offers</title></head><body><h1>Stage Cable 10 m</h1></body></html>`), source)
	if err != nil {
		t.Fatal(err)
	}
	if preview.Name != "Stage Cable 10 m" {
		t.Fatalf("visible product heading was ignored: %+v", preview)
	}
}
