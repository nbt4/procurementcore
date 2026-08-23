package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestLogoutHandlerExpiresSharedCookie(t *testing.T) {
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/v1/auth/logout", nil)

	logoutHandler(".tsunami-events.de").ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusOK)
	}
	cookies := recorder.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("cookies = %d, want 1", len(cookies))
	}
	cookie := cookies[0]
	if cookie.Name != "cores_token" || cookie.Domain != "tsunami-events.de" || cookie.Path != "/" {
		t.Fatalf("unexpected cookie: %#v", cookie)
	}
	if cookie.MaxAge != -1 || !cookie.HttpOnly || !cookie.Secure || cookie.SameSite != http.SameSiteLaxMode {
		t.Fatalf("logout cookie does not match SSO settings: %#v", cookie)
	}
	if !strings.Contains(recorder.Body.String(), `"success":true`) {
		t.Fatalf("body = %q", recorder.Body.String())
	}
}
