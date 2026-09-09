package scraper

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"slices"
	"strings"
)

// AdamHallItem is a product-number based line accepted by Adam Hall's fast-order API.
type AdamHallItem struct {
	ProductNumber string `json:"productNumber"`
	Quantity      int    `json:"quantity"`
}

type AdamHallCartLine struct {
	ProductNumber  string `json:"productNumber"`
	Description    string `json:"description"`
	Quantity       int    `json:"quantity"`
	UnitPriceCents int64  `json:"unitPriceCents"`
	TotalCents     int64  `json:"totalCents"`
}

type AdamHallCart struct {
	Lines           []AdamHallCartLine `json:"lines"`
	TotalCents      int64              `json:"totalCents"`
	Currency        string             `json:"currency"`
	Customer        string             `json:"customer"`
	ShippingAddress string             `json:"shippingAddress"`
	ShippingMethod  string             `json:"shippingMethod"`
	PaymentMethod   string             `json:"paymentMethod"`
}

type AdamHallOrder struct {
	OrderNumber string       `json:"orderNumber"`
	Cart        AdamHallCart `json:"cart"`
}

type AdamHallSubmissionUncertainError struct{ err error }

func (e *AdamHallSubmissionUncertainError) Error() string {
	return "Adam Hall hat den Bestellstatus nicht eindeutig bestätigt: " + e.err.Error()
}

func (e *AdamHallSubmissionUncertainError) Unwrap() error { return e.err }

func IsAdamHallSubmissionUncertain(err error) bool {
	var uncertain *AdamHallSubmissionUncertainError
	return errors.As(err, &uncertain)
}

type adamHallCartPayload struct {
	LineItems []struct {
		ID        string `json:"id"`
		Label     string `json:"label"`
		Quantity  int    `json:"quantity"`
		Removable bool   `json:"removable"`
		Payload   struct {
			ProductNumber string `json:"productNumber"`
		} `json:"payload"`
		Price struct {
			UnitPrice  float64 `json:"unitPrice"`
			TotalPrice float64 `json:"totalPrice"`
		} `json:"price"`
	} `json:"lineItems"`
	Price struct {
		TotalPrice float64 `json:"totalPrice"`
	} `json:"price"`
	Errors     json.RawMessage `json:"errors"`
	Extensions struct {
		ShippingOptions struct {
			Options []struct {
				ShippingOption    int    `json:"shippingOption"`
				ShippingAgentCode string `json:"shippingAgentCode"`
			} `json:"options"`
		} `json:"shippingOptionsExtension"`
	} `json:"extensions"`
}

type adamHallContextPayload struct {
	Customer *struct {
		FirstName             string `json:"firstName"`
		LastName              string `json:"lastName"`
		Email                 string `json:"email"`
		ActiveShippingAddress *struct {
			Company    string `json:"company"`
			FirstName  string `json:"firstName"`
			LastName   string `json:"lastName"`
			Street     string `json:"street"`
			Zipcode    string `json:"zipcode"`
			City       string `json:"city"`
			Additional string `json:"additionalAddressLine1"`
			Country    *struct {
				Name string `json:"name"`
			} `json:"country"`
		} `json:"activeShippingAddress"`
	} `json:"customer"`
	Currency *struct {
		ISOCode string `json:"isoCode"`
	} `json:"currency"`
	ShippingMethod *struct {
		Name       string `json:"name"`
		Translated struct {
			Name string `json:"name"`
		} `json:"translated"`
	} `json:"shippingMethod"`
	PaymentMethod *struct {
		Name       string `json:"name"`
		Translated struct {
			Name string `json:"name"`
		} `json:"translated"`
	} `json:"paymentMethod"`
	Extensions map[string]json.RawMessage `json:"extensions"`
}

func (f *Fetcher) AdamHallConfigured() bool {
	return strings.TrimSpace(f.adamHallUsername) != "" && strings.TrimSpace(f.adamHallPassword) != ""
}

func (f *Fetcher) AdamHallCart(ctx context.Context, items []AdamHallItem) (AdamHallCart, error) {
	f.adamHallCheckoutMu.Lock()
	defer f.adamHallCheckoutMu.Unlock()
	return f.buildAdamHallCart(ctx, items)
}

