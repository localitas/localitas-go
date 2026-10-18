package client

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// What core signs at identityT0 under the Ed25519 seed of 32 bytes of 7;
// core's internal/auth golden test pins the same strings.
const (
	goldenEd25519PublicKey = "6kpsY-KcUgq-9VB7Ey7F-ZVHdq6-vnuSQh7qaRRG0iw"
	goldenEd25519Token     = "eyJlbWFpbCI6ImFsaWNlQHRlc3QubG9jYWwiLCJuYW1lIjoiQWxpY2UiLCJ1c2VyX2lkIjoidTEiLCJwZXJtaXNzaW9uIjoid3JpdGUiLCJpYXQiOjE3OTIyMzg0MDAsImV4cCI6MTc5MjIzODcwMCwibWF4IjoxNzkyMzI0ODAwLCJraWQiOiJlZDI1NTE5LV9vRXNFdk9yVE9hcyJ9.C8hu4KQIKHVrA_NyuOnESy33xUYWVqZWDyrONgSpvaTPxCU_aa7xf1N9POyND7eOnwGTh0Pffo71Y-mB3yO9Dg"
)

var identityT0 = time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)

func goldenSeed() []byte {
	seed := make([]byte, ed25519.SeedSize)
	for i := range seed {
		seed[i] = 7
	}
	return seed
}

// atTime pins the clock identity tokens are checked against.
func atTime(t *testing.T, now *time.Time) {
	t.Helper()
	prev := identityNow
	identityNow = func() time.Time { return *now }
	t.Cleanup(func() { identityNow = prev })
}

func withPublicKeys(t *testing.T, keys ...string) {
	t.Helper()
	if err := SetIdentityPublicKeys(keys...); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(restoreTestIdentity)
}

func withLegacy(t *testing.T, accept bool) {
	t.Helper()
	SetAcceptLegacyIdentityTokens(accept)
	t.Cleanup(restoreTestIdentity)
}

// signEd25519 signs info as core does with the Ed25519 key of seed.
func signEd25519(seed []byte, info TokenInfo) string {
	priv := ed25519.NewKeyFromSeed(seed)
	info.KeyID = Ed25519KeyID(priv.Public().(ed25519.PublicKey))
	raw, _ := json.Marshal(info)
	payload := base64.RawURLEncoding.EncodeToString(raw)
	return payload + "." + base64.RawURLEncoding.EncodeToString(ed25519.Sign(priv, []byte(payload)))
}

func freshInfo(now time.Time, ttl time.Duration) TokenInfo {
	return TokenInfo{UserID: "u1", Email: "alice@test.local", Name: "Alice", Permission: "write",
		IssuedAt: now.Unix(), ExpiresAt: now.Add(ttl).Unix(), MaxExpiry: now.Add(24 * time.Hour).Unix()}
}

func TestParseBearerToken_VerifiesCoreEd25519Token(t *testing.T) {
	now := identityT0
	atTime(t, &now)
	withPublicKeys(t, goldenEd25519PublicKey)
	got, err := ParseBearerToken(goldenEd25519Token)
	if err != nil {
		t.Fatalf("core's token does not verify: %v", err)
	}
	if got.UserID != "u1" || got.Permission != "write" || got.ExpiresAt != identityT0.Add(5*time.Minute).Unix() {
		t.Fatalf("claims = %+v", got)
	}
}

func TestParseBearerToken_RejectsExpiredAndFutureTokens(t *testing.T) {
	now := identityT0
	atTime(t, &now)
	withPublicKeys(t, goldenEd25519PublicKey)

	now = identityT0.Add(5*time.Minute + 119*time.Second)
	if _, err := ParseBearerToken(goldenEd25519Token); err != nil {
		t.Fatalf("within skew after exp: %v", err)
	}
	now = identityT0.Add(5*time.Minute + 121*time.Second)
	if _, err := ParseBearerToken(goldenEd25519Token); !errors.Is(err, ErrTokenExpired) {
		t.Fatalf("past exp+skew: got %v, want ErrTokenExpired", err)
	}
	now = identityT0.Add(-121 * time.Second)
	if _, err := ParseBearerToken(goldenEd25519Token); !errors.Is(err, ErrTokenNotYetValid) {
		t.Fatalf("iat beyond skew ahead: got %v, want ErrTokenNotYetValid", err)
	}
}

func TestParseBearerToken_RejectsAnotherClustersKey(t *testing.T) {
	now := identityT0
	atTime(t, &now)
	withPublicKeys(t, goldenEd25519PublicKey)
	other := make([]byte, ed25519.SeedSize)
	if _, err := ParseBearerToken(signEd25519(other, freshInfo(now, 5*time.Minute))); err == nil {
		t.Fatal("a token signed with another key verified")
	}
}

// An app can compute the legacy HMAC key from the api_token it holds, so
// HMAC tokens — and tokens without an expiry — stop verifying once legacy
// tokens are refused.
func TestParseBearerToken_LegacyTokensOnlyWhileAccepted(t *testing.T) {
	now := identityT0
	atTime(t, &now)
	withPublicKeys(t, goldenEd25519PublicKey)
	withLegacy(t, true)
	withExpiry := signTestToken(testAPIToken, freshInfo(now, 5*time.Minute))
	for name, token := range map[string]string{"unexpiring": goldenIdentityToken, "hmac with exp": withExpiry} {
		if _, err := ParseBearerToken(token); err != nil {
			t.Fatalf("%s legacy token while accepted: %v", name, err)
		}
	}

	SetAcceptLegacyIdentityTokens(false)
	for name, token := range map[string]string{"unexpiring": goldenIdentityToken, "hmac with exp": withExpiry} {
		if _, err := ParseBearerToken(token); !errors.Is(err, ErrLegacyToken) {
			t.Fatalf("%s legacy token once refused: got %v, want ErrLegacyToken", name, err)
		}
	}
	if _, err := ParseBearerToken(goldenEd25519Token); err != nil {
		t.Fatalf("core's Ed25519 token: %v", err)
	}
}

