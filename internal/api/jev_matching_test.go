package api

import (
	"testing"

	"procurementcore/internal/orderimport"
)

func TestOrderLineCandidatesPreferSupplierSKU(t *testing.T) {
	products := []orderimport.ProductHint{
		{ID: 1, SKU: "GENERIC", Name: "XLR cable"},
		{ID: 2, SKU: "CABLE-10", Name: "Audio cable", Offers: []orderimport.OfferHint{{SupplierID: 7, SupplierSKU: "KLOTZ-M1K1FM0100"}}},
	}

	got := orderLineCandidates("Klotz M1K1FM0100 10 m", 7, products, 10)
	if len(got) != 2 || got[0].ID != 2 {
		t.Fatalf("orderLineCandidates() = %#v, want supplier SKU candidate first", got)
	}
}

func TestMergeSelectedCandidateMovesSelectionToFrontWithoutDuplicate(t *testing.T) {
	selected := warehouseProductCandidate{ProductID: 2, Name: "Selected"}
	existing := []warehouseProductCandidate{
		{ProductID: 1, Name: "First"},
		{ProductID: 2, Name: "Selected"},
		{ProductID: 3, Name: "Third"},
	}

	got := mergeSelectedCandidate(selected, existing, 3)
	if len(got) != 3 || got[0].ProductID != 2 || got[1].ProductID != 1 || got[2].ProductID != 3 {
		t.Fatalf("mergeSelectedCandidate() = %#v", got)
	}
}

func TestJevMinimumConfidence(t *testing.T) {
	t.Setenv("JEV_MIN_CONFIDENCE", "0.82")
	if got := jevMinimumConfidence(); got != 0.82 {
		t.Fatalf("jevMinimumConfidence() = %v, want 0.82", got)
	}

	t.Setenv("JEV_MIN_CONFIDENCE", "invalid")
	if got := jevMinimumConfidence(); got != 0.70 {
		t.Fatalf("jevMinimumConfidence() = %v, want fallback 0.70", got)
	}
}