func (f *Fetcher) PlaceAdamHallOrder(ctx context.Context, items []AdamHallItem, customerComment string) (AdamHallOrder, error) {
	f.adamHallCheckoutMu.Lock()
	defer f.adamHallCheckoutMu.Unlock()

	cart, token, err := f.prepareAdamHallCart(ctx, items)
	if err != nil {
		return AdamHallOrder{}, err
	}
	body := map[string]any{
		"customerComment":   strings.TrimSpace(customerComment),
		"affiliateCode":     "ProcurementCore",
		"campaignCode":      "",
		"isAluCut2m":        false,
		"isPartialDelivery": true,
	}
	var payload struct {
		OrderNumber string `json:"orderNumber"`
	}
	if err := f.adamHallJSON(ctx, http.MethodPost, "/checkout/order", token, body, &payload); err != nil {
		return AdamHallOrder{}, &AdamHallSubmissionUncertainError{err: err}
	}
	if strings.TrimSpace(payload.OrderNumber) == "" {
		return AdamHallOrder{}, &AdamHallSubmissionUncertainError{err: errors.New("keine Bestellnummer zurückgegeben")}
	}
	return AdamHallOrder{OrderNumber: payload.OrderNumber, Cart: cart}, nil
}

func (f *Fetcher) buildAdamHallCart(ctx context.Context, items []AdamHallItem) (AdamHallCart, error) {
	cart, _, err := f.prepareAdamHallCart(ctx, items)
	return cart, err
}

func (f *Fetcher) prepareAdamHallCart(ctx context.Context, items []AdamHallItem) (AdamHallCart, string, error) {
	items, err := normalizeAdamHallItems(items)
	if err != nil {
		return AdamHallCart{}, "", err
	}
	if !f.AdamHallConfigured() {
		return AdamHallCart{}, "", errors.New("Adam-Hall-Zugangsdaten sind nicht konfiguriert")
	}
	token, err := f.loginAdamHall(ctx)
	if err != nil {
		return AdamHallCart{}, "", err
	}
	if err := f.clearAdamHallCart(ctx, token); err != nil {
		return AdamHallCart{}, "", err
	}
	var fastOrder struct {
		Success bool `json:"success"`
	}
	if err := f.adamHallJSON(ctx, http.MethodPost, "/checkout/fastOrder", token, map[string]any{"items": items}, &fastOrder); err != nil {
		return AdamHallCart{}, "", err
	}
	if !fastOrder.Success {
		return AdamHallCart{}, "", errors.New("Adam Hall konnte den Warenkorb nicht vollständig anlegen")
	}

	var payload adamHallCartPayload
	if err := f.adamHallJSON(ctx, http.MethodGet, "/checkout/cart", token, nil, &payload); err != nil {
		return AdamHallCart{}, "", err
	}
	if err := f.selectAdamHallShipping(ctx, token, &payload); err != nil {
		return AdamHallCart{}, "", err
	}
	if err := f.adamHallJSON(ctx, http.MethodPost, "/checkout/cart", token, map[string]any{
		"isConfirm": true, "isPartialDelivery": true, "isAluCut2m": false,
	}, &payload); err != nil {
		return AdamHallCart{}, "", err
	}
	if err := adamHallBlockingCartError(payload.Errors); err != nil {
		return AdamHallCart{}, "", err
	}

	var contextPayload adamHallContextPayload
	if err := f.adamHallJSON(ctx, http.MethodGet, "/context", token, nil, &contextPayload); err != nil {
		return AdamHallCart{}, "", err
	}
	cart := adamHallCartFromPayload(payload, contextPayload)
	if err := validateAdamHallCart(items, cart.Lines); err != nil {
		return AdamHallCart{}, "", err
	}
	return cart, token, nil
}

func (f *Fetcher) clearAdamHallCart(ctx context.Context, token string) error {
	var payload adamHallCartPayload
	if err := f.adamHallJSON(ctx, http.MethodGet, "/checkout/cart", token, nil, &payload); err != nil {
		return err
	}
	ids := make([]string, 0, len(payload.LineItems))
	for _, line := range payload.LineItems {
		if line.Removable && strings.TrimSpace(line.ID) != "" {
			ids = append(ids, line.ID)
		}
	}
	if len(ids) == 0 {
		return nil
	}
	if err := f.adamHallJSON(ctx, http.MethodDelete, "/checkout/cart/line-item", token, map[string]any{"ids": ids}, nil); err != nil {
		return fmt.Errorf("Adam-Hall-Warenkorb konnte nicht geleert werden: %w", err)
	}
	return nil
}

