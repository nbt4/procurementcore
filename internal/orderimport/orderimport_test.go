package orderimport

import (
	"os"
	"strings"
	"testing"
	"time"
)

func TestExtractTextFromFixture(t *testing.T) {
	path := os.Getenv("ORDER_IMPORT_PDF_TEST_FILE")
	if path == "" {
		t.Skip("set ORDER_IMPORT_PDF_TEST_FILE to exercise a real PDF")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text, pages, err := ExtractText(data)
	if err != nil {
		t.Fatal(err)
	}
	if pages != 1 || !strings.Contains(text, "Bestellnummer") || !strings.Contains(text, "8747X3") {
		t.Fatalf("unexpected extraction: pages=%d text=%q", pages, text)
	}
	preview := Analyze("fixture.pdf", text, pages,
		[]SupplierHint{{ID: 1, Name: "Adam Hall GmbH"}},
		[]ProductHint{{ID: 42, SKU: "8747X3", Name: "Adam Hall Patchkabel", Unit: "Stk."}},
	)
	if strings.Contains(preview.SupplierOrderNumber, "Bestelldatum") {
		t.Fatalf("visual rows were not preserved: %q", text)
	}
	if len(preview.Lines) != 1 || preview.Lines[0].UnitPriceCents != 250 {
		t.Fatalf("unexpected real PDF lines: %+v (text %q)", preview.Lines, text)
	}
}

func TestAnalyzeMatchesOrderMetadataAndCatalogProduct(t *testing.T) {
	text := `Adam Hall GmbH
Bestellnummer: AH-9981
Bestelldatum: 09.09.2026
Lieferdatum: 15.09.2026
Pos Artikel Menge Einzelpreis Gesamt
1 8747X3 Patchkabel 5 Stk. 2,50 EUR 12,50 EUR
Gesamtsumme netto 12,50 EUR`
	productID := uint(42)
	preview := Analyze(`C:\Uploads\Bestellung.pdf`, text, 2,
		[]SupplierHint{{ID: 7, Name: "Adam Hall GmbH", Website: "https://www.adamhall.com"}},
		[]ProductHint{{
			ID: productID, SKU: "8747X3", Name: "Adam Hall Patchkabel", Unit: "Stk.",
			Offers: []OfferHint{{SupplierID: 7, SupplierSKU: "8747X3", PurchaseURL: "https://www.adamhall.com/item"}},
		}},
	)

	if preview.SourceFileName != "Bestellung.pdf" || preview.SupplierID != 7 || preview.SupplierName != "Adam Hall GmbH" {
		t.Fatalf("unexpected source or supplier: %+v", preview)
	}
	if preview.SupplierOrderNumber != "AH-9981" || preview.OrderDate == nil || preview.ExpectedDelivery == nil {
		t.Fatalf("missing metadata: %+v", preview)
	}
	if got := preview.OrderDate.Format(time.DateOnly); got != "2026-09-09" {
		t.Fatalf("order date = %s", got)
	}
	if len(preview.Lines) != 1 || preview.Lines[0].ProductID == nil || *preview.Lines[0].ProductID != productID {
		t.Fatalf("unexpected lines: %+v", preview.Lines)
	}
	line := preview.Lines[0]
	if line.Quantity != 5 || line.UnitPriceCents != 250 || line.PurchaseURL == "" {
		t.Fatalf("unexpected linked line: %+v", line)
	}
	if preview.DocumentTotalCents != 1250 || preview.RecognizedTotalCents != 1250 || preview.Confidence != 100 || len(preview.Warnings) != 0 {
		t.Fatalf("unexpected totals or confidence: %+v", preview)
	}
}

func TestAnalyzeFallsBackToEditableFreeTextLine(t *testing.T) {
	preview := Analyze("invoice.pdf", "Invoice number: INV-77\nGrand total 1,234.56 USD", 1, nil, nil)

	if preview.Currency != "USD" || preview.DocumentTotalCents != 123456 {
		t.Fatalf("unexpected currency or total: %+v", preview)
	}
	if len(preview.Lines) != 1 || preview.Lines[0].Description != "Bestellung laut invoice.pdf" || preview.Lines[0].UnitPriceCents != 123456 {
		t.Fatalf("unexpected fallback line: %+v", preview.Lines)
	}
	if len(preview.Warnings) < 2 {
		t.Fatalf("expected review warnings, got: %#v", preview.Warnings)
	}
}

func TestAnalyzeRecognizesGenericLine(t *testing.T) {
	preview := Analyze("order.pdf", "Order no: WEB-42\n1 2 Stk. Kabelbinder schwarz 3,50 7,00 EUR\nGesamtsumme 7,00 EUR", 1, nil, nil)

	if len(preview.Lines) != 1 {
		t.Fatalf("unexpected lines: %+v", preview.Lines)
	}
	line := preview.Lines[0]
	if line.Description != "Kabelbinder schwarz" || line.Quantity != 2 || line.UnitPriceCents != 350 {
		t.Fatalf("unexpected generic line: %+v", line)
	}
}

func TestExtractTextRejectsNonPDF(t *testing.T) {
	_, _, err := ExtractText([]byte("not a pdf"))
	if err == nil || !strings.Contains(err.Error(), "kein gültiges PDF") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestParseNumberSupportsGermanAndEnglishAmounts(t *testing.T) {
	for value, want := range map[string]float64{"1.234,56": 1234.56, "1,234.56": 1234.56, "12,50": 12.5} {
		got, ok := parseNumber(value)
		if !ok || got != want {
			t.Errorf("parseNumber(%q) = %v, %v; want %v", value, got, ok, want)
		}
	}
}
