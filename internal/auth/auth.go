package auth

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	commonjwt "github.com/nbt4/cores-common/pkg/jwt"
)

type User struct {
	ID       uint   `json:"userId"`
	Username string `json:"username"`
	IsAdmin  bool   `json:"isAdmin"`
}

type contextKey string

const userKey contextKey = "procurement-user"

func Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := ""
		if cookie, err := r.Cookie("cores_token"); err == nil {
			token = cookie.Value
		}
		if token == "" && strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
			token = strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		}
		claims, ok := commonjwt.ValidateToken(token)
		if !ok {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "authentication required"})
			return
		}
		user := User{ID: claims.UserID, Username: claims.Username, IsAdmin: claims.IsAdmin}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), userKey, user)))
	})
}

func RequireAdmin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !CurrentUser(r).IsAdmin {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusForbidden)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "administrator permission required"})
			return
		}
		next.ServeHTTP(w, r)
	})
}

func CurrentUser(r *http.Request) User {
	user, _ := r.Context().Value(userKey).(User)
	return user
}