func validateAdamHallCart(items []AdamHallItem, lines []AdamHallCartLine) error {
	quantities := make(map[string]int, len(lines))
	for _, line := range lines {
		productNumber := strings.ToUpper(strings.TrimSpace(line.ProductNumber))
		if productNumber != "" {
			quantities[productNumber] += line.Quantity
		}
	}
	for _, item := range items {
		actual := quantities[item.ProductNumber]
		if actual != item.Quantity {
			return fmt.Errorf("Adam Hall hat Artikel %s nicht mit der erwarteten Menge übernommen (erwartet: %d, Warenkorb: %d)", item.ProductNumber, item.Quantity, actual)
		}
		delete(quantities, item.ProductNumber)
	}
	if len(quantities) > 0 {
		unexpected := make([]string, 0, len(quantities))
		for productNumber := range quantities {
			unexpected = append(unexpected, productNumber)
		}
		slices.Sort(unexpected)
		return fmt.Errorf("Adam-Hall-Warenkorb enthält unerwartete Artikel: %s", strings.Join(unexpected, ", "))
	}
	return nil
}

func normalizeAdamHallItems(items []AdamHallItem) ([]AdamHallItem, error) {
	combined := make(map[string]int, len(items))
	order := make([]string, 0, len(items))
	for _, item := range items {
		productNumber := strings.ToUpper(strings.TrimSpace(item.ProductNumber))
		if productNumber == "" || item.Quantity <= 0 {
			return nil, errors.New("Jede Adam-Hall-Position benötigt Artikelnummer und positive Menge")
		}
		if _, exists := combined[productNumber]; !exists {
			order = append(order, productNumber)
		}
		combined[productNumber] += item.Quantity
	}
	if len(order) == 0 {
		return nil, errors.New("Der Adam-Hall-Warenkorb enthält keine Positionen")
	}
	result := make([]AdamHallItem, 0, len(order))
	for _, productNumber := range order {
		result = append(result, AdamHallItem{ProductNumber: productNumber, Quantity: combined[productNumber]})
	}
	return result, nil
}

func (f *Fetcher) selectAdamHallShipping(ctx context.Context, token string, cart *adamHallCartPayload) error {
	if len(cart.Extensions.ShippingOptions.Options) == 0 {
		return nil
	}
	selected := cart.Extensions.ShippingOptions.Options[0].ShippingOption
	for _, option := range cart.Extensions.ShippingOptions.Options {
		if option.ShippingOption == 1 {
			selected = option.ShippingOption
			break
		}
	}
	return f.adamHallJSON(ctx, http.MethodPatch, "/context", token, map[string]any{
		"selectedShippingMethodOptionNumber": selected,
	}, nil)
}

func (f *Fetcher) adamHallJSON(ctx context.Context, method, endpoint, token string, input, output any) error {
	var body io.Reader
	if input != nil {
		encoded, err := json.Marshal(input)
		if err != nil {
			return errors.New("Adam-Hall-Anfrage konnte nicht vorbereitet werden")
		}
		body = bytes.NewReader(encoded)
	}
	req, err := http.NewRequestWithContext(ctx, method, f.adamHallBaseURL+endpoint, body)
	if err != nil {
		return errors.New("Adam-Hall-Anfrage konnte nicht vorbereitet werden")
	}
	setAdamHallHeaders(req)
	req.Header.Set("sw-context-token", token)
	if input != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	response, err := f.adamHallClient.Do(req)
	if err != nil {
		return errors.New("Adam Hall konnte nicht erreicht werden")
	}
	defer response.Body.Close()
	limited := io.LimitReader(response.Body, maxPageBytes)
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return adamHallResponseError(response.StatusCode, limited)
	}
	if output == nil || response.StatusCode == http.StatusNoContent {
		_, _ = io.Copy(io.Discard, limited)
		return nil
	}
	if err := json.NewDecoder(limited).Decode(output); err != nil {
		return fmt.Errorf("Adam Hall hat für %s %s eine ungültige Antwort geliefert: %w", method, endpoint, err)
	}
	return nil
}

