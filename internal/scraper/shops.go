package scraper

import (
	"net/url"
	"strings"

	xhtml "golang.org/x/net/html"
)

func applyMicrodata(preview *ProductPreview, document *xhtml.Node) bool {
	product := findElement(document, func(node *xhtml.Node) bool {
		return hasItemType(node, "Product")
	})
	if product == nil {
		return false
	}

	fill := func(target *string, properties ...string) {
		if *target != "" {
			return
		}
		for _, property := range properties {
			if node := findScopedItemProperty(product, property); node != nil {
				if value := itemValue(node); value != "" {
					*target = value
					return
				}
			}
		}
	}
	fill(&preview.Name, "name")
	fill(&preview.Description, "description")
	fill(&preview.SKU, "sku", "gtin13", "gtin")
	fill(&preview.Model, "model", "mpn")
	fill(&preview.ImageURL, "image")

	if preview.Manufacturer == "" {
		for _, property := range []string{"manufacturer", "brand"} {
			if node := findScopedItemProperty(product, property); node != nil {
				preview.Manufacturer = itemValue(node)
				if nested := findScopedItemProperty(node, "name"); nested != nil {
					preview.Manufacturer = first(itemValue(nested), preview.Manufacturer)
				}
				if preview.Manufacturer != "" {
					break
				}
			}
		}
	}

	if offer := findScopedItemProperty(product, "offers"); offer != nil {
		if preview.PriceCents == 0 {
			if price := findScopedItemProperty(offer, "price"); price != nil {
				preview.PriceCents = priceCents(itemValue(price))
			}
		}
		if currency := findScopedItemProperty(offer, "priceCurrency"); currency != nil {
			preview.Currency = normalizeCurrency(itemValue(currency))
		}
	}
	return true
}

func applyShopPage(preview *ProductPreview, document *xhtml.Node, sourceURL *url.URL) {
	host := normalizedHost(sourceURL.Hostname())
	switch {
	case hostMatches(host, "ltt-versand.de"):
		applyLTTPage(preview, document)
	case hostMatches(host, "huss-licht-ton.de"):
		applyHussPage(preview, document)
	case hostMatches(host, "thomann.de"):
		applyThomannPage(preview, document)
	case hostMatches(host, "steinigke.de"), hostMatches(host, "steinigke.com"), hostMatches(host, "steinigke.at"):
		applySteinigkePage(preview, document)
	case hostMatches(host, "ab-in-die-box.de"):
		applyBoxShopPage(preview, document)
	case hostMatches(host, "caseman-berlin.de"):
		preview.Source = "Caseman/JSON-LD"
	case hostMatches(host, "aweo.de"):
		preview.Source = "aweo/JSON-LD"
	}
}

func applyLTTPage(preview *ProductPreview, document *xhtml.Node) {
	if orderNumber := findElement(document, func(node *xhtml.Node) bool { return hasClass(node, "ordernumber") }); orderNumber != nil {
		if sku := findItemProperty(orderNumber, "sku"); sku != nil {
			preview.SKU = itemValue(sku)
		}
	}
	description := findElement(document, func(node *xhtml.Node) bool {
		return hasClass(node, "product--description") && hasClass(node, "ltt--text")
	})
	addTableAttributes(preview, description, "")

	baseInfo := findElement(document, func(node *xhtml.Node) bool { return hasClass(node, "product--base-info") })
	forEachElement(baseInfo, func(node *xhtml.Node) {
		if node.Data != "div" || (!hasClass(node, "ean") && !hasClass(node, "suppliernumber")) {
			return
		}
		labelNode := findElement(node, func(child *xhtml.Node) bool { return child.Data == "strong" })
		label := strings.TrimSpace(strings.TrimSuffix(nodeText(labelNode), ":"))
		value := strings.TrimSpace(strings.TrimPrefix(nodeText(node), nodeText(labelNode)))
		if label != "" && value != "" {
			preview.Attributes[label] = value
			if strings.EqualFold(label, "MPN") && preview.Model == "" {
				preview.Model = value
			}
		}
	})
	preview.Source = "LTT/HTML"
}

