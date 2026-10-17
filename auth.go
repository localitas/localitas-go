package client

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"strings"
	"sync"
)

// TokenInfo is the identity core vouches for on a request it forwards to an
// app: the user, and the permission their role and groups give them.
type TokenInfo struct {
	UserID     string `json:"user_id"`
	Email      string `json:"email"`
	Name       string `json:"name"`
	Permission string `json:"permission,omitempty"`
}

// identityKeyDomain matches the domain core derives its token signing key
// under from the cluster API token.
const identityKeyDomain = "localitas-internal-token-v1"

var (
	ErrUnsignedToken   = errors.New("identity token is not signed")
	ErrBadSignature    = errors.New("identity token signature does not verify")
	ErrNoIdentityKey   = errors.New("no identity key: set LOCALITAS_API_TOKEN or call SetIdentityKeyFromAPIToken")
	identityKeyMu      sync.RWMutex
	identityKey        []byte
	identityKeyDerived bool
)

// SetIdentityKeyFromAPIToken sets the key identity tokens are verified with
// to the one core derives from its API token. Apps normally need not call
// it: the key is derived from LOCALITAS_API_TOKEN, or the token in
// ~/.localitas/config-core.yaml, on first use.
func SetIdentityKeyFromAPIToken(apiToken string) {
	identityKeyMu.Lock()
	defer identityKeyMu.Unlock()
	identityKey = deriveIdentityKey(apiToken)
	identityKeyDerived = true
}

func deriveIdentityKey(apiToken string) []byte {
	if apiToken == "" {
		return nil
	}
	sum := sha256.Sum256([]byte(identityKeyDomain + ":" + apiToken))
	return []byte(base64.StdEncoding.EncodeToString(sum[:]))
}

func currentIdentityKey() []byte {
	identityKeyMu.RLock()
	key, derived := identityKey, identityKeyDerived
	identityKeyMu.RUnlock()
	if derived {
		return key
	}
	token := os.Getenv("LOCALITAS_API_TOKEN")
	if token == "" {
		token = DefaultToken()
	}
	SetIdentityKeyFromAPIToken(token)
	return deriveIdentityKey(token)
}

// ParseBearerToken verifies an identity token core put on a request it
// forwarded ("payload.signature": base64url JSON claims and their
// HMAC-SHA256 under the key core derives from its API token) and returns
// its claims. Unsigned, tampered and wrong-key tokens are rejected, so a
// caller cannot claim to be another user.
func ParseBearerToken(token string) (TokenInfo, error) {
	key := currentIdentityKey()
	if len(key) == 0 {
		return TokenInfo{}, ErrNoIdentityKey
	}
	payloadB64, sig, ok := strings.Cut(token, ".")
	if !ok || payloadB64 == "" || sig == "" {
		return TokenInfo{}, ErrUnsignedToken
	}
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(payloadB64))
	if !hmac.Equal([]byte(sig), []byte(base64.RawURLEncoding.EncodeToString(mac.Sum(nil)))) {
		return TokenInfo{}, ErrBadSignature
	}
	payload, err := base64.RawURLEncoding.DecodeString(payloadB64)
	if err != nil {
		return TokenInfo{}, err
	}
	var info TokenInfo
	if err := json.Unmarshal(payload, &info); err != nil {
		return TokenInfo{}, err
	}
	return info, nil
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
