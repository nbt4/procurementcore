package orderimport

import (
	"math"
	"regexp"
	"strings"
	"time"
)

type OfferPreview struct {
	SourceFileName       string     `json:"sourceFileName"`
	PageCount            int        `json:"pageCount"`
	ExtractedCharacters  int        `json:"extractedCharacters"`
	OCRUsed              bool       `json:"ocrUsed"`
	SupplierID           uint       `json:"supplierId,omitempty"`
	SupplierName         string     `json:"supplierName,omitempty"`
	OfferNumber          string     `json:"offerNumber"`
	OfferDate            *time.Time `json:"offerDate,omitempty"`
	Currency             string     `json:"currency"`
	DocumentTotalCents   int64      `json:"documentTotalCents"`
	RecognizedTotalCents int64      `json:"recognizedTotalCents"`
	Warnings             []string   `json:"warnings"`
	Lines                []Line     `json:"lines"`
}

var offerNumberPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?im)(?:angebots(?:nummer|nr\.?|[- ]?nr\.?|[- ]?nummer)|quotation\s*(?:number|no\.?)|quote\s*(?:number|no\.?))\s*[:#]?\s*([[:alnum:]][[:alnum:]._/-]{1,})`),
}
var offerDatePatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?im)(?:angebotsdatum|quotation\s*date|quote\s*date|datum)\s*:?\s*(\d{1,2}[./-]\d{1,2}[./-]\d{2,4}|\d{4}-\d{1,2}-\d{1,2})`),
}
var supplierSKU = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]{2,79}$`)
var trailingQuantity = regexp.MustCompile(`(?i)\s+\d+(?:[.,]\d+)?\s*(?:x|stk\.?|st(?:ü|ue)ck|pcs\.?|pieces?|ea\.?)$`)

func AnalyzeOffer(filename, text string, pages int, ocrUsed bool, suppliers []SupplierHint, products []ProductHint) OfferPreview {
	text = cleanText(text)
	preview := OfferPreview{
		SourceFileName: safeFilename(filename), PageCount: pages,
		ExtractedCharacters: len([]rune(text)), OCRUsed: ocrUsed,
		Currency: detectCurrency(text), Warnings: []string{}, Lines: []Line{},
	}
	preview.SupplierID, preview.SupplierName = matchSupplier(text, suppliers)
	preview.OfferNumber = firstMatch(text, offerNumberPatterns)
	preview.OfferDate = firstDate(text, offerDatePatterns)
	preview.DocumentTotalCents = findDocumentTotal(text)
	preview.Lines = extractLines(text, preview.SupplierID, products)
	for i := range preview.Lines {
		line := &preview.Lines[i]
		if line.ProductID == nil {
			line.Description = strings.TrimSpace(trailingQuantity.ReplaceAllString(line.Description, ""))
			parts := strings.Fields(line.Description)
			if len(parts) > 1 && supplierSKU.MatchString(parts[0]) && strings.IndexFunc(parts[0], func(r rune) bool { return r >= '0' && r <= '9' }) >= 0 {
				line.SupplierSKU = parts[0]
				line.Description = strings.TrimSpace(strings.TrimPrefix(line.Description, parts[0]))
			}
		}
		preview.RecognizedTotalCents += int64(math.Round(line.Quantity * float64(line.UnitPriceCents)))
		if line.UnitPriceCents == 0 {
			preview.Warnings = append(preview.Warnings, "Mindestens eine Position hat keinen erkannten Preis.")
		}
	}
	if preview.SupplierID == 0 {
		preview.Warnings = append(preview.Warnings, "Lieferant bitte auswählen.")
	}
	if len(preview.Lines) == 0 {
		preview.Warnings = append(preview.Warnings, "Keine Angebotsposition erkannt; bitte Positionen manuell ergänzen.")
	}
	if preview.DocumentTotalCents > 0 && preview.RecognizedTotalCents > 0 && abs64(preview.DocumentTotalCents-preview.RecognizedTotalCents) > 2 {
		preview.Warnings = append(preview.Warnings, "Positionssumme weicht vom Angebotsbetrag ab; Preisbasis und Steuer prüfen.")
	}
	if ocrUsed {
		preview.Warnings = append(preview.Warnings, "Gescanntes PDF per OCR gelesen; alle Werte bitte besonders sorgfältig prüfen.")
	}
	return preview
}
