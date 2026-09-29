package orderimport

import (
	"context"
	"os"
	"strings"
	"testing"
)

func TestExtractTextWithOCRFromFixture(t *testing.T) {
	path := os.Getenv("OFFER_OCR_PDF_TEST_FILE")
	if path == "" {
		t.Skip("set OFFER_OCR_PDF_TEST_FILE to a scanned test PDF")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text, pages, ocrUsed, err := ExtractTextWithOCR(context.Background(), data)
	if err != nil {
		t.Fatal(err)
	}
	if pages != 1 || !ocrUsed || !strings.Contains(strings.ToLower(text), "adam hall") {
		t.Fatalf("OCR result: pages=%d used=%v text=%q", pages, ocrUsed, text)
	}
}

func TestAnalyzeOfferKeepsUnknownAdamHallProductEditable(t *testing.T) {
	text := `Adam Hall GmbH
Angebotsnummer: AH-2026-4711
Angebotsdatum: 29.09.2026
Pos Artikel Beschreibung Menge Einzelpreis Gesamt
1 8747X3 Patchkabel 5 Stk. 2,50 EUR 12,50 EUR
2 K4TPP0300 Adapterkabel 2 Stk. 17,60 EUR 35,20 EUR
Gesamtsumme 47,70 EUR`
	preview := AnalyzeOffer("Angebot.pdf", text, 1, false,
		[]SupplierHint{{ID: 7, Name: "Adam Hall GmbH"}},
		[]ProductHint{{ID: 42, SKU: "8747X3", Name: "Patchkabel", Unit: "Stk."}},
	)
	if preview.SupplierID != 7 || preview.OfferNumber != "AH-2026-4711" || preview.OfferDate == nil {
		t.Fatalf("metadata: %+v", preview)
	}
	if len(preview.Lines) != 2 || preview.Lines[0].ProductID == nil || *preview.Lines[0].ProductID != 42 {
		t.Fatalf("lines: %+v", preview.Lines)
	}
	if preview.Lines[1].ProductID != nil || preview.Lines[1].SupplierSKU != "K4TPP0300" || preview.Lines[1].Description != "Adapterkabel" || preview.Lines[1].Quantity != 2 || preview.Lines[1].UnitPriceCents != 1760 {
		t.Fatalf("new product proposal: %+v", preview.Lines[1])
	}
	if preview.DocumentTotalCents != 4770 || preview.RecognizedTotalCents != 4770 {
		t.Fatalf("totals: %+v", preview)
	}
}

func TestAnalyzeOfferDoesNotInventLineFromTotal(t *testing.T) {
	preview := AnalyzeOffer("offer.pdf", "Quote no: X-12\nGrand total 100,00 EUR", 1, false, nil, nil)
	if len(preview.Lines) != 0 || len(preview.Warnings) == 0 {
		t.Fatalf("unexpected fallback: %+v", preview)
	}
}
