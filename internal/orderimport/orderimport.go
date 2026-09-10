package orderimport

import (
	"bytes"
	"errors"
	"fmt"
	"math"
	"net/url"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/ledongthuc/pdf"
)

const (
	MaxPDFBytes      = 12 << 20
	maxPDFPages      = 100
	maxExtractedText = 2 << 20
	maxOrderLines    = 100
)

type SupplierHint struct {
	ID      uint
	Name    string
	Code    string
	Website string
	Email   string
}

type OfferHint struct {
	SupplierID  uint
	SupplierSKU string
	PurchaseURL string
}

type ProductHint struct {
	ID     uint
	SKU    string
	Name   string
	Unit   string
	Offers []OfferHint
}

type Line struct {
	ProductID        *uint   `json:"productId,omitempty"`
	Description      string  `json:"description"`
	Quantity         float64 `json:"quantity"`
	ReceivedQuantity float64 `json:"receivedQuantity"`
	Unit             string  `json:"unit"`
	UnitPriceCents   int64   `json:"unitPriceCents"`
	PurchaseURL      string  `json:"purchaseUrl"`
}

type Preview struct {
	SourceFileName       string     `json:"sourceFileName"`
	PageCount            int        `json:"pageCount"`
	ExtractedCharacters  int        `json:"extractedCharacters"`
	SupplierID           uint       `json:"supplierId,omitempty"`
	SupplierName         string     `json:"supplierName,omitempty"`
	SupplierOrderNumber  string     `json:"supplierOrderNumber"`
	OrderDate            *time.Time `json:"orderDate,omitempty"`
	ExpectedDelivery     *time.Time `json:"expectedDelivery,omitempty"`
	Currency             string     `json:"currency"`
	DocumentTotalCents   int64      `json:"documentTotalCents"`
	RecognizedTotalCents int64      `json:"recognizedTotalCents"`
	Confidence           int        `json:"confidence"`
	Warnings             []string   `json:"warnings"`
	Lines                []Line     `json:"lines"`
}

var (
	amountPattern          = regexp.MustCompile(`[+-]?(?:\d{1,3}(?:[.,\x{00a0} ]\d{3})+|\d+)[.,]\d{2}`)
	quantityPattern        = regexp.MustCompile(`(?i)(\d+(?:[.,]\d+)?)\s*(?:x|stk\.?|st(?:ü|ue)ck|pcs\.?|pieces?|ea\.?)(?:\s|$)`)
	leadingLinePattern     = regexp.MustCompile(`(?i)^\s*(?:\d{1,4}[.)\-]?\s+)?(?:\d+(?:[.,]\d+)?\s*(?:x|stk\.?|st(?:ü|ue)ck|pcs\.?|pieces?|ea\.?)\s+)?`)
	documentNumberPatterns = []*regexp.Regexp{
		regexp.MustCompile(`(?im)(?:bestell(?:nummer|nr\.?)|auftrags(?:nummer|nr\.?)|order\s*(?:number|no\.?))\s*[:#]?\s*([[:alnum:]][[:alnum:]._/\-]{2,})`),
		regexp.MustCompile(`(?im)(?:rechnungsnummer|rechnung\s*nr\.?|invoice\s*(?:number|no\.?))\s*[:#]?\s*([[:alnum:]][[:alnum:]._/\-]{2,})`),
	}
	orderDatePatterns = []*regexp.Regexp{
		regexp.MustCompile(`(?im)(?:bestelldatum|auftragsdatum|order\s*date|datum)\s*:?\s*(\d{1,2}[./-]\d{1,2}[./-]\d{2,4}|\d{4}-\d{1,2}-\d{1,2})`),
	}
	deliveryDatePatterns = []*regexp.Regexp{
		regexp.MustCompile(`(?im)(?:lieferdatum|liefertermin|voraussichtliche\s+lieferung|delivery\s*date)\s*:?\s*(\d{1,2}[./-]\d{1,2}[./-]\d{2,4}|\d{4}-\d{1,2}-\d{1,2})`),
	}
	summaryPattern = regexp.MustCompile(`(?i)(gesamt|summe|subtotal|total|netto|brutto|mwst|ust\.?|vat|versand|fracht|rabatt|discount)`)
	totalPattern   = regexp.MustCompile(`(?i)(gesamt(?:betrag|summe)?|grand\s+total|total\s+(?:eur|net|gross)|zu\s+zahlen)`)
)

