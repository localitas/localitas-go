package client

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"testing"
)

const testAPIToken = "lt_test_cluster_token"

func TestMain(m *testing.M) {
	SetIdentityKeyFromAPIToken(testAPIToken)
	os.Exit(m.Run())
}

// signTestToken signs claims exactly as core does: base64url JSON, a dot,
// and the base64url HMAC-SHA256 of the payload under the key derived from
// the cluster API token.
func signTestToken(apiToken string, info TokenInfo) string {
	raw, _ := json.Marshal(info)
	payload := base64.RawURLEncoding.EncodeToString(raw)
	mac := hmac.New(sha256.New, deriveIdentityKey(apiToken))
	mac.Write([]byte(payload))
	return payload + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func TestParseBearerToken_ValidToken(t *testing.T) {
	encoded := signTestToken(testAPIToken, TokenInfo{UserID: "u123", Email: "alice@example.com", Name: "Alice", Permission: "write"})

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
	if got.Permission != "write" {
		t.Errorf("Permission = %q, want write", got.Permission)
	}
}

func TestParseBearerToken_RejectsForgeries(t *testing.T) {
	claims, _ := json.Marshal(TokenInfo{UserID: "alice"})
	for name, token := range map[string]string{
		"unsigned":  base64.StdEncoding.EncodeToString(claims),
		"other key": signTestToken("someone-else", TokenInfo{UserID: "alice"}),
		"lt_ token": "lt_abc123",
	} {
		if _, err := ParseBearerToken(token); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestParseBearerToken_NoKeyRejectsEverything(t *testing.T) {
	SetIdentityKeyFromAPIToken("")
	t.Cleanup(func() { SetIdentityKeyFromAPIToken(testAPIToken) })
	if _, err := ParseBearerToken(signTestToken(testAPIToken, TokenInfo{UserID: "alice"})); !errors.Is(err, ErrNoIdentityKey) {
		t.Fatalf("got %v, want ErrNoIdentityKey", err)
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
	encoded := signTestToken(testAPIToken, TokenInfo{UserID: "user-42", Email: "bob@example.com", Name: "Bob"})

	req, _ := http.NewRequest("GET", "/test", nil)
	req.Header.Set("Authorization", "Bearer "+encoded)

	got := UserIDFromRequest(req)
	if got != "user-42" {
		t.Errorf("got %q, want %q", got, "user-42")
	}
}

// goldenIdentityToken is what core signs for these claims under
// testAPIToken (core's internal/auth golden test pins the same string).
const goldenIdentityToken = "eyJlbWFpbCI6ImFsaWNlQHRlc3QubG9jYWwiLCJuYW1lIjoiQWxpY2UiLCJ1c2VyX2lkIjoidTEiLCJwZXJtaXNzaW9uIjoid3JpdGUifQ.qpBZu3EVKLK3lKw_8Y2WD_VP9nCqx849D30L0r9fo5Y"

func TestParseBearerToken_VerifiesCoreSignedToken(t *testing.T) {
	got, err := ParseBearerToken(goldenIdentityToken)
	if err != nil {
		t.Fatalf("core's token does not verify: %v", err)
	}
	if got.UserID != "u1" || got.Email != "alice@test.local" || got.Name != "Alice" || got.Permission != "write" {
		t.Fatalf("claims = %+v", got)
	}
}
