package scraper

import (
	"strings"
	"unicode"

	xhtml "golang.org/x/net/html"
)

// applyGenericSpecs reads visible key/value markup independently of the shop.
// Structured data and shop adapters remain authoritative for duplicate keys.
func applyGenericSpecs(preview *ProductPreview, document *xhtml.Node) int {
	if preview.Attributes == nil {
		preview.Attributes = map[string]string{}
	}
	before := len(preview.Attributes)
	var walk func(*xhtml.Node)
	walk = func(node *xhtml.Node) {
		if node.Type == xhtml.ElementNode {
			switch node.Data {
			case "script", "style", "template", "noscript", "nav", "header", "footer", "aside":
				return
			case "tr":
				var cells []*xhtml.Node
				for child := node.FirstChild; child != nil; child = child.NextSibling {
					if child.Type == xhtml.ElementNode && (child.Data == "th" || child.Data == "td") {
						cells = append(cells, child)
					}
				}
				if len(cells) == 2 {
					addGenericSpec(preview, nodeText(cells[0]), nodeText(cells[1]))
				}
			case "dt":
				if value := nextElementSibling(node); value != nil && value.Data == "dd" {
					addGenericSpec(preview, nodeText(node), nodeText(value))
				}
			case "li", "p":
				foundLabel := false
				for child := node.FirstChild; child != nil; child = child.NextSibling {
					if child.Type != xhtml.ElementNode {
						continue
					}
					if child.Data != "strong" && child.Data != "b" {
						break
					}
					label := nodeText(child)
					if !strings.HasSuffix(label, ":") && !inSpecArea(node) {
						break
					}
					value := strings.TrimSpace(strings.TrimPrefix(nodeText(node), label))
					addGenericSpec(preview, label, value)
					foundLabel = true
					break
				}
				if !foundLabel && inSpecArea(node) {
					if label, value, ok := splitAttribute(nodeText(node)); ok {
						addGenericSpec(preview, label, value)
					}
				}
			case "div":
				if !inSpecArea(node) {
					break
				}
				var label, value string
				for child := node.FirstChild; child != nil; child = child.NextSibling {
					if child.Type != xhtml.ElementNode {
						continue
					}
					classes := strings.ToLower(attribute(child, "class"))
					switch {
					case strings.Contains(classes, "label"), strings.Contains(classes, "key"), strings.Contains(classes, "name"):
						label = nodeText(child)
					case strings.Contains(classes, "value"):
						value = nodeText(child)
					}
				}
				if label != "" && value != "" {
					addGenericSpec(preview, label, value)
				}
			}
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(document)
	return len(preview.Attributes) - before
}

func inSpecArea(node *xhtml.Node) bool {
	for parent := node.Parent; parent != nil; parent = parent.Parent {
		if parent.Type != xhtml.ElementNode {
			continue
		}
		identifier := strings.ToLower(attribute(parent, "class") + " " + attribute(parent, "id"))
		for _, hint := range []string{"spec", "technical", "tech-data", "datasheet", "data-sheet", "attribute", "product-detail", "feature"} {
			if strings.Contains(identifier, hint) {
				return true
			}
		}
	}
	return false
}

func addGenericSpec(preview *ProductPreview, rawName, rawValue string) {
	name := cleanText(strings.TrimSuffix(strings.TrimSpace(rawName), ":"))
	value := cleanText(rawValue)
	containsLetter := strings.IndexFunc(name, unicode.IsLetter) >= 0
	if !containsLetter || len([]rune(name)) < 2 || len([]rune(name)) > 80 || len([]rune(value)) < 1 || len([]rune(value)) > 400 || strings.EqualFold(name, value) || strings.ContainsAny(name, "\n\r") {
		return
	}
	if strings.HasPrefix(strings.ToLower(value), "http://") || strings.HasPrefix(strings.ToLower(value), "https://") {
		return
	}
	for existing := range preview.Attributes {
		if strings.EqualFold(existing, name) {
			return
		}
	}
	if len(preview.Attributes) < 200 {
		preview.Attributes[name] = value
	}
}
