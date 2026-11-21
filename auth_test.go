package client

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"
)

const testAPIToken = "lt_test_cluster_token"

func TestMain(m *testing.M) {
	restoreTestIdentity()
	// No test reaches the core of the machine running it through its
	// core.env: the home is empty unless a test writes one.
	home, err := os.MkdirTemp("", "localitas-home-")
	if err != nil {
		panic(err)
	}
	os.Setenv(HomeVar, home)
	code := m.Run()
	os.RemoveAll(home)
	os.Exit(code)
}

// restoreTestIdentity pins the keys tests verify with, so no test asks a
// running core for them.
func restoreTestIdentity() {
	ResetIdentityPublicKeys()
	if err := SetIdentityPublicKeys(goldenEd25519PublicKey); err != nil {
		panic(err)
	}
}

// signTestToken signs claims as core does, with the golden Ed25519 key,
// issued now and valid for five minutes.
func signTestToken(info TokenInfo) string {
	now := identityNow()
	info.IssuedAt, info.ExpiresAt = now.Unix(), now.Add(5*time.Minute).Unix()
	return signEd25519(goldenSeed(), info)
}

// signLegacyHMAC signs claims exactly as core signed legacy identity tokens:
// base64url JSON, a dot, and the base64url HMAC-SHA256 of the payload under
// base64std(sha256("localitas-internal-token-v1:" + apiToken)).
func signLegacyHMAC(apiToken string, info TokenInfo) string {
	sum := sha256.Sum256([]byte("localitas-internal-token-v1:" + apiToken))
	mac := hmac.New(sha256.New, []byte(base64.StdEncoding.EncodeToString(sum[:])))
	raw, _ := json.Marshal(info)
	payload := base64.RawURLEncoding.EncodeToString(raw)
	mac.Write([]byte(payload))
	return payload + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func TestParseBearerToken_ValidToken(t *testing.T) {
	encoded := signTestToken(TokenInfo{UserID: "u123", Email: "alice@example.com", Name: "Alice", Permission: "write"})

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
		"other key": signEd25519(make([]byte, 32), TokenInfo{UserID: "alice", ExpiresAt: time.Now().Add(time.Minute).Unix()}),
		"hmac":      signLegacyHMAC(testAPIToken, TokenInfo{UserID: "alice", ExpiresAt: time.Now().Add(time.Minute).Unix()}),
		"lt_ token": "lt_abc123",
	} {
		if _, err := ParseBearerToken(token); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestParseBearerToken_NoKeyRejectsEverything(t *testing.T) {
	if err := SetIdentityPublicKeys(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(restoreTestIdentity)
	if _, err := ParseBearerToken(signTestToken(TokenInfo{UserID: "alice"})); !errors.Is(err, ErrNoIdentityKey) {
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
	encoded := signTestToken(TokenInfo{UserID: "user-42", Email: "bob@example.com", Name: "Bob"})

	req, _ := http.NewRequest("GET", "/test", nil)
	req.Header.Set("Authorization", "Bearer "+encoded)

	got := UserIDFromRequest(req)
	if got != "user-42" {
		t.Errorf("got %q, want %q", got, "user-42")
	}
}

// goldenIdentityToken is the legacy HMAC token, without an expiry, core
// signed for these claims under testAPIToken.
const goldenIdentityToken = "eyJlbWFpbCI6ImFsaWNlQHRlc3QubG9jYWwiLCJuYW1lIjoiQWxpY2UiLCJ1c2VyX2lkIjoidTEiLCJwZXJtaXNzaW9uIjoid3JpdGUifQ.qpBZu3EVKLK3lKw_8Y2WD_VP9nCqx849D30L0r9fo5Y"

func TestRequireSystem_AdmitsOnlyCoresToken(t *testing.T) {
	t.Setenv("LOCALITAS_API_TOKEN", testAPIToken)
	h := RequireSystem(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	for _, tc := range []struct {
		name, auth string
		want       int
	}{
		{"core's token", "Bearer " + testAPIToken, http.StatusOK},
		{"a user's identity", "Bearer " + signTestToken(TokenInfo{UserID: "alice", Permission: "admin"}), http.StatusForbidden},
		{"another token", "Bearer lt_someone_else", http.StatusForbidden},
		{"none", "", http.StatusForbidden},
	} {
		req, _ := http.NewRequest("POST", "/api/sync-all", nil)
		if tc.auth != "" {
			req.Header.Set("Authorization", tc.auth)
		}
		rec := httptest.NewRecorder()
		h(rec, req)
		if rec.Code != tc.want {
			t.Errorf("%s: got %d, want %d", tc.name, rec.Code, tc.want)
		}
	}
}
