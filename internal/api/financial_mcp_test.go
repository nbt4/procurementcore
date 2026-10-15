package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"procurementcore/internal/auth"

	"github.com/golang-jwt/jwt/v5"
	commonjwt "github.com/nbt4/cores-common/pkg/jwt"
)

func financialTestToken(t *testing.T, action string, financial any) string {
	t.Helper()
	claims := jwt.MapClaims{"uid": 7, "username": "financial-fixture", "is_admin": true, "exp": time.Now().Add(time.Hour).Unix()}
	if action != "" {
		claims["mcp_scope"] = "cores:procurement:" + action
	}
	if financial != nil {
		claims["mcp_procurement_financial"] = financial
	}
	raw, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(commonjwt.JWTSecret())
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func financialTestAuth(next http.Handler) http.Handler {
	return auth.Middleware(func(context.Context, uint) (commonjwt.User, error) {
		return commonjwt.User{ID: 7, Username: "financial-fixture", IsActive: true, IsAdmin: true}, nil
	}, next)
}

func serveFinancialTestRequest(t *testing.T, fn http.HandlerFunc, w http.ResponseWriter, r *http.Request, action string, financial bool) {
	t.Helper()
	t.Setenv("CORES_JWT_SECRET", "synthetic-procurement-financial-test-secret")
	r.AddCookie(&http.Cookie{Name: "cores_token", Value: financialTestToken(t, action, financial)})
	financialTestAuth(fn).ServeHTTP(w, r)
}

func TestMCPFinancialRoutesRejectBeforeDataOrReplay(t *testing.T) {
	t.Setenv("CORES_JWT_SECRET", "synthetic-procurement-financial-test-secret")
	// A nil database and scraper prove denial precedes all SQL, stored receipts,
	// supplier calls and import parsing. Each native route uses real auth middleware.
	h := financialTestAuth((&Handler{}).Routes())
	routes := []struct{ method, path, action, body string }{
		{"POST", "/mcp/master-data/offers/archive", "archive", "{}"},
		{"POST", "/mcp/master-data/offers/restore", "archive", "{}"},
		{"POST", "/mcp/requisitions/create", "create", "{}"},
		{"POST", "/mcp/requisitions/update", "update", "{}"},
		{"POST", "/mcp/requisitions/submit", "submit", "{}"},
		{"POST", "/mcp/order-drafts/create", "create", "{}"},
		{"POST", "/mcp/order-drafts/update", "update", "{}"},
		{"POST", "/mcp/workflows/requisitions/archive", "archive", "{}"},
		{"POST", "/mcp/workflows/orders/restore", "archive", "{}"},
		{"POST", "/mcp/approvals/requisitions", "approve", "{}"},
		{"POST", "/mcp/approvals/orders", "approve", "{}"},
		{"POST", "/mcp/requisition-orders", "create", "{}"},
		{"POST", "/mcp/orders/receive", "receive", "{}"},
		{"POST", "/mcp/orders/send-amazon", "send", "{}"},
		{"POST", "/mcp/orders/reconcile-submission", "send", "{}"},
		{"POST", "/mcp/orders/adam-hall/build-cart", "send", "{}"},
		{"POST", "/products/1/offers", "create", "{}"},
		{"PUT", "/offers/1", "update", "{}"},
		{"POST", "/products/import-preview", "create", `{"url":"https://example.invalid/item"}`},
		{"POST", "/products", "create", `{"sku":"TEST","name":"Synthetic","initialOffer":{"priceCents":0}}`},
		{"POST", "/requisitions", "create", "{}"},
		{"PUT", "/requisitions/1", "update", "{}"},
		{"POST", "/requisitions/1/submit", "submit", "{}"},
		{"POST", "/requisitions/1/decision", "approve", "{}"},
		{"POST", "/requisitions/1/order", "create", "{}"},
		{"POST", "/orders", "create", "{}"},
		{"PUT", "/orders/1/draft", "update", "{}"},
		{"PUT", "/orders/1", "approve", "{}"},
		{"POST", "/orders/1/receipt", "receive", "{}"},
		{"POST", "/orders/1/adam-hall/cart", "send", "{}"},
		{"POST", "/orders/1/adam-hall/order", "send", "{}"},
		{"POST", "/orders/1/amazon/submit", "send", "{}"},
		{"GET", "/orders/1", "update", ""},
		{"GET", "/products/1/offers", "create", ""},
	}
	for _, route := range routes {
		for _, financial := range []any{nil, false, "true", 1, []any{true}, map[string]any{"allow": true}} {
			for _, transport := range []string{"cookie", "bearer-empty-cookie", "bearer-origin"} {
				t.Run(route.method+route.path+"/"+transport+"/"+stringify(financial), func(t *testing.T) {
					req := httptest.NewRequest(route.method, route.path, strings.NewReader(route.body))
					token := financialTestToken(t, route.action, financial)
					if transport == "cookie" {
						req.AddCookie(&http.Cookie{Name: "cores_token", Value: token})
					} else {
						req.Header.Set("Authorization", "Bearer "+token)
						req.AddCookie(&http.Cookie{Name: "cores_token", Value: ""})
						if transport == "bearer-origin" {
							req.Header.Set("X-Cores-Origin", "MCP/AI")
						}
					}
					req.Header.Set("Idempotency-Key", "previous-successful-financial-request")
					response := httptest.NewRecorder()
					h.ServeHTTP(response, req)
					if response.Code != http.StatusForbidden {
						t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
					}
					for _, field := range []string{"priceCents", "totalCents", "unitPriceCents"} {
						if strings.Contains(response.Body.String(), field) {
							t.Fatal("financial response leaked")
						}
					}
				})
			}
		}
	}
}

func stringify(v any) string { raw, _ := json.Marshal(v); return string(raw) }

func TestMCPDelegationAndNativeSessionShareTokenSelection(t *testing.T) {
	t.Setenv("CORES_JWT_SECRET", "synthetic-procurement-financial-test-secret")
	for _, tc := range []struct {
		name, action                 string
		financial                    any
		wantFinancial, wantDelegated bool
	}{
		{"native session", "", nil, true, false},
		{"separate signed financial grant", "create", true, true, true},
		{"missing financial grant", "create", nil, false, true},
		{"explicitly denied", "create", false, false, true},
		{"malformed financial string", "create", "true", false, true},
		{"malformed financial number", "create", 1, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest("POST", "/", nil)
			req.Header.Set("Authorization", "Bearer "+financialTestToken(t, tc.action, tc.financial))
			req.AddCookie(&http.Cookie{Name: "cores_token", Value: ""})
			financialTestAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if isMCPMutation(r) != tc.wantDelegated {
					t.Fatal("authentication and delegation disagree")
				}
				if got := requireMCPFinancialDelegation(w, r, "create"); got != tc.wantFinancial {
					t.Fatal("financial decision", got)
				}
			})).ServeHTTP(httptest.NewRecorder(), req)
		})
	}
}

