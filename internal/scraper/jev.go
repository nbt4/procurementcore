package scraper

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"

	xhtml "golang.org/x/net/html"
)

const maxJevProductCandidates = 8

// enrichProductPreview lets Jev identify the main product when the page supplies
// conflicting structured product records. Every selectable value comes from the
// downloaded page; an unavailable or uncertain decision keeps the local parser.
func (f *Fetcher) enrichProductPreview(ctx context.Context, body []byte, sourceURL *url.URL, fallback ProductPreview) ProductPreview {
	if f == nil || f.jev == nil {
		return fallback
	}
	if len(body) > maxPageBytes {
		body = body[:maxPageBytes]
	}
	document, err := xhtml.Parse(strings.NewReader(string(body)))
	if err != nil {
		return fallback
	}

	var title, heading string
	var scripts []string
	var products []*xhtml.Node
	forEachElement(document, func(node *xhtml.Node) {
		switch node.Data {
		case "title":
			if title == "" {
				title = nodeText(node)
			}
		case "h1":
			if heading == "" {
				heading = nodeText(node)
			}
		case "script":
			if strings.EqualFold(attribute(node, "type"), "application/ld+json") && node.FirstChild != nil {
				scripts = append(scripts, node.FirstChild.Data)
			}
		default:
			if hasItemType(node, "Product") {
				products = append(products, node)
			}
		}
	})

	candidates := make([]ProductPreview, 0, 1+len(products))
	seen := map[string]bool{productCandidateKey(fallback): true}
	add := func(candidate ProductPreview) {
		candidate.Name = cleanText(candidate.Name)
		candidate.Description = cleanText(candidate.Description)
		candidate.ImageURL = absoluteURL(sourceURL, candidate.ImageURL)
		candidate.PurchaseURL = sourceURL.String()
		if candidate.Name == "" || seen[productCandidateKey(candidate)] {
			return
		}
		seen[productCandidateKey(candidate)] = true
		candidates = append(candidates, candidate)
	}
	if fallback.Source == "OpenGraph/HTML" && heading != "" {
		visible := fallback
		visible.Name = heading
		visible.Source = "HTML heading + Jev"
		add(visible)
	}
	for _, script := range scripts {
		var value any
		decoder := json.NewDecoder(strings.NewReader(script))
		decoder.UseNumber()
		if decoder.Decode(&value) != nil {
			continue
		}
		var records []map[string]any
		collectProductRecords(value, &records)
		for _, record := range records {
			candidate := ProductPreview{Attributes: map[string]string{}, Currency: "EUR", Source: "JSON-LD + Jev"}
			applyProduct(&candidate, record)
			add(candidate)
		}
	}
	for _, product := range products {
		candidate := ProductPreview{Attributes: map[string]string{}, Currency: "EUR", Source: "schema.org Microdata + Jev"}
		applyMicrodata(&candidate, product)
		add(candidate)
	}
	if len(candidates) == 0 {
		return fallback
	}

	contextName := first(heading, title, fallback.Name)
	sort.SliceStable(candidates, func(i, j int) bool {
		return productNameOverlap(candidates[i].Name, contextName) > productNameOverlap(candidates[j].Name, contextName)
	})
	if len(candidates) > maxJevProductCandidates {
		candidates = candidates[:maxJevProductCandidates]
	}
	criteria := map[string]string{
		"keep": productCandidateSummary(fallback),
	}
	for index, candidate := range candidates {
		criteria[fmt.Sprintf("product_%d", index)] = productCandidateSummary(candidate)
	}
	decision, err := f.jev.Choose(ctx, map[string]string{
		"host":    sourceURL.Hostname(),
		"path":    clipProductText(sourceURL.EscapedPath(), 160),
		"title":   clipProductText(title, 160),
		"heading": clipProductText(heading, 160),
	}, "Choose the main product sold on this exact page. Prefer the visible product heading and URL over recommendations, accessories, or unrelated structured data. Keep the current extraction if evidence is unclear.", criteria)
	if err != nil || decision.Choice == "keep" || decision.Confidence < scraperJevMinimumConfidence() {
		return fallback
	}
	var index int
	if _, err := fmt.Sscanf(decision.Choice, "product_%d", &index); err != nil || index < 0 || index >= len(candidates) || decision.Choice != fmt.Sprintf("product_%d", index) {
		return fallback
	}
	selected := candidates[index]
	// Visible specifications belong to the selected page product, regardless of
	// whether its identity came from JSON-LD, microdata, or the heading.
	if heading != "" && productNameOverlap(selected.Name, heading) > 0 {
		if applyGenericSpecs(&selected, document) > 0 {
			selected.Source += " + HTML specs"
		}
	}
	// A product decision must never select or transfer a price from a different
	// structured record. The existing parser remains the price authority.
	if sameProductIdentity(fallback, selected) {
		selected.PriceCents = fallback.PriceCents
		selected.Currency = fallback.Currency
	} else {
		selected.PriceCents = 0
	}
	return selected
}

func sameProductIdentity(a, b ProductPreview) bool {
	if a.SKU != "" || b.SKU != "" {
		return a.SKU != "" && strings.EqualFold(strings.TrimSpace(a.SKU), strings.TrimSpace(b.SKU))
	}
	return strings.EqualFold(cleanText(a.Name), cleanText(b.Name))
}

func collectProductRecords(value any, records *[]map[string]any) {
	if len(*records) >= 32 {
		return
	}
	switch typed := value.(type) {
	case []any:
		for _, item := range typed {
			collectProductRecords(item, records)
		}
	case map[string]any:
		if hasType(typed["@type"], "Product") {
			*records = append(*records, typed)
		}
		for _, key := range []string{"@graph", "mainEntity", "itemListElement", "item", "hasVariant"} {
			collectProductRecords(typed[key], records)
		}
	}
}

func productCandidateKey(preview ProductPreview) string {
	return strings.ToLower(cleanText(preview.Name) + "|" + cleanText(preview.SKU) + "|" + cleanText(preview.Model))
}

func productCandidateSummary(preview ProductPreview) string {
	return fmt.Sprintf("name=%q; sku=%q; manufacturer=%q; model=%q; description=%q", clipProductText(preview.Name, 120), clipProductText(preview.SKU, 60), clipProductText(preview.Manufacturer, 60), clipProductText(preview.Model, 60), clipProductText(preview.Description, 120))
}

func clipProductText(value string, max int) string {
	value = cleanText(value)
	runes := []rune(value)
	if len(runes) > max {
		return string(runes[:max])
	}
	return value
}

func productNameOverlap(name, contextName string) int {
	contextWords := strings.Fields(strings.ToLower(contextName))
	name = strings.ToLower(name)
	score := 0
	for _, word := range contextWords {
		if len([]rune(word)) > 2 && strings.Contains(name, word) {
			score++
		}
	}
	return score
}

func scraperJevMinimumConfidence() float64 {
	raw := strings.TrimSpace(os.Getenv("JEV_MIN_CONFIDENCE"))
	if value, err := strconv.ParseFloat(raw, 64); err == nil && value >= 0 && value <= 1 {
		return value
	}
	return 0.70
}
