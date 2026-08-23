package api

import (
	"net/url"
	"testing"
)

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
