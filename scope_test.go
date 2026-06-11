package client

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestScopeRank(t *testing.T) {
	if scopeRank(ScopeGuest) >= scopeRank(ScopeRead) {
		t.Error("guest should be lower than read")
	}
	if scopeRank(ScopeRead) >= scopeRank(ScopeWrite) {
		t.Error("read should be lower than write")
	}
	if scopeRank(ScopeWrite) >= scopeRank(ScopeAdmin) {
		t.Error("write should be lower than admin")
	}
}

func TestHasScope(t *testing.T) {
	tests := []struct {
		user     Scope
		required Scope
		expected bool
	}{
		{ScopeAdmin, ScopeAdmin, true},
		{ScopeAdmin, ScopeWrite, true},
		{ScopeAdmin, ScopeRead, true},
		{ScopeWrite, ScopeWrite, true},
		{ScopeWrite, ScopeRead, true},
		{ScopeWrite, ScopeAdmin, false},
		{ScopeRead, ScopeRead, true},
		{ScopeRead, ScopeWrite, false},
		{ScopeRead, ScopeAdmin, false},
		{ScopeGuest, ScopeRead, false},
	}

	for _, tt := range tests {
		result := HasScope(tt.user, tt.required)
		if result != tt.expected {
			t.Errorf("HasScope(%q, %q) = %v, want %v", tt.user, tt.required, result, tt.expected)
		}
	}
}

func TestGetScope_FromContext(t *testing.T) {
	ctx := context.WithValue(context.Background(), ctxPermission, ScopeWrite)
	if s := GetScope(ctx); s != ScopeWrite {
		t.Errorf("expected ScopeWrite, got %q", s)
	}
}

func TestGetScope_DefaultsToReadForAuthenticatedUser(t *testing.T) {
	ctx := context.WithValue(context.Background(), ctxUserID, "user-123")
	if s := GetScope(ctx); s != ScopeRead {
		t.Errorf("expected ScopeRead for authenticated user, got %q", s)
	}
}

func TestGetScope_DefaultsToGuestForAnonymous(t *testing.T) {
	if s := GetScope(context.Background()); s != ScopeGuest {
		t.Errorf("expected ScopeGuest for anonymous, got %q", s)
	}
}

func makeToken(userID, email, permission string) string {
	data := map[string]string{
		"user_id":    userID,
		"email":      email,
		"permission": permission,
	}
	b, _ := json.Marshal(data)
	return base64.StdEncoding.EncodeToString(b)
}

func TestAuthMiddleware_SetsScope(t *testing.T) {
	var gotScope Scope
	handler := AuthMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotScope = GetScope(r.Context())
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Authorization", "Bearer "+makeToken("u1", "test@example.com", "write"))
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	if gotScope != ScopeWrite {
		t.Errorf("expected ScopeWrite, got %q", gotScope)
	}
}

func TestAuthMiddleware_RejectsNoToken(t *testing.T) {
	handler := AuthMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest("GET", "/", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", w.Code)
	}
}

func TestRequireScope_Allows(t *testing.T) {
	called := false
	handler := RequireScope(ScopeWrite)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))

	ctx := context.WithValue(context.Background(), ctxPermission, ScopeAdmin)
	req := httptest.NewRequest("GET", "/", nil).WithContext(ctx)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if !called {
		t.Error("handler should have been called")
	}
	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}
}

func TestRequireScope_Rejects(t *testing.T) {
	called := false
	handler := RequireScope(ScopeAdmin)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))

	ctx := context.WithValue(context.Background(), ctxPermission, ScopeRead)
	req := httptest.NewRequest("GET", "/", nil).WithContext(ctx)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if called {
		t.Error("handler should not have been called")
	}
	if w.Code != http.StatusForbidden {
		t.Errorf("expected 403, got %d", w.Code)
	}
}

func TestRequireScopeFunc(t *testing.T) {
	called := false
	handler := RequireScopeFunc(ScopeWrite, func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	})

	ctx := context.WithValue(context.Background(), ctxPermission, ScopeWrite)
	req := httptest.NewRequest("GET", "/", nil).WithContext(ctx)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if !called {
		t.Error("handler should have been called")
	}
}
