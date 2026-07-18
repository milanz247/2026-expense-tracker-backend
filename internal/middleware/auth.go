// Package middleware contains HTTP middleware shared across handlers.
package middleware

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/milan/expense-tracker/backend/pkg/auth"
)

// AuthCookieName is the name of the httpOnly cookie the Next.js frontend
// stores the backend-issued JWT in. Must match AUTH_COOKIE_NAME in the
// frontend's src/lib/constants.ts.
const AuthCookieName = "auth_token"

type contextKey int

const userIDContextKey contextKey = iota

// RequireAuth returns middleware that authenticates a request using the
// JWT found in either the "Authorization: Bearer <token>" header or the
// AuthCookieName cookie (checked in that order), and injects the token's
// UserID into the request context for downstream handlers. Requests with
// a missing, malformed, or expired token are rejected with 401 before
// ever reaching the wrapped handler.
func RequireAuth(tokens *auth.TokenManager) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			tokenString, ok := extractToken(r)
			if !ok {
				writeUnauthorized(w, "missing authentication token")
				return
			}

			claims, err := tokens.ParseToken(tokenString)
			if err != nil {
				writeUnauthorized(w, "invalid or expired authentication token")
				return
			}

			ctx := context.WithValue(r.Context(), userIDContextKey, claims.UserID)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// UserIDFromContext retrieves the authenticated user's ID previously
// injected by RequireAuth. ok is false if called with a context that
// never passed through the middleware.
func UserIDFromContext(ctx context.Context) (userID uint, ok bool) {
	userID, ok = ctx.Value(userIDContextKey).(uint)
	return userID, ok
}

// extractToken looks for a bearer token first (used by server-to-server
// calls, e.g. Server Components fetching directly from the Go backend),
// then falls back to the auth cookie (used by same-origin browser
// requests that carry it automatically).
func extractToken(r *http.Request) (string, bool) {
	const bearerPrefix = "Bearer "
	if header := r.Header.Get("Authorization"); strings.HasPrefix(header, bearerPrefix) {
		if token := strings.TrimPrefix(header, bearerPrefix); token != "" {
			return token, true
		}
	}

	if cookie, err := r.Cookie(AuthCookieName); err == nil && cookie.Value != "" {
		return cookie.Value, true
	}

	return "", false
}

func writeUnauthorized(w http.ResponseWriter, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusUnauthorized)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": message})
}