func ExtractText(data []byte) (text string, pages int, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			text, pages, err = "", 0, errors.New("PDF konnte nicht sicher gelesen werden")
		}
	}()
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) < 5 || !bytes.HasPrefix(trimmed, []byte("%PDF-")) {
		return "", 0, errors.New("Datei ist kein gültiges PDF")
	}
	reader, err := pdf.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return "", 0, fmt.Errorf("PDF konnte nicht gelesen werden: %w", err)
	}
	pages = reader.NumPage()
	if pages < 1 || pages > maxPDFPages {
		return "", 0, fmt.Errorf("PDF muss zwischen 1 und %d Seiten enthalten", maxPDFPages)
	}
	var extracted strings.Builder
	for pageNumber := 1; pageNumber <= pages && extracted.Len() < maxExtractedText; pageNumber++ {
		page := reader.Page(pageNumber)
		if page.V.IsNull() {
			continue
		}
		pageText := pageLayoutText(page)
		if pageText == "" {
			pageText, _ = page.GetPlainText(nil)
		}
		if pageText != "" {
			appendLimited(&extracted, pageText)
			appendLimited(&extracted, "\n")
		}
	}
	text = strings.TrimSpace(extracted.String())
	if len([]rune(text)) < 20 {
		return "", pages, errors.New("PDF enthält keinen ausreichend maschinenlesbaren Text; bitte eine PDF mit Textebene verwenden")
	}
	return text, pages, nil
}

func appendLimited(target *strings.Builder, value string) {
	remaining := maxExtractedText - target.Len()
	if remaining <= 0 {
		return
	}
	if len(value) > remaining {
		value = value[:remaining]
	}
	target.WriteString(value)
}

type layoutRow struct {
	y     float64
	parts []pdf.Text
}

func pageLayoutText(page pdf.Page) string {
	parts := append([]pdf.Text(nil), page.Content().Text...)
	if len(parts) == 0 {
		return ""
	}
	sort.SliceStable(parts, func(i, j int) bool {
		if parts[i].Y != parts[j].Y {
			return parts[i].Y > parts[j].Y
		}
		return parts[i].X < parts[j].X
	})
	rows := make([]layoutRow, 0)
	for _, part := range parts {
		if part.S == "" {
			continue
		}
		tolerance := math.Max(2, part.FontSize*0.35)
		if len(rows) == 0 || math.Abs(rows[len(rows)-1].y-part.Y) > tolerance {
			rows = append(rows, layoutRow{y: part.Y, parts: []pdf.Text{part}})
			continue
		}
		rows[len(rows)-1].parts = append(rows[len(rows)-1].parts, part)
	}

	var result strings.Builder
	for _, row := range rows {
		sort.SliceStable(row.parts, func(i, j int) bool { return row.parts[i].X < row.parts[j].X })
		rowStarted := false
		lastWasSpace := false
		previousEnd := 0.0
		for _, part := range row.parts {
			value := part.S
			if strings.TrimSpace(value) == "" {
				if rowStarted && !lastWasSpace {
					appendLimited(&result, " ")
				}
				lastWasSpace = true
				previousEnd = math.Max(previousEnd, part.X+part.W)
				continue
			}
			if rowStarted && !lastWasSpace && part.X-previousEnd > math.Max(1, part.FontSize*0.2) {
				appendLimited(&result, " ")
			}
			appendLimited(&result, value)
			rowStarted = true
			lastWasSpace = strings.HasSuffix(value, " ")
			previousEnd = math.Max(previousEnd, part.X+part.W)
		}
		if rowStarted {
			appendLimited(&result, "\n")
		}
	}
	return strings.TrimSpace(result.String())
}

