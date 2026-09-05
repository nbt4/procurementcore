package auth

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	jwtlib "github.com/golang-jwt/jwt/v5"
	commonjwt "github.com/nbt4/cores-common/pkg/jwt"
)

func TestRevokedAccessOnExistingSession(t *testing.T) {
	secret := "middleware-test-secret-at-least-32-bytes"
	t.Setenv("CORES_JWT_SECRET", secret)
	raw, err := jwtlib.NewWithClaims(jwtlib.SigningMethodHS256, commonjwt.Claims{
		UserID: 7, IsAdmin: true, RegisteredClaims: jwtlib.RegisteredClaims{ExpiresAt: jwtlib.NewNumericDate(time.Now().Add(time.Hour))},
	}).SignedString([]byte(secret))
	if err != nil {
		t.Fatal(err)
	}
	user := commonjwt.User{ID: 7, Username: "tester", IsActive: true, IsAdmin: true}
	var lookupErr error
	lookup := func(context.Context, uint) (commonjwt.User, error) { return user, lookupErr }
	endpoint := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	handler := Middleware(lookup, RequireAdmin(endpoint))
	check := func(want int) {
		t.Helper()
		request := httptest.NewRequest(http.MethodGet, "/admin", nil)
		request.AddCookie(&http.Cookie{Name: "cores_token", Value: raw})
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != want {
			t.Fatalf("status=%d, want %d", response.Code, want)
		}
	}
	check(http.StatusNoContent)
	user.IsAdmin = false
	check(http.StatusForbidden)
	user.IsActive = false
	check(http.StatusUnauthorized)
	user.IsActive, user.IsAdmin = true, true
	lookupErr = errors.New("account database unavailable")
	check(http.StatusUnauthorized)
}
