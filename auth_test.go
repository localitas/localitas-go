package client

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"testing"
)

func TestParseBearerToken_ValidToken(t *testing.T) {
	info := TokenInfo{UserID: "u123", Email: "alice@example.com", Name: "Alice"}
	raw, err := json.Marshal(info)
	if err != nil {
		t.Fatal(err)
	}
	encoded := base64.StdEncoding.EncodeToString(raw)

	got, err := ParseBearerToken(encoded)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.UserID != "u123" {
		t.Errorf("UserID = %q, want %q", got.UserID, "u123")
	}
	if got.Email != "alice@example.com" {
		t.Errorf("Email = %q, want %q", got.Email, "alice@example.com")
	}
	if got.Name != "Alice" {
		t.Errorf("Name = %q, want %q", got.Name, "Alice")
	}
}

func TestTokenFromRequest_ValidHeader(t *testing.T) {
	req, _ := http.NewRequest("GET", "/test", nil)
	req.Header.Set("Authorization", "Bearer my-secret-token")

	got := TokenFromRequest(req)
	if got != "my-secret-token" {
		t.Errorf("got %q, want %q", got, "my-secret-token")
	}
}

func TestTokenFromRequest_MissingHeader(t *testing.T) {
	req, _ := http.NewRequest("GET", "/test", nil)

	got := TokenFromRequest(req)
	if got != "" {
		t.Errorf("expected empty string, got %q", got)
	}
}

func TestTokenFromRequest_EmptyHeader(t *testing.T) {
	req, _ := http.NewRequest("GET", "/test", nil)
	req.Header.Set("Authorization", "")

	got := TokenFromRequest(req)
	if got != "" {
		t.Errorf("expected empty string, got %q", got)
	}
}

func TestUserIDFromRequest_ValidToken(t *testing.T) {
	info := TokenInfo{UserID: "user-42", Email: "bob@example.com", Name: "Bob"}
	raw, _ := json.Marshal(info)
	encoded := base64.StdEncoding.EncodeToString(raw)

	req, _ := http.NewRequest("GET", "/test", nil)
	req.Header.Set("Authorization", "Bearer "+encoded)

	got := UserIDFromRequest(req)
	if got != "user-42" {
		t.Errorf("got %q, want %q", got, "user-42")
	}
}