func applyHussPage(preview *ProductPreview, document *xhtml.Node) {
	description := findItemProperty(document, "description")
	addColonListAttributes(preview, description)

	forEachElement(document, func(node *xhtml.Node) {
		if !hasClass(node, "ad_merkmale_content_element_wrapper") {
			return
		}
		nameNode := findElement(node, func(child *xhtml.Node) bool { return hasClass(child, "ad_merkmale_content_element_left") })
		valueNode := findElement(node, func(child *xhtml.Node) bool { return hasClass(child, "ad_merkmale_content_element_right") })
		addAttribute(preview, nodeText(nameNode), nodeText(valueNode))
	})
	if gtin := findItemProperty(document, "gtin13"); gtin != nil {
		addAttribute(preview, "EAN", itemValue(gtin))
	}
	if preview.Model == "" {
		parts := strings.Split(preview.Name, " - ")
		candidate := strings.TrimSpace(parts[len(parts)-1])
		if len(parts) > 1 && !strings.Contains(candidate, " ") {
			preview.Model = candidate
		}
	}
	preview.Source = "Huss Licht & Ton/HTML"
}

func applyThomannPage(preview *ProductPreview, document *xhtml.Node) {
	if title := findElement(document, func(node *xhtml.Node) bool { return hasClass(node, "product-title") }); title != nil {
		if heading := findElement(title, func(node *xhtml.Node) bool { return node.Data == "h1" }); heading != nil {
			preview.Name = nodeText(heading)
		}
	}
	forEachElement(document, func(node *xhtml.Node) {
		if node.Data != "li" || !hasClass(node, "keyfeature") {
			return
		}
		labelNode := findElement(node, func(child *xhtml.Node) bool { return hasClass(child, "keyfeature__label") })
		valueNode := nextElementSibling(labelNode)
		name, value := nodeText(labelNode), nodeText(valueNode)
		if strings.EqualFold(name, "Artikelnummer") && preview.SKU == "" {
			preview.SKU = value
			return
		}
		addAttribute(preview, name, value)
	})
	description := findElement(document, func(node *xhtml.Node) bool { return hasClass(node, "product-text") })
	addColonListAttributes(preview, description)
	preview.Source = "Thomann/HTML"
}

func applySteinigkePage(preview *ProductPreview, document *xhtml.Node) {
	table := findElement(document, func(node *xhtml.Node) bool { return node.Data == "table" && hasClass(node, "technicalData") })
	addTableAttributes(preview, table, "")
	preview.Source = "Steinigke/JSON-LD+HTML"
}

func applyBoxShopPage(preview *ProductPreview, document *xhtml.Node) {
	if title := findElement(document, func(node *xhtml.Node) bool {
		return node.Data == "span" && attribute(node, "data-ui-id") == "page-title-wrapper"
	}); title != nil {
		preview.Name = nodeText(title)
	}
	if description := findElement(document, func(node *xhtml.Node) bool { return hasClass(node, "product-description") }); description != nil {
		preview.Description = nodeText(description)
	}
	if sku := findElement(document, func(node *xhtml.Node) bool { return hasClass(node, "product-detail-value-sku") }); sku != nil {
		preview.SKU = nodeText(sku)
	}
	if priceBox := findElement(document, func(node *xhtml.Node) bool { return hasClass(node, "price-final_price") }); priceBox != nil {
		if price := findElement(priceBox, func(node *xhtml.Node) bool { return attribute(node, "data-price-amount") != "" }); price != nil {
			preview.PriceCents = priceCents(attribute(price, "data-price-amount"))
			preview.Currency = "EUR"
		}
	}
	attributeList := findElement(document, func(node *xhtml.Node) bool { return hasClass(node, "aidb-product-attributes") })
	forEachElement(attributeList, func(node *xhtml.Node) {
		if node.Data != "li" {
			return
		}
		name := first(attribute(node, "aria-label"), attribute(node, "title"))
		if label, value, ok := splitAttribute(name); ok {
			addAttribute(preview, label, value)
			return
		}
		inner := findElement(node, func(child *xhtml.Node) bool { return child.Data == "div" })
		value := strings.TrimSpace(strings.Join(nonEmpty(attribute(inner, "data-text1"), attribute(inner, "data-text2")), " / "))
		addAttribute(preview, name, first(value, "Ja"))
	})
	preview.Source = "ab-in-die-BOX/HTML"
}