func Analyze(filename, text string, pageCount int, suppliers []SupplierHint, products []ProductHint) Preview {
	text = cleanText(text)
	preview := Preview{
		SourceFileName:      safeFilename(filename),
		PageCount:           pageCount,
		ExtractedCharacters: len([]rune(text)),
		Currency:            detectCurrency(text),
		Warnings:            []string{},
		Lines:               []Line{},
	}
	preview.SupplierID, preview.SupplierName = matchSupplier(text, suppliers)
	preview.SupplierOrderNumber = firstMatch(text, documentNumberPatterns)
	preview.OrderDate = firstDate(text, orderDatePatterns)
	preview.ExpectedDelivery = firstDate(text, deliveryDatePatterns)
	preview.DocumentTotalCents = findDocumentTotal(text)
	preview.Lines = extractLines(text, preview.SupplierID, products)
	for _, line := range preview.Lines {
		preview.RecognizedTotalCents += int64(math.Round(float64(line.UnitPriceCents) * line.Quantity))
	}
	if len(preview.Lines) == 0 && preview.DocumentTotalCents > 0 {
		preview.Lines = append(preview.Lines, Line{
			Description:    "Bestellung laut " + preview.SourceFileName,
			Quantity:       1,
			Unit:           "Stk.",
			UnitPriceCents: preview.DocumentTotalCents,
		})
		preview.RecognizedTotalCents = preview.DocumentTotalCents
		preview.Warnings = append(preview.Warnings, "Keine einzelnen Positionen erkannt; Dokumentgesamtbetrag als Freitextposition übernommen.")
	}
	if preview.SupplierID == 0 {
		preview.Warnings = append(preview.Warnings, "Lieferant konnte nicht eindeutig zugeordnet werden.")
	}
	if preview.SupplierOrderNumber == "" {
		preview.Warnings = append(preview.Warnings, "Lieferanten-Bestellnummer konnte nicht erkannt werden.")
	}
	if preview.OrderDate == nil {
		preview.Warnings = append(preview.Warnings, "Bestelldatum konnte nicht erkannt werden.")
	}
	if len(preview.Lines) == 0 {
		preview.Lines = []Line{{Description: "", Quantity: 1, Unit: "Stk."}}
		preview.Warnings = append(preview.Warnings, "Keine Bestellpositionen erkannt; Positionen bitte manuell ergänzen.")
	}
	zeroPrices := 0
	for _, line := range preview.Lines {
		if line.UnitPriceCents == 0 {
			zeroPrices++
		}
	}
	if zeroPrices > 0 {
		preview.Warnings = append(preview.Warnings, fmt.Sprintf("Bei %d Position(en) wurde kein Preis erkannt.", zeroPrices))
	}
	if preview.DocumentTotalCents > 0 && preview.RecognizedTotalCents > 0 && abs64(preview.DocumentTotalCents-preview.RecognizedTotalCents) > 2 {
		preview.Warnings = append(preview.Warnings, "Erkannte Positionssumme weicht vom Dokumentgesamtbetrag ab; Preise und Steuerbasis bitte prüfen.")
	}
	preview.Confidence = confidence(preview)
	return preview
}

func cleanText(value string) string {
	value = strings.ReplaceAll(value, "\x00", "")
	value = strings.ReplaceAll(value, "\r\n", "\n")
	value = strings.ReplaceAll(value, "\r", "\n")
	return strings.TrimSpace(value)
}

func safeFilename(value string) string {
	value = filepath.Base(strings.ReplaceAll(strings.TrimSpace(value), "\\", "/"))
	if value == "." || value == "" {
		return "Bestellung.pdf"
	}
	return value
}

func compact(value string) string {
	var result strings.Builder
	for _, char := range strings.ToLower(value) {
		if unicode.IsLetter(char) || unicode.IsDigit(char) {
			result.WriteRune(char)
		}
	}
	return result.String()
}

func matchSupplier(text string, suppliers []SupplierHint) (uint, string) {
	lower, compactText := strings.ToLower(text), compact(text)
	bestScore, bestID, bestName, tied := 0, uint(0), "", false
	for _, supplier := range suppliers {
		score := 0
		name := strings.TrimSpace(supplier.Name)
		if len([]rune(name)) >= 4 && strings.Contains(lower, strings.ToLower(name)) {
			score += 12
		} else if normalized := compact(name); len(normalized) >= 6 && strings.Contains(compactText, normalized) {
			score += 8
		}
		if code := compact(supplier.Code); len(code) >= 4 && strings.Contains(compactText, code) {
			score += 5
		}
		if emailParts := strings.Split(strings.ToLower(strings.TrimSpace(supplier.Email)), "@"); len(emailParts) == 2 && len(emailParts[1]) >= 5 && strings.Contains(lower, emailParts[1]) {
			score += 7
		}
		if parsed, err := url.Parse(supplier.Website); err == nil {
			host := strings.TrimPrefix(strings.ToLower(parsed.Hostname()), "www.")
			if len(host) >= 5 && strings.Contains(lower, host) {
				score += 7
			}
		}
		if score > bestScore {
			bestScore, bestID, bestName, tied = score, supplier.ID, supplier.Name, false
		} else if score > 0 && score == bestScore {
			tied = true
		}
	}
	if bestScore < 5 || tied {
		return 0, ""
	}
	return bestID, bestName
}

