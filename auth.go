package client

import (
	"context"
	"crypto/ed25519"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

// TokenInfo is the identity core vouches for on a request it forwards to an
// app: the user, the permission their role and groups give them, and the
// token's lifetime.
type TokenInfo struct {
	UserID     string `json:"user_id"`
	Email      string `json:"email"`
	Name       string `json:"name"`
	Permission string `json:"permission,omitempty"`
	IssuedAt   int64  `json:"iat,omitempty"`
	ExpiresAt  int64  `json:"exp,omitempty"`
	MaxExpiry  int64  `json:"max,omitempty"`
	KeyID      string `json:"kid,omitempty"`
	Audience   string `json:"aud,omitempty"`
}

const (
	// identityClockSkew is the clock difference tolerated with core.
	identityClockSkew = 120
	// refreshBeforeSeconds is how close to its expiry a client refreshes
	// the identity token it acts with.
	refreshBeforeSeconds = 60
	ed25519KeyIDPrefix   = "ed25519-"
	keyFetchRetry        = 30 * time.Second
)

var (
	ErrUnsignedToken    = errors.New("identity token is not signed")
	ErrBadSignature     = errors.New("identity token signature does not verify")
	ErrNoIdentityKey    = errors.New("no identity key: set LOCALITAS_IDENTITY_PUBLIC_KEY")
	ErrTokenExpired     = errors.New("identity token expired")
	ErrTokenNotYetValid = errors.New("identity token issued in the future")
	ErrLegacyToken      = errors.New("legacy identity tokens are no longer accepted")

	// identityNow is the clock identity tokens are checked against.
	identityNow = time.Now
)

// identity holds what identity tokens are verified with: core's Ed25519
// public keys, by key ID.
var identity struct {
	mu sync.Mutex

	keys       map[string]ed25519.PublicKey
	keysSet    bool // set explicitly, from the environment, or fetched from core
	lastFetch  time.Time
	fetchedKey bool
}

// Ed25519KeyID names an identity public key as core does: a prefix and the
// base64url of the first 9 bytes of its SHA-256.
func Ed25519KeyID(pub ed25519.PublicKey) string {
	sum := sha256.Sum256(pub)
	return ed25519KeyIDPrefix + base64.RawURLEncoding.EncodeToString(sum[:9])
}

func decodePublicKey(s string) (ed25519.PublicKey, error) {
	s = strings.TrimSpace(s)
	for _, enc := range []*base64.Encoding{base64.RawURLEncoding, base64.URLEncoding, base64.RawStdEncoding, base64.StdEncoding} {
		if b, err := enc.DecodeString(s); err == nil {
			if len(b) != ed25519.PublicKeySize {
				return nil, fmt.Errorf("identity public key must be %d bytes, got %d", ed25519.PublicKeySize, len(b))
			}
			return ed25519.PublicKey(b), nil
		}
	}
	return nil, errors.New("identity public key is not base64")
}

func parsePublicKeys(keys []string) (map[string]ed25519.PublicKey, error) {
	out := map[string]ed25519.PublicKey{}
	for _, k := range keys {
		if strings.TrimSpace(k) == "" {
			continue
		}
		pub, err := decodePublicKey(k)
		if err != nil {
			return nil, err
		}
		out[Ed25519KeyID(pub)] = pub
	}
	return out, nil
}

// SetIdentityPublicKeys sets the Ed25519 public keys (base64url) identity
// tokens are verified with, instead of LOCALITAS_IDENTITY_PUBLIC_KEY or the
// key core serves. With none, no token verifies.
func SetIdentityPublicKeys(keys ...string) error {
	parsed, err := parsePublicKeys(keys)
	if err != nil {
		return err
	}
	identity.mu.Lock()
	defer identity.mu.Unlock()
	identity.keys, identity.keysSet = parsed, true
	return nil
}

// ResetIdentityPublicKeys goes back to resolving the public keys from
// LOCALITAS_IDENTITY_PUBLIC_KEY (comma-separated), or else from core's
// GET /api/auth/identity/public-key.
func ResetIdentityPublicKeys() {
	identity.mu.Lock()
	defer identity.mu.Unlock()
	identity.keys, identity.keysSet, identity.lastFetch, identity.fetchedKey = nil, false, time.Time{}, false
}

// systemToken is the API token core gave the app; core's automation calls
// the app's cron and event routes with it.
func systemToken() string {
	if t := os.Getenv("LOCALITAS_API_TOKEN"); t != "" {
		return t
	}
	return DefaultToken()
}

// IsSystemRequest reports whether r carries the API token core gave the app,
// i.e. comes from core itself (scheduled jobs, event deliveries) rather than
// from a user.
func IsSystemRequest(r *http.Request) bool {
	want := systemToken()
	got := TokenFromRequest(r)
	return want != "" && got != "" && hmac.Equal([]byte(got), []byte(want))
}

// RequireSystem admits only requests from core itself (see
// IsSystemRequest): routes that act across every user's data, like the
// app's cron jobs, must not be callable by a user.
func RequireSystem(handler http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !IsSystemRequest(r) {
			http.Error(w, `{"error":"only core may call this route"}`, http.StatusForbidden)
			return
		}
		handler(w, r)
	}
}