func addTableAttributes(preview *ProductPreview, root *xhtml.Node, requiredClass string) {
	forEachElement(root, func(node *xhtml.Node) {
		if node.Data != "tr" || (requiredClass != "" && !hasClass(node, requiredClass)) {
			return
		}
		var cells []*xhtml.Node
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			if child.Type == xhtml.ElementNode && (child.Data == "td" || child.Data == "th") {
				cells = append(cells, child)
			}
		}
		if len(cells) >= 2 {
			addAttribute(preview, strings.TrimSuffix(nodeText(cells[0]), ":"), nodeText(cells[1]))
		}
	})
}

func addColonListAttributes(preview *ProductPreview, root *xhtml.Node) {
	forEachElement(root, func(node *xhtml.Node) {
		if node.Data != "li" {
			return
		}
		if name, value, ok := splitAttribute(nodeText(node)); ok {
			addAttribute(preview, name, value)
		}
	})
}

func splitAttribute(value string) (string, string, bool) {
	name, result, found := strings.Cut(value, ":")
	name, result = cleanText(name), cleanText(result)
	return name, result, found && name != "" && result != ""
}

func addAttribute(preview *ProductPreview, name, value string) {
	name, value = cleanText(strings.TrimSuffix(name, ":")), cleanText(value)
	if name != "" && value != "" {
		preview.Attributes[name] = value
	}
}

func findItemProperty(root *xhtml.Node, property string) *xhtml.Node {
	return findElement(root, func(node *xhtml.Node) bool {
		for _, value := range strings.Fields(attribute(node, "itemprop")) {
			if strings.EqualFold(value, property) {
				return true
			}
		}
		return false
	})
}

func findScopedItemProperty(scope *xhtml.Node, property string) *xhtml.Node {
	if scope == nil {
		return nil
	}
	var walk func(*xhtml.Node, bool) *xhtml.Node
	walk = func(node *xhtml.Node, isRoot bool) *xhtml.Node {
		if node.Type == xhtml.ElementNode {
			for _, value := range strings.Fields(attribute(node, "itemprop")) {
				if strings.EqualFold(value, property) {
					return node
				}
			}
			if !isRoot && hasAttribute(node, "itemscope") {
				return nil
			}
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			if found := walk(child, false); found != nil {
				return found
			}
		}
		return nil
	}
	return walk(scope, true)
}

func hasAttribute(node *xhtml.Node, name string) bool {
	if node == nil {
		return false
	}
	for _, item := range node.Attr {
		if strings.EqualFold(item.Key, name) {
			return true
		}
	}
	return false
}

func itemValue(node *xhtml.Node) string {
	if node == nil {
		return ""
	}
	for _, name := range []string{"content", "href", "src", "value"} {
		if value := attribute(node, name); value != "" {
			return cleanText(value)
		}
	}
	return nodeText(node)
}

func hasItemType(node *xhtml.Node, expected string) bool {
	for _, value := range strings.Fields(attribute(node, "itemtype")) {
		parts := strings.Split(strings.TrimSuffix(value, "/"), "/")
		if strings.EqualFold(parts[len(parts)-1], expected) {
			return true
		}
	}
	return false
}

func normalizeCurrency(value string) string {
	switch strings.ToUpper(strings.TrimSpace(value)) {
	case "€", "EURO", "EUR":
		return "EUR"
	case "$", "USD":
		return "USD"
	case "£", "GBP":
		return "GBP"
	default:
		return strings.ToUpper(strings.TrimSpace(value))
	}
}

func normalizedHost(host string) string {
	return strings.ToLower(strings.TrimSuffix(strings.TrimSpace(host), "."))
}

func hostMatches(host, domain string) bool {
	return host == domain || strings.HasSuffix(host, "."+domain)
}

func nonEmpty(values ...string) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		if value = cleanText(value); value != "" {
			result = append(result, value)
		}
	}
	return result
}
