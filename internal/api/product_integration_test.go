package api

import (
	"encoding/json"
	"testing"

	"procurementcore/internal/models"
)

func TestScoreWarehouseCandidateUsesStableIdentifiers(t *testing.T) {
	product := models.Product{SKU: "ABC-42", Name: "Acme Road One", Manufacturer: "ACME", Model: "Road-One", Attributes: json.RawMessage(`{"ean":"4006380133936"}`)}
	candidate := warehouseProductCandidate{ManufacturerSKU: "ABC42", Name: "Acme Road One", Manufacturer: "Acme", Model: "Road One", EAN: "40 06380-133936"}

	got := scoreWarehouseCandidate(product, candidate)
	if got.Score != 320 {
		t.Fatalf("scoreWarehouseCandidate() score = %d, want 320 (reasons: %v)", got.Score, got.Reasons)
	}
}

func TestRankedWarehouseCandidatesDropsWeakMatches(t *testing.T) {
	product := models.Product{Name: "Notebook Netzteil", Manufacturer: "Acme"}
	rows := rankedWarehouseCandidates(product, []warehouseProductCandidate{{ProductID: 1, Name: "Notebook Netzteil", Manufacturer: "Acme"}, {ProductID: 2, Name: "Funkmikrofon"}})
	if len(rows) != 1 || rows[0].ProductID != 1 {
		t.Fatalf("rankedWarehouseCandidates() = %#v", rows)
	}
}
