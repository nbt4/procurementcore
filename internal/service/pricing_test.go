package service

import (
	"testing"

	"procurementcore/internal/models"
)

func TestRequisitionTotal(t *testing.T) {
	tests := []struct {
		name  string
		lines []models.RequisitionLine
		want  int64
	}{
		{name: "empty", want: 0},
		{name: "whole quantities", lines: []models.RequisitionLine{{Quantity: 3, EstimatedPriceCents: 1299}, {Quantity: 1, EstimatedPriceCents: 500}}, want: 4397},
		{name: "fractional quantity", lines: []models.RequisitionLine{{Quantity: 2.5, EstimatedPriceCents: 400}}, want: 1000},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := RequisitionTotal(tc.lines); got != tc.want {
				t.Fatalf("RequisitionTotal() = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestPurchaseOrderTotal(t *testing.T) {
	lines := []models.PurchaseOrderLine{{Quantity: 4, UnitPriceCents: 250}, {Quantity: 0.5, UnitPriceCents: 1000}}
	if got := PurchaseOrderTotal(lines); got != 1500 {
		t.Fatalf("PurchaseOrderTotal() = %d, want 1500", got)
	}
}
