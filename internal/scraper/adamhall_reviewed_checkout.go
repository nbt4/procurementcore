package scraper

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"strings"
)

// The context is private owning-service state. Callers must encrypt it at rest
// and never include it in API results, previews or audit payloads.
type AdamHallReviewedCheckout struct {
	Cart         AdamHallCart
	ContextToken string
}

func (f *Fetcher) AdamHallAccountFingerprint() string {
	h := sha256.Sum256([]byte(strings.ToLower(strings.TrimSpace(f.adamHallUsername)) + "|" + f.adamHallBaseURL))
	return hex.EncodeToString(h[:])
}

// PrepareAdamHallReviewedCheckout is an explicitly confirmed remote cart action,
// never a read-only preview. It never removes pre-existing cart contents.
func (f *Fetcher) PrepareAdamHallReviewedCheckout(ctx context.Context, items []AdamHallItem) (AdamHallReviewedCheckout, error) {
	f.adamHallCheckoutMu.Lock()
	defer f.adamHallCheckoutMu.Unlock()
	items, err := normalizeAdamHallItems(items)
	if err != nil {
		return AdamHallReviewedCheckout{}, err
	}
	if !f.AdamHallConfigured() {
		return AdamHallReviewedCheckout{}, errors.New("Adam-Hall-Zugangsdaten sind nicht konfiguriert")
	}
	token, err := f.loginAdamHall(ctx)
	if err != nil {
		return AdamHallReviewedCheckout{}, err
	}
	var payload adamHallCartPayload
	if err := f.adamHallJSON(ctx, http.MethodGet, "/checkout/cart", token, nil, &payload); err != nil {
		return AdamHallReviewedCheckout{}, err
	}
	if len(payload.LineItems) > 0 {
		if err := validateAdamHallCart(items, adamHallCartFromPayload(payload, adamHallContextPayload{}).Lines); err != nil {
			return AdamHallReviewedCheckout{}, errors.New("Vorhandener Adam-Hall-Warenkorb weicht von der bestätigten Bestellung ab. Im Lieferantenkonto prüfen; vorhandene Positionen werden nicht gelöscht.")
		}
		for _, line := range payload.LineItems {
			if strings.TrimSpace(line.Payload.ProductNumber) == "" {
				return AdamHallReviewedCheckout{}, errors.New("Zusätzliche Adam-Hall-Warenkorbpositionen müssen im Lieferantenkonto geprüft werden")
			}
		}
	} else {
		var fastOrder struct {
			Success bool `json:"success"`
		}
		if err := f.adamHallJSON(ctx, http.MethodPost, "/checkout/fastOrder", token, map[string]any{"items": items}, &fastOrder); err != nil {
			return AdamHallReviewedCheckout{}, err
		}
		if !fastOrder.Success {
			return AdamHallReviewedCheckout{}, errors.New("Adam Hall konnte den bestätigten Warenkorb nicht vollständig anlegen")
		}
		if err := f.adamHallJSON(ctx, http.MethodGet, "/checkout/cart", token, nil, &payload); err != nil {
			return AdamHallReviewedCheckout{}, err
		}
	}
	if err := f.selectAdamHallShipping(ctx, token, &payload); err != nil {
		return AdamHallReviewedCheckout{}, err
	}
	if err := f.adamHallJSON(ctx, http.MethodPost, "/checkout/cart", token, map[string]any{"isConfirm": true, "isPartialDelivery": true, "isAluCut2m": false}, &payload); err != nil {
		return AdamHallReviewedCheckout{}, err
	}
	if err := adamHallBlockingCartError(payload.Errors); err != nil {
		return AdamHallReviewedCheckout{}, err
	}
	var contextPayload adamHallContextPayload
	if err := f.adamHallJSON(ctx, http.MethodGet, "/context", token, nil, &contextPayload); err != nil {
		return AdamHallReviewedCheckout{}, err
	}
	cart := adamHallBusinessCart(payload, contextPayload)
	if err := validateAdamHallReviewedCart(items, cart); err != nil {
		return AdamHallReviewedCheckout{}, err
	}
	return AdamHallReviewedCheckout{Cart: cart, ContextToken: token}, nil
}

