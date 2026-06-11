package client

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"
)

// Scope represents an authorization level.
// The hierarchy is: Guest < Read < Write < Admin.
type Scope string

const (
	ScopeGuest Scope = ""
	ScopeRead  Scope = "read"
	ScopeWrite Scope = "write"
	ScopeAdmin Scope = "admin"
)

type contextKey string

const (
	ctxUserID     contextKey = "user_id"
	ctxEmail      contextKey = "user_email"
	ctxName       contextKey = "user_name"
	ctxPermission contextKey = "user_permission"
)

func scopeRank(s Scope) int {
	switch s {
	case ScopeAdmin:
		return 3
	case ScopeWrite:
		return 2
	case ScopeRead:
		return 1
	default:
		return 0
	}
}

// HasScope returns true if the user's scope meets or exceeds the required scope.
func HasScope(userScope, required Scope) bool {
	return scopeRank(userScope) >= scopeRank(required)
}

// GetScope returns the user's scope from the request context.
func GetScope(ctx context.Context) Scope {
	if s, ok := ctx.Value(ctxPermission).(Scope); ok {
		return s
	}
	if GetUserID(ctx) != "" {
		return ScopeRead
	}
	return ScopeGuest
}

// GetUserID returns the user ID from the request context.
func GetUserID(ctx context.Context) string {
	v, _ := ctx.Value(ctxUserID).(string)
	return v
}

// GetEmail returns the user email from the request context.
func GetEmail(ctx context.Context) string {
	v, _ := ctx.Value(ctxEmail).(string)
	return v
}

// GetName returns the user name from the request context.
func GetName(ctx context.Context) string {
	v, _ := ctx.Value(ctxName).(string)
	return v
}

// AuthMiddleware parses the Localitas bearer token and sets user identity
// and scope in the request context. Rejects unauthenticated requests with 401.
//
// The bearer token is a base64-encoded JSON payload:
//
//	{"user_id": "...", "email": "...", "name": "...", "permission": "write"}
//
// The permission field is set by the Localitas platform proxy based on the
// user's role and group memberships.
func AuthMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authHeader := r.Header.Get("Authorization")
		if authHeader == "" || !strings.HasPrefix(authHeader, "Bearer ") {
			http.Error(w, `{"error":"authorization required"}`, http.StatusUnauthorized)
			return
		}

		token := strings.TrimPrefix(authHeader, "Bearer ")
		decoded, err := base64.StdEncoding.DecodeString(token)
		if err != nil {
			decoded, err = base64.RawStdEncoding.DecodeString(token)
			if err != nil {
				http.Error(w, `{"error":"invalid token"}`, http.StatusUnauthorized)
				return
			}
		}

		var claims struct {
			UserID     string `json:"user_id"`
			Email      string `json:"email"`
			Name       string `json:"name"`
			Permission string `json:"permission"`
		}
		if err := json.Unmarshal(decoded, &claims); err != nil {
			http.Error(w, `{"error":"invalid token payload"}`, http.StatusUnauthorized)
			return
		}

		if claims.Email == "" && claims.UserID == "" {
			http.Error(w, `{"error":"invalid token: no user identity"}`, http.StatusUnauthorized)
			return
		}

		ctx := context.WithValue(r.Context(), ctxUserID, claims.UserID)
		ctx = context.WithValue(ctx, ctxEmail, claims.Email)
		ctx = context.WithValue(ctx, ctxName, claims.Name)
		if claims.Permission != "" {
			ctx = context.WithValue(ctx, ctxPermission, Scope(claims.Permission))
		}

		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// RequireScope returns a middleware that enforces a minimum scope level.
// If the user's scope is lower than required, it responds with 403 Forbidden.
//
// Usage:
//
//	mux.Handle("POST /api/items", client.RequireScope(client.ScopeWrite)(handleCreate))
//	mux.Handle("GET /api/items", client.RequireScope(client.ScopeRead)(handleList))
//	mux.Handle("DELETE /api/admin/reset", client.RequireScope(client.ScopeAdmin)(handleReset))
func RequireScope(scope Scope) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			userScope := GetScope(r.Context())
			if !HasScope(userScope, scope) {
				http.Error(w, `{"error":"insufficient permission"}`, http.StatusForbidden)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// RequireScopeFunc is the same as RequireScope but wraps an http.HandlerFunc.
//
// Usage:
//
//	mux.HandleFunc("POST /api/items", client.RequireScopeFunc(client.ScopeWrite, handleCreate))
func RequireScopeFunc(scope Scope, handler http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		userScope := GetScope(r.Context())
		if !HasScope(userScope, scope) {
			http.Error(w, `{"error":"insufficient permission"}`, http.StatusForbidden)
			return
		}
		handler(w, r)
	}
}
