package api

import (
	"encoding/json"
	"net/http"
	"net/url"
	"reflect"
	"testing"

	"procurementcore/internal/models"
)

func TestValidateWarehouseReceipt(t *testing.T) {
	tests := []struct {
		name         string
		trackingMode string
		quantity     float64
		wantStatus   int
		wantCode     string
	}{
		{name: "quantity accepts fractions", trackingMode: "quantity", quantity: 2.5},
		{name: "individual accepts whole devices", trackingMode: "individual", quantity: 8},
		{name: "untracked accepts receipt", trackingMode: "none", quantity: 3},
		{name: "individual rejects fractions", trackingMode: "individual", quantity: 1.5, wantStatus: http.StatusBadRequest, wantCode: "individual_quantity_required"},
		{name: "unknown mode is blocked", trackingMode: "legacy", quantity: 1, wantStatus: http.StatusConflict, wantCode: "warehouse_tracking_invalid"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := validateWarehouseReceipt(test.trackingMode, test.quantity)
			if test.wantCode == "" {
				if err != nil {
					t.Fatalf("validateWarehouseReceipt() error = %v", err)
				}
				return
			}
			if err == nil || err.status != test.wantStatus || err.code != test.wantCode {
				t.Fatalf("validateWarehouseReceipt() error = %#v, want status=%d code=%q", err, test.wantStatus, test.wantCode)
			}
		})
	}
}

func TestNormalizeSupplierOrderNumber(t *testing.T) {
	if got := normalizeSupplierOrderNumber("  AB-4711 / 26  "); got != "AB-4711 / 26" {
		t.Fatalf("normalizeSupplierOrderNumber() = %q", got)
	}
}

func TestIsAdamHallSupplier(t *testing.T) {
	tests := []struct {
		name     string
		supplier models.Supplier
		want     bool
	}{
		{name: "name", supplier: models.Supplier{Name: "Adam Hall GmbH"}, want: true},
		{name: "code", supplier: models.Supplier{Code: "ADAM-HALL"}, want: true},
		{name: "website", supplier: models.Supplier{Website: "https://www.adamhall.com/shop/de/"}, want: true},
		{name: "other supplier", supplier: models.Supplier{Name: "Thomann", Website: "https://thomann.de"}},
		{name: "lookalike host", supplier: models.Supplier{Website: "https://adamhall.com.example.org"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := isAdamHallSupplier(test.supplier); got != test.want {
				t.Fatalf("isAdamHallSupplier() = %v, want %v", got, test.want)
			}
		})
	}
}

func TestParseProductFilter(t *testing.T) {
	values := url.Values{
		"q": {"  cable  "}, "categoryId": {"12"}, "supplierId": {"7"}, "preferred": {"true"},
		"alertsOnly": {"true"}, "minPriceCents": {"100"}, "maxPriceCents": {"5000"},
		"param": {"farbe:Schwarz", "laenge:10", "broken"},
	}
	got := ParseProductFilter(values)
	if got.Query != "cable" || got.CategoryID != 12 || got.SupplierID != 7 || !got.PreferredOnly || !got.AlertsOnly {
		t.Fatalf("unexpected base filter: %+v", got)
	}
	if got.MinPrice == nil || *got.MinPrice != 100 || got.MaxPrice == nil || *got.MaxPrice != 5000 {
		t.Fatalf("unexpected price filter: %+v", got)
	}
	if got.Parameters["farbe"] != "Schwarz" || got.Parameters["laenge"] != "10" || len(got.Parameters) != 2 {
		t.Fatalf("unexpected parameters: %#v", got.Parameters)
	}
}

func TestParseProductFilterIgnoresInvalidNumbers(t *testing.T) {
	got := ParseProductFilter(url.Values{"categoryId": {"nope"}, "minPriceCents": {"x"}, "param": {" :x"}})
	if got.CategoryID != 0 || got.MinPrice != nil || len(got.Parameters) != 0 {
		t.Fatalf("invalid values should be ignored: %+v", got)
	}
}

func TestProductSearchTerms(t *testing.T) {
	got := productSearchTerms(" LD Systems  Stinger SUB 18A G3 ")
	want := []string{"ld", "systems", "stinger", "sub", "18a", "g3"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("productSearchTerms() = %#v, want %#v", got, want)
	}
}

func TestValidateProductDefaultsIndependentAttributes(t *testing.T) {
	product := models.Product{SKU: " test-1 ", Name: " Test product "}
	if message := validateProduct(&product); message != "" {
		t.Fatal(message)
	}
	if product.SKU != "TEST-1" || product.Name != "Test product" {
		t.Fatalf("product identity was not normalized: %+v", product)
	}
	if string(product.Parameters) != "{}" || string(product.Attributes) != "{}" {
		t.Fatalf("JSON fields were not initialized: parameters=%s attributes=%s", product.Parameters, product.Attributes)
	}
}

func TestValidateProductRejectsInvalidIndependentAttributes(t *testing.T) {
	product := models.Product{
		SKU:        "TEST-1",
		Name:       "Test product",
		Parameters: json.RawMessage(`{}`),
		Attributes: json.RawMessage(`{"EAN":`),
	}
	if message := validateProduct(&product); message != "Attribute sind kein gültiges JSON" {
		t.Fatalf("unexpected validation result: %q", message)
	}
}