func firstMatch(text string, patterns []*regexp.Regexp) string {
	for _, pattern := range patterns {
		if match := pattern.FindStringSubmatch(text); len(match) > 1 {
			return strings.Trim(strings.TrimSpace(match[1]), ".,;:")
		}
	}
	return ""
}

func firstDate(text string, patterns []*regexp.Regexp) *time.Time {
	value := firstMatch(text, patterns)
	if value == "" {
		return nil
	}
	for _, layout := range []string{"02.01.2006", "2.1.2006", "02/01/2006", "2/1/2006", "02-01-2006", "2-1-2006", "2006-01-02", "02.01.06", "2.1.06"} {
		if parsed, err := time.Parse(layout, value); err == nil {
			date := time.Date(parsed.Year(), parsed.Month(), parsed.Day(), 12, 0, 0, 0, time.UTC)
			return &date
		}
	}
	return nil
}

func detectCurrency(text string) string {
	upper := strings.ToUpper(text)
	for _, currency := range []string{"CHF", "USD", "GBP"} {
		if strings.Contains(upper, currency) {
			return currency
		}
	}
	return "EUR"
}

func findDocumentTotal(text string) int64 {
	var result int64
	for _, rawLine := range strings.Split(text, "\n") {
		line := strings.TrimSpace(rawLine)
		if !totalPattern.MatchString(line) {
			continue
		}
		amounts := amounts(line)
		if len(amounts) > 0 {
			result = amounts[len(amounts)-1]
		}
	}
	return result
}

type productAlias struct {
	product ProductHint
	compact string
}

func extractLines(text string, supplierID uint, products []ProductHint) []Line {
	rawLines := strings.Split(text, "\n")
	lines := make([]string, 0, len(rawLines))
	for _, rawLine := range rawLines {
		if line := strings.Join(strings.Fields(rawLine), " "); line != "" {
			lines = append(lines, line)
		}
	}
	aliases := make([]productAlias, 0, len(products)*2)
	for _, product := range products {
		for _, alias := range productAliases(product) {
			if normalized := compact(alias); len(normalized) >= 4 {
				aliases = append(aliases, productAlias{product: product, compact: normalized})
			}
		}
	}
	sort.SliceStable(aliases, func(i, j int) bool { return len(aliases[i].compact) > len(aliases[j].compact) })
	result := make([]Line, 0)
	usedProducts := make(map[uint]bool)
	usedLines := make(map[int]bool)
	for index, textLine := range lines {
		compactLine := compact(textLine)
		for _, candidate := range aliases {
			if usedProducts[candidate.product.ID] || !strings.Contains(compactLine, candidate.compact) {
				continue
			}
			contextLine := textLine
			if len(amounts(contextLine)) == 0 {
				for offset := 1; offset <= 3 && index+offset < len(lines); offset++ {
					if summaryPattern.MatchString(lines[index+offset]) {
						break
					}
					contextLine += " " + lines[index+offset]
					if len(amounts(contextLine)) >= 2 {
						break
					}
				}
			}
			quantity, price := lineNumbers(contextLine)
			productID := candidate.product.ID
			result = append(result, Line{
				ProductID:      &productID,
				Description:    candidate.product.Name,
				Quantity:       quantity,
				Unit:           first(candidate.product.Unit, "Stk."),
				UnitPriceCents: price,
				PurchaseURL:    productPurchaseURL(candidate.product, supplierID),
			})
			usedProducts[candidate.product.ID], usedLines[index] = true, true
			break
		}
		if len(result) >= maxOrderLines {
			return result
		}
	}
	for index, textLine := range lines {
		if usedLines[index] || summaryPattern.MatchString(textLine) || len(result) >= maxOrderLines {
			continue
		}
		values := amounts(textLine)
		if len(values) < 2 {
			continue
		}
		matches := amountPattern.FindAllStringIndex(textLine, -1)
		if len(matches) < 2 {
			continue
		}
		description := strings.TrimSpace(textLine[:matches[len(matches)-2][0]])
		description = leadingLinePattern.ReplaceAllString(description, "")
		description = strings.Trim(description, " -–—|;:")
		if len([]rune(description)) < 3 || !containsLetter(description) {
			continue
		}
		quantity, price := lineNumbers(textLine)
		if len([]rune(description)) > 500 {
			description = string([]rune(description)[:500])
		}
		result = append(result, Line{Description: description, Quantity: quantity, Unit: "Stk.", UnitPriceCents: price})
	}
	return result
}