// SubmitReviewedAdamHallCheckout reads and checks the same private cart context;
// it never rebuilds a cart or silently accepts changed prices or destinations.
func (f *Fetcher) SubmitReviewedAdamHallCheckout(ctx context.Context, token string, items []AdamHallItem, reviewed AdamHallCart, customerComment string) (AdamHallOrder, error) {
	f.adamHallCheckoutMu.Lock()
	defer f.adamHallCheckoutMu.Unlock()
	if strings.TrimSpace(token) == "" {
		return AdamHallOrder{}, errors.New("Geprüfter Adam-Hall-Kontext fehlt")
	}
	var payload adamHallCartPayload
	if err := f.adamHallJSON(ctx, http.MethodGet, "/checkout/cart", token, nil, &payload); err != nil {
		return AdamHallOrder{}, err
	}
	if err := adamHallBlockingCartError(payload.Errors); err != nil {
		return AdamHallOrder{}, err
	}
	var contextPayload adamHallContextPayload
	if err := f.adamHallJSON(ctx, http.MethodGet, "/context", token, nil, &contextPayload); err != nil {
		return AdamHallOrder{}, err
	}
	cart := adamHallBusinessCart(payload, contextPayload)
	if err := validateAdamHallReviewedCart(items, cart); err != nil {
		return AdamHallOrder{}, err
	}
	if !reflect.DeepEqual(reviewed, cart) {
		return AdamHallOrder{}, errors.New("Adam-Hall-Positionen, Preise, Lieferadresse oder Zahlungs-/Versandart haben sich geändert. Es wurde keine Bestellung abgesendet.")
	}
	var outcome struct {
		OrderNumber string `json:"orderNumber"`
	}
	if err := f.adamHallJSON(ctx, http.MethodPost, "/checkout/order", token, map[string]any{"customerComment": strings.TrimSpace(customerComment), "affiliateCode": "ProcurementCore", "campaignCode": "", "isAluCut2m": false, "isPartialDelivery": true}, &outcome); err != nil {
		return AdamHallOrder{}, &AdamHallSubmissionUncertainError{err: err}
	}
	if strings.TrimSpace(outcome.OrderNumber) == "" || len(outcome.OrderNumber) > 120 {
		return AdamHallOrder{}, &AdamHallSubmissionUncertainError{err: errors.New("keine gültige Lieferanten-Bestellnummer bestätigt")}
	}
	return AdamHallOrder{OrderNumber: strings.TrimSpace(outcome.OrderNumber), Cart: cart}, nil
}

func adamHallBusinessCart(payload adamHallCartPayload, ctx adamHallContextPayload) AdamHallCart {
	// Exclude customer names/email and payment details. Only a complete business
	// delivery address and human-readable payment/shipping method are reviewed.
	if ctx.Customer != nil {
		ctx.Customer.FirstName = ""
		ctx.Customer.LastName = ""
		ctx.Customer.Email = ""
		if ctx.Customer.ActiveShippingAddress != nil {
			ctx.Customer.ActiveShippingAddress.FirstName = ""
			ctx.Customer.ActiveShippingAddress.LastName = ""
		}
		for _, address := range []*adamHallAddressPayload{ctx.Customer.ActiveBillingAddress, ctx.Customer.DefaultBillingAddress} {
			if address != nil {
				address.FirstName = ""
				address.LastName = ""
			}
		}
	}
	cart := adamHallCartFromPayload(payload, ctx)
	cart.Customer = "Konfiguriertes Geschäftskonto"
	businessAddress := func(address *adamHallAddressPayload) string {
		if address == nil || strings.TrimSpace(address.Company) == "" || strings.TrimSpace(address.Street) == "" || strings.TrimSpace(address.City) == "" || strings.TrimSpace(address.Zipcode) == "" || address.Country == nil || strings.TrimSpace(address.Country.Name) == "" {
			return ""
		}
		parts := []string{address.Company, address.Street, address.Additional, address.Zipcode + " " + address.City, address.Country.Name}
		clean := parts[:0]
		for _, part := range parts {
			if value := strings.TrimSpace(part); value != "" {
				clean = append(clean, value)
			}
		}
		return strings.Join(clean, ", ")
	}
	if ctx.Customer != nil {
		cart.ShippingAddress = businessAddress(ctx.Customer.ActiveShippingAddress)
		billing := ctx.Customer.ActiveBillingAddress
		if billing == nil {
			billing = ctx.Customer.DefaultBillingAddress
		}
		cart.BillingAddress = businessAddress(billing)
	}
	// Bind the actual account/address/method IDs even if their human labels match.
	// The context's private customer/contact/extension fields remain excluded.
	raw, _ := json.Marshal(map[string]any{"customer": ctx.Customer, "currency": ctx.Currency, "shipping_method": ctx.ShippingMethod, "payment_method": ctx.PaymentMethod})
	hash := sha256.Sum256(raw)
	cart.ReviewFingerprint = hex.EncodeToString(hash[:])
	return cart
}

func validateAdamHallReviewedCart(items []AdamHallItem, cart AdamHallCart) error {
	items, err := normalizeAdamHallItems(items)
	if err != nil {
		return err
	}
	if err := validateAdamHallCart(items, cart.Lines); err != nil {
		return err
	}
	if len(cart.Lines) < 1 || len(cart.Lines) > 100 || cart.Currency != "EUR" || cart.TotalCents < 0 || cart.TotalCents > 100000000000 || strings.TrimSpace(cart.ShippingAddress) == "" || strings.TrimSpace(cart.BillingAddress) == "" || len(cart.ReviewFingerprint) != 64 || strings.TrimSpace(cart.PaymentMethod) == "" || strings.TrimSpace(cart.ShippingMethod) == "" {
		return errors.New("Vollständiger begrenzter EUR-Warenkorb, Geschäfts-Liefer- und Rechnungsadresse und Zahlungs-/Versandart erforderlich")
	}
	for _, line := range cart.Lines {
		if line.Quantity < 1 || line.Quantity > 999 || line.UnitPriceCents < 0 || line.UnitPriceCents > 1000000000 || line.TotalCents < 0 {
			return errors.New("Ungültige Adam-Hall-Warenkorbposition")
		}
	}
	raw, _ := json.Marshal(cart)
	if len(raw) > 256<<10 {
		return errors.New("Adam-Hall-Geschäftsvorschau überschreitet die Größenbegrenzung")
	}
	return nil
}