func adamHallResponseError(status int, body io.Reader) error {
	var payload struct {
		Errors []struct {
			Detail string `json:"detail"`
			Title  string `json:"title"`
		} `json:"errors"`
		LineItems []struct {
			ProductNumber string `json:"productNumber"`
			Errors        []struct {
				Code string `json:"code"`
			} `json:"errors"`
		} `json:"lineItems"`
	}
	_ = json.NewDecoder(body).Decode(&payload)
	if len(payload.LineItems) > 0 {
		missing := make([]string, 0, len(payload.LineItems))
		for _, line := range payload.LineItems {
			if len(line.Errors) > 0 {
				missing = append(missing, line.ProductNumber)
			}
		}
		if len(missing) > 0 {
			return fmt.Errorf("Adam Hall lehnt folgende Artikel ab: %s", strings.Join(missing, ", "))
		}
	}
	if len(payload.Errors) > 0 {
		message := strings.TrimSpace(first(payload.Errors[0].Detail, payload.Errors[0].Title))
		if message != "" {
			return fmt.Errorf("Adam Hall: %s", message)
		}
	}
	return fmt.Errorf("Adam Hall antwortet mit HTTP %d", status)
}

type adamHallCartError struct {
	Message string `json:"message"`
	Level   int    `json:"level"`
	Block   bool   `json:"blockOrder"`
}

func adamHallBlockingCartError(raw json.RawMessage) error {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) || bytes.Equal(trimmed, []byte("[]")) || bytes.Equal(trimmed, []byte("{}")) {
		return nil
	}
	var rows []adamHallCartError
	if trimmed[0] == '[' {
		if err := json.Unmarshal(trimmed, &rows); err != nil {
			return errors.New("Adam Hall hat ungültige Warenkorbhinweise geliefert")
		}
	} else {
		var byKey map[string]adamHallCartError
		if err := json.Unmarshal(trimmed, &byKey); err != nil {
			return errors.New("Adam Hall hat ungültige Warenkorbhinweise geliefert")
		}
		for _, row := range byKey {
			rows = append(rows, row)
		}
	}
	for _, row := range rows {
		if !row.Block && row.Level < 20 {
			continue
		}
		message := strings.TrimSpace(row.Message)
		if message == "" {
			message = "Der Adam-Hall-Warenkorb enthält einen blockierenden Fehler"
		}
		return errors.New(message)
	}
	return nil
}

func adamHallCartFromPayload(payload adamHallCartPayload, contextPayload adamHallContextPayload) AdamHallCart {
	cart := AdamHallCart{TotalCents: moneyCents(payload.Price.TotalPrice), Currency: "EUR"}
	for _, line := range payload.LineItems {
		if strings.TrimSpace(line.Payload.ProductNumber) == "" {
			continue
		}
		cart.Lines = append(cart.Lines, AdamHallCartLine{
			ProductNumber:  line.Payload.ProductNumber,
			Description:    line.Label,
			Quantity:       line.Quantity,
			UnitPriceCents: moneyCents(line.Price.UnitPrice),
			TotalCents:     moneyCents(line.Price.TotalPrice),
		})
	}
	if contextPayload.Currency != nil && strings.TrimSpace(contextPayload.Currency.ISOCode) != "" {
		cart.Currency = strings.ToUpper(contextPayload.Currency.ISOCode)
	}
	if contextPayload.Customer != nil {
		cart.Customer = strings.TrimSpace(strings.Join([]string{contextPayload.Customer.FirstName, contextPayload.Customer.LastName}, " "))
		if cart.Customer == "" {
			cart.Customer = contextPayload.Customer.Email
		}
		if address := contextPayload.Customer.ActiveShippingAddress; address != nil {
			parts := []string{address.Company, strings.TrimSpace(address.FirstName + " " + address.LastName), address.Street, address.Additional, strings.TrimSpace(address.Zipcode + " " + address.City)}
			if address.Country != nil {
				parts = append(parts, address.Country.Name)
			}
			clean := parts[:0]
			for _, part := range parts {
				if value := strings.TrimSpace(part); value != "" {
					clean = append(clean, value)
				}
			}
			cart.ShippingAddress = strings.Join(clean, ", ")
		}
	}
	if contextPayload.ShippingMethod != nil {
		cart.ShippingMethod = first(contextPayload.ShippingMethod.Translated.Name, contextPayload.ShippingMethod.Name)
	}
	if contextPayload.PaymentMethod != nil {
		cart.PaymentMethod = first(contextPayload.PaymentMethod.Translated.Name, contextPayload.PaymentMethod.Name)
	}
	return cart
}

func moneyCents(value float64) int64 { return int64(math.Round(value * 100)) }