type identityPublicKeyResponse struct {
	Alg       string `json:"alg"`
	KeyID     string `json:"kid"`
	PublicKey string `json:"public_key"`
}

// fetchIdentityKey asks core for its identity public key. Called with
// identity.mu held.
func fetchIdentityKey() {
	token := systemToken()
	if token == "" || time.Since(identity.lastFetch) < keyFetchRetry {
		return
	}
	identity.lastFetch = time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, DefaultCoreURL()+"/api/auth/identity/public-key", nil)
	if err != nil {
		return
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := defaultHTTPClient.Do(req)
	if err != nil {
		return
	}
	defer resp.Body.Close()
	var out identityPublicKeyResponse
	if resp.StatusCode != http.StatusOK || json.NewDecoder(resp.Body).Decode(&out) != nil {
		return
	}
	identity.fetchedKey = true
	if out.PublicKey == "" {
		return
	}
	if keys, err := parsePublicKeys([]string{out.PublicKey}); err == nil {
		identity.keys, identity.keysSet = keys, true
	}
}

// resolveIdentity returns the public keys to verify with, resolving them from
// the environment or core on first use.
func resolveIdentity() map[string]ed25519.PublicKey {
	identity.mu.Lock()
	defer identity.mu.Unlock()
	if !identity.keysSet {
		if env := os.Getenv("LOCALITAS_IDENTITY_PUBLIC_KEY"); env != "" {
			if keys, err := parsePublicKeys(strings.Split(env, ",")); err == nil {
				identity.keys, identity.keysSet = keys, true
			}
		} else if !identity.fetchedKey {
			fetchIdentityKey()
		}
	}
	return identity.keys
}

// ParseBearerToken verifies an identity token core put on a request it
// forwarded ("payload.signature": base64url JSON claims and their signature)
// and returns its claims. Core signs with its Ed25519 key, whose public key
// the app gets in LOCALITAS_IDENTITY_PUBLIC_KEY or from core. Unsigned,
// tampered and wrong-key tokens are rejected, so a caller cannot claim to be
// another user; so are tokens expired (beyond two minutes of clock skew) or
// issued in the future. Legacy tokens — signed with anything but an Ed25519
// key, or without an expiry — are always refused: every app holds the API
// token the old HMAC key was derived from, so it could forge them.
func ParseBearerToken(token string) (TokenInfo, error) {
	payloadB64, sigB64, ok := strings.Cut(token, ".")
	if !ok || payloadB64 == "" || sigB64 == "" {
		return TokenInfo{}, ErrUnsignedToken
	}
	payload, err := base64.RawURLEncoding.DecodeString(payloadB64)
	if err != nil {
		return TokenInfo{}, err
	}
	var info TokenInfo
	if err := json.Unmarshal(payload, &info); err != nil {
		return TokenInfo{}, err
	}

	if !strings.HasPrefix(info.KeyID, ed25519KeyIDPrefix) {
		return TokenInfo{}, ErrLegacyToken
	}
	keys := resolveIdentity()
	pub, known := keys[info.KeyID]
	if !known {
		if len(keys) == 0 {
			return TokenInfo{}, ErrNoIdentityKey
		}
		return TokenInfo{}, ErrBadSignature
	}
	sig, err := base64.RawURLEncoding.DecodeString(sigB64)
	if err != nil || !ed25519.Verify(pub, []byte(payloadB64), sig) {
		return TokenInfo{}, ErrBadSignature
	}

	if info.ExpiresAt == 0 {
		return TokenInfo{}, ErrLegacyToken
	}
	now := identityNow().Unix()
	switch {
	case now > info.ExpiresAt+identityClockSkew:
		return TokenInfo{}, ErrTokenExpired
	case info.IssuedAt > now+identityClockSkew:
		return TokenInfo{}, ErrTokenNotYetValid
	}
	return info, nil
}

// unverifiedClaims reads an identity token's claims without verifying it,
// to learn when a token the client itself holds expires.
func unverifiedClaims(token string) (TokenInfo, bool) {
	payloadB64, _, ok := strings.Cut(token, ".")
	if !ok {
		return TokenInfo{}, false
	}
	payload, err := base64.RawURLEncoding.DecodeString(payloadB64)
	if err != nil {
		return TokenInfo{}, false
	}
	var info TokenInfo
	return info, json.Unmarshal(payload, &info) == nil
}

// TokenFromRequest extracts the bearer token from a request's Authorization header.
// Returns empty string if the header is missing or malformed.
func TokenFromRequest(r *http.Request) string {
	auth := r.Header.Get("Authorization")
	if auth == "" || !strings.HasPrefix(auth, "Bearer ") {
		return ""
	}
	return strings.TrimPrefix(auth, "Bearer ")
}

// UserIDFromRequest returns the user core vouches for on the request, or ""
// when the token is missing or does not verify.
func UserIDFromRequest(r *http.Request) string {
	token := TokenFromRequest(r)
	if token == "" {
		return ""
	}
	info, err := ParseBearerToken(token)
	if err != nil {
		return ""
	}
	return info.UserID
}
