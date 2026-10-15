package api

import (
	"encoding/json"
	"net/http"

	"procurementcore/internal/auth"

	"github.com/golang-jwt/jwt/v5"
	commonjwt "github.com/nbt4/cores-common/pkg/jwt"
)

type procurementDelegationClaims struct {
	UID       uint            `json:"uid"`
	Scope     json.RawMessage `json:"mcp_scope"`
	Financial any             `json:"mcp_procurement_financial"`
	jwt.RegisteredClaims
}

func procurementDelegation(r *http.Request) (*procurementDelegationClaims, bool) {
	claims := &procurementDelegationClaims{}
	token, err := jwt.ParseWithClaims(auth.SessionToken(r), claims, func(*jwt.Token) (any, error) { return commonjwt.JWTSecret(), nil }, jwt.WithValidMethods([]string{"HS256"}), jwt.WithExpirationRequired())
	valid := err == nil && token.Valid && claims.UID != 0 && claims.UID == auth.CurrentUser(r).ID && len(claims.Scope) != 0
	return claims, valid
}

// Financial rights are a separately signed grant. Native UI sessions retain
// their normal Core authorization; origin headers cannot manufacture a grant.
func requireMCPFinancialDelegation(w http.ResponseWriter, r *http.Request, action string) bool {
	if !isMCPMutation(r) {
		return true
	}
	claims, valid := procurementDelegation(r)
	if !valid || !claims.financialGranted() || action != "" && claims.actionScope() != "cores:procurement:"+action {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "Signed real-user action and explicit cores:procurement:financial delegation required", "code": "procurement_financial_required"})
		return false
	}
	return true
}

func (h *Handler) requireMCPFinancial(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if requireMCPFinancialDelegation(w, r, "") {
			next.ServeHTTP(w, r)
		}
	})
}

func (h *Handler) rejectUnguidedMCP(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isMCPMutation(r) {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "This native route has no guided MCP workflow", "code": "guided_mcp_workflow_required"})
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (claims *procurementDelegationClaims) financialGranted() bool {
	granted, _ := claims.Financial.(bool)
	return granted
}

func (claims *procurementDelegationClaims) actionScope() string {
	var scope string
	_ = json.Unmarshal(claims.Scope, &scope)
	return scope
}