func TestUnguidedFinancialRoutesRejectEvenWithFinancialGrant(t *testing.T) {
	t.Setenv("CORES_JWT_SECRET", "synthetic-procurement-financial-test-secret")
	h := financialTestAuth((&Handler{}).Routes())
	for _, route := range []struct{ method, path string }{{"POST", "/alerts"}, {"PUT", "/alerts/1"}, {"DELETE", "/alerts/1"}, {"POST", "/requisitions/from-offer"}, {"POST", "/requisitions/offer-preview"}, {"POST", "/amazon/punchout/start"}, {"POST", "/orders/import-preview"}} {
		req := httptest.NewRequest(route.method, route.path, strings.NewReader("{}"))
		req.Header.Set("Authorization", "Bearer "+financialTestToken(t, "create", true))
		req.AddCookie(&http.Cookie{Name: "cores_token", Value: ""})
		response := httptest.NewRecorder()
		h.ServeHTTP(response, req)
		if response.Code != 403 || !strings.Contains(response.Body.String(), "guided_mcp_workflow_required") {
			t.Fatalf("%s: %d %s", route.path, response.Code, response.Body.String())
		}
	}
}

func TestMalformedDelegationsCannotFallBackToNativeAuthorization(t *testing.T) {
	t.Setenv("CORES_JWT_SECRET", "synthetic-procurement-financial-test-secret")
	for _, scope := range []any{"", nil, 1, []any{"cores:procurement:create"}, map[string]any{"action": "create"}} {
		claims := jwt.MapClaims{"uid": 7, "username": "financial-fixture", "mcp_scope": scope, "exp": time.Now().Add(time.Hour).Unix()}
		raw, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(commonjwt.JWTSecret())
		if err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest("POST", "/products/import-preview", strings.NewReader("{}"))
		req.Header.Set("Authorization", "Bearer "+raw)
		response := httptest.NewRecorder()
		financialTestAuth((&Handler{}).Routes()).ServeHTTP(response, req)
		if response.Code != 403 {
			t.Fatalf("malformed scope %v: %d %s", scope, response.Code, response.Body.String())
		}
	}
	for _, tc := range []struct {
		name   string
		method jwt.SigningMethod
		expiry bool
	}{{"missing expiry", jwt.SigningMethodHS256, false}, {"different HMAC", jwt.SigningMethodHS384, true}} {
		t.Run(tc.name, func(t *testing.T) {
			claims := jwt.MapClaims{"uid": 7, "username": "financial-fixture", "mcp_scope": "cores:procurement:create", "mcp_procurement_financial": true}
			if tc.expiry {
				claims["exp"] = time.Now().Add(time.Hour).Unix()
			}
			raw, err := jwt.NewWithClaims(tc.method, claims).SignedString(commonjwt.JWTSecret())
			if err != nil {
				t.Fatal(err)
			}
			req := httptest.NewRequest("POST", "/products/import-preview", strings.NewReader("{}"))
			req.Header.Set("Authorization", "Bearer "+raw)
			response := httptest.NewRecorder()
			financialTestAuth((&Handler{}).Routes()).ServeHTTP(response, req)
			if response.Code != 403 && response.Code != 401 {
				t.Fatalf("invalid MCP token fell back to UI: %d %s", response.Code, response.Body.String())
			}
		})
	}
}