func TestParseBearerToken_PublicKeyAndPolicyFromEnvironment(t *testing.T) {
	now := identityT0
	atTime(t, &now)
	t.Setenv("LOCALITAS_IDENTITY_PUBLIC_KEY", goldenEd25519PublicKey)
	t.Setenv("LOCALITAS_ACCEPT_LEGACY_IDENTITY_TOKENS", "false")
	ResetIdentityPublicKeys()
	ResetAcceptLegacyIdentityTokens()
	t.Cleanup(restoreTestIdentity)
	t.Cleanup(restoreTestIdentity)

	if _, err := ParseBearerToken(goldenEd25519Token); err != nil {
		t.Fatalf("token under the environment's key: %v", err)
	}
	if _, err := ParseBearerToken(goldenIdentityToken); !errors.Is(err, ErrLegacyToken) {
		t.Fatalf("legacy token with LOCALITAS_ACCEPT_LEGACY_IDENTITY_TOKENS=false: got %v", err)
	}
}

// An app on the host, without LOCALITAS_IDENTITY_PUBLIC_KEY, asks core for
// the key with its api_token.
func TestParseBearerToken_FetchesThePublicKeyFromCore(t *testing.T) {
	now := identityT0
	atTime(t, &now)
	var asked string
	core := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/auth/identity/public-key" {
			http.NotFound(w, r)
			return
		}
		asked = r.Header.Get("Authorization")
		json.NewEncoder(w).Encode(map[string]any{"alg": "EdDSA", "public_key": goldenEd25519PublicKey, "accept_legacy": false})
	}))
	defer core.Close()
	t.Setenv("LOCALITAS_IDENTITY_PUBLIC_KEY", "")
	t.Setenv("LOCALITAS_ACCEPT_LEGACY_IDENTITY_TOKENS", "")
	t.Setenv("CORE_URL", core.URL)
	t.Setenv("LOCALITAS_API_TOKEN", testAPIToken)
	ResetIdentityPublicKeys()
	ResetAcceptLegacyIdentityTokens()
	t.Cleanup(restoreTestIdentity)
	t.Cleanup(restoreTestIdentity)

	if _, err := ParseBearerToken(goldenEd25519Token); err != nil {
		t.Fatalf("token under the key core serves: %v", err)
	}
	if asked != "Bearer "+testAPIToken {
		t.Fatalf("asked core with %q, want the api_token", asked)
	}
	if _, err := ParseBearerToken(goldenIdentityToken); !errors.Is(err, ErrLegacyToken) {
		t.Fatalf("legacy token after core said accept_legacy=false: got %v", err)
	}
}

// A client acting with an identity token refreshes it with core shortly
// before it expires, and sends the refreshed one.
func TestClient_RefreshesAnIdentityTokenNearExpiry(t *testing.T) {
	now := identityT0
	atTime(t, &now)
	withPublicKeys(t, goldenEd25519PublicKey)
	original := signEd25519(goldenSeed(), freshInfo(now, 5*time.Minute))
	refreshed := signEd25519(goldenSeed(), freshInfo(now.Add(4*time.Minute+30*time.Second), 5*time.Minute))

	var refreshes int
	var sent []string
	core := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bearer := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if r.URL.Path == "/api/auth/identity/refresh" {
			refreshes++
			if bearer != original {
				http.Error(w, "unexpected token", http.StatusUnauthorized)
				return
			}
			json.NewEncoder(w).Encode(map[string]any{"token": refreshed})
			return
		}
		sent = append(sent, bearer)
		w.Write([]byte(`{}`))
	}))
	defer core.Close()

	c := New(core.URL).WithToken(original)
	if err := c.Do(context.Background(), "GET", "/apps/notes/api/notes", nil, nil); err != nil {
		t.Fatal(err)
	}
	now = identityT0.Add(4*time.Minute + 30*time.Second)
	if err := c.Do(context.Background(), "GET", "/apps/notes/api/notes", nil, nil); err != nil {
		t.Fatal(err)
	}
	if refreshes != 1 || len(sent) != 2 || sent[0] != original || sent[1] != refreshed {
		t.Fatalf("refreshes %d, sent fresh-then-refreshed: %v", refreshes, len(sent) == 2 && sent[0] == original && sent[1] == refreshed)
	}
}

func TestClient_IdentityForUser(t *testing.T) {
	var got identityForUserRequest
	var bearer string
	core := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.Path != "/api/auth/identity/for-user" {
			http.NotFound(w, r)
			return
		}
		bearer = r.Header.Get("Authorization")
		json.NewDecoder(r.Body).Decode(&got)
		json.NewEncoder(w).Encode(map[string]any{"token": "minted", "expires_at": 1})
	}))
	defer core.Close()

	token, err := New(core.URL).WithToken(testAPIToken).IdentityForUser(context.Background(), "bob", ScopeWrite, 2*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if token != "minted" || bearer != "Bearer "+testAPIToken || got.UserID != "bob" || got.Permission != "write" || got.TTLSeconds != 120 {
		t.Fatalf("token %q, bearer %q, request %+v", token, bearer, got)
	}
}
