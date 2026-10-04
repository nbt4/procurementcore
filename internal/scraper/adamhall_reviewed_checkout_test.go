package scraper

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
)

type reviewedCheckoutTransport func(*http.Request) (*http.Response, error)

func (fn reviewedCheckoutTransport) RoundTrip(r *http.Request) (*http.Response, error) { return fn(r) }

func TestReviewedAdamHallCheckoutRetainsOtherCartAndExactPaidReview(t *testing.T) {
	var clearCalls, fastOrderCalls, orderCalls atomic.Int32
	f := newAdamHallCheckoutTestFetcher(t, &clearCalls, &fastOrderCalls, &orderCalls)
	items := []AdamHallItem{{ProductNumber: "8747X6", Quantity: 2}}
	if _, err := f.PrepareAdamHallReviewedCheckout(t.Context(), items); err == nil || !strings.Contains(err.Error(), "nicht gelöscht") || clearCalls.Load() != 0 || fastOrderCalls.Load() != 0 || orderCalls.Load() != 0 {
		t.Fatal("unrelated remote cart changed", err)
	}
	original := f.adamHallClient.Transport
	var changedPrice, changedMethod atomic.Bool
	f.adamHallClient.Transport = reviewedCheckoutTransport(func(r *http.Request) (*http.Response, error) {
		response, err := original.RoundTrip(r)
		if err != nil {
			return response, err
		}
		if r.Method != http.MethodGet || (r.URL.Path != "/context" && r.URL.Path != "/checkout/cart") {
			return response, nil
		}
		raw, err := io.ReadAll(response.Body)
		_ = response.Body.Close()
		if err != nil {
			return nil, err
		}
		body := map[string]any{}
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Fatal(err)
		}
		if r.URL.Path == "/checkout/cart" {
			if fastOrderCalls.Load() == 0 {
				body["lineItems"] = []any{}
			} else if changedPrice.Load() {
				line := body["lineItems"].([]any)[0].(map[string]any)
				price := line["price"].(map[string]any)
				price["unitPrice"] = 13.35
				price["totalPrice"] = 26.70
				body["price"].(map[string]any)["totalPrice"] = 26.70
			}
		} else if customer, ok := body["customer"].(map[string]any); ok {
			customer["id"] = "business-account-id"
			customer["activeBillingAddress"] = customer["activeShippingAddress"]
			body["shippingMethod"].(map[string]any)["id"] = "shipping-method-id"
			id := "invoice-method-id"
			if changedMethod.Load() {
				id = "different-invoice-method-with-same-label"
			}
			body["paymentMethod"].(map[string]any)["id"] = id
		}
		raw, _ = json.Marshal(body)
		response.Body = io.NopCloser(bytes.NewReader(raw))
		response.ContentLength = int64(len(raw))
		return response, nil
	})
	quote, err := f.PrepareAdamHallReviewedCheckout(t.Context(), items)
	if err != nil || quote.ContextToken == "" || quote.Cart.TotalCents != 2470 || clearCalls.Load() != 0 || fastOrderCalls.Load() != 1 || orderCalls.Load() != 0 {
		t.Fatal("confirmed cart stage", quote, err)
	}
	raw, _ := json.Marshal(quote.Cart)
	for _, private := range []string{"buyer@example.com", "Ada Buyer", "shop-secret", "authenticated-context"} {
		if strings.Contains(string(raw), private) {
			t.Fatal("private checkout data leaked", private)
		}
	}
	if quote.Cart.BillingAddress == "" || len(quote.Cart.ReviewFingerprint) != 64 {
		t.Fatal("complete business destinations/context", quote.Cart)
	}
	changedPrice.Store(true)
	if _, err := f.SubmitReviewedAdamHallCheckout(t.Context(), quote.ContextToken, items, quote.Cart, "PO-reviewed"); err == nil || orderCalls.Load() != 0 {
		t.Fatal("changed price ordered", err)
	}
	changedPrice.Store(false)
	changedMethod.Store(true)
	if _, err := f.SubmitReviewedAdamHallCheckout(t.Context(), quote.ContextToken, items, quote.Cart, "PO-reviewed"); err == nil || orderCalls.Load() != 0 {
		t.Fatal("same-label different payment method ordered", err)
	}
	changedMethod.Store(false)
	result, err := f.SubmitReviewedAdamHallCheckout(t.Context(), quote.ContextToken, items, quote.Cart, "PO-reviewed")
	if err != nil || result.OrderNumber != "AH-4711" || clearCalls.Load() != 0 || fastOrderCalls.Load() != 1 || orderCalls.Load() != 1 {
		t.Fatal("reviewed cart rebuilt or order mismatch", result, err)
	}
}