func productAliases(product ProductHint) []string {
	aliases := []string{product.SKU, product.Name}
	for _, offer := range product.Offers {
		aliases = append(aliases, offer.SupplierSKU)
	}
	return aliases
}

func productPurchaseURL(product ProductHint, supplierID uint) string {
	for _, offer := range product.Offers {
		if offer.SupplierID == supplierID && offer.PurchaseURL != "" {
			return offer.PurchaseURL
		}
	}
	return ""
}

func lineNumbers(line string) (float64, int64) {
	quantity := 1.0
	if match := quantityPattern.FindStringSubmatch(line); len(match) > 1 {
		if parsed, ok := parseNumber(match[1]); ok && parsed > 0 {
			quantity = parsed
		}
	}
	values := amounts(line)
	if len(values) == 0 {
		return quantity, 0
	}
	unitPrice := values[len(values)-1]
	if len(values) >= 2 {
		unitPrice = values[len(values)-2]
		lineTotal := values[len(values)-1]
		if quantity == 1 && unitPrice > 0 {
			ratio := float64(lineTotal) / float64(unitPrice)
			if rounded := math.Round(ratio); rounded >= 1 && rounded <= 100000 && math.Abs(ratio-rounded) < 0.01 {
				quantity = rounded
			}
		}
	}
	return quantity, unitPrice
}

func amounts(value string) []int64 {
	matches := amountPattern.FindAllString(value, -1)
	result := make([]int64, 0, len(matches))
	for _, match := range matches {
		if parsed, ok := parseNumber(match); ok {
			result = append(result, int64(math.Round(parsed*100)))
		}
	}
	return result
}

func parseNumber(value string) (float64, bool) {
	value = strings.NewReplacer("\u00a0", "", " ", "", "'", "", "’", "").Replace(strings.TrimSpace(value))
	comma, dot := strings.LastIndex(value, ","), strings.LastIndex(value, ".")
	switch {
	case comma >= 0 && dot >= 0:
		if comma > dot {
			value = strings.ReplaceAll(value, ".", "")
			value = strings.Replace(value, ",", ".", 1)
		} else {
			value = strings.ReplaceAll(value, ",", "")
		}
	case comma >= 0:
		value = strings.ReplaceAll(value, ".", "")
		value = strings.Replace(value, ",", ".", 1)
	}
	parsed, err := strconv.ParseFloat(value, 64)
	return parsed, err == nil && !math.IsNaN(parsed) && !math.IsInf(parsed, 0)
}

func containsLetter(value string) bool {
	for _, char := range value {
		if unicode.IsLetter(char) {
			return true
		}
	}
	return false
}

func confidence(preview Preview) int {
	score := 0
	if preview.SupplierID > 0 {
		score += 25
	}
	if preview.SupplierOrderNumber != "" {
		score += 15
	}
	if preview.OrderDate != nil {
		score += 10
	}
	if len(preview.Lines) > 0 && preview.Lines[0].Description != "" {
		score += 25
	}
	priced, linked := 0, 0
	for _, line := range preview.Lines {
		if line.UnitPriceCents > 0 {
			priced++
		}
		if line.ProductID != nil {
			linked++
		}
	}
	if len(preview.Lines) > 0 {
		score += int(math.Round(15 * float64(priced) / float64(len(preview.Lines))))
		score += int(math.Round(10 * float64(linked) / float64(len(preview.Lines))))
	}
	if score > 100 {
		return 100
	}
	return score
}

func first(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func abs64(value int64) int64 {
	if value < 0 {
		return -value
	}
	return value
}
