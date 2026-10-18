// Package identitytest signs identity tokens the way core does, so tests of
// an app that verifies them with the localitas-go SDK (AuthMiddleware,
// ParseBearerToken) can act as any user.
package identitytest

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"testing"
	"time"

	client "github.com/localitas/localitas-go"
)

// Signer holds a test identity key the SDK verifies tokens with.
type Signer struct {
	priv ed25519.PrivateKey
}

// New makes a fresh identity key the SDK verifies with for the rest of t,
// and refuses legacy (HMAC or unexpiring) tokens meanwhile, as core does
// once they are switched off.
func New(t testing.TB) *Signer {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	if err := client.SetIdentityPublicKeys(base64.RawURLEncoding.EncodeToString(pub)); err != nil {
		t.Fatal(err)
	}
	client.SetAcceptLegacyIdentityTokens(false)
	t.Cleanup(func() {
		client.ResetIdentityPublicKeys()
		client.ResetAcceptLegacyIdentityTokens()
	})
	return &Signer{priv: priv}
}

// Token signs info as core would: issued now and valid for five minutes
// unless info says otherwise.
func (s *Signer) Token(info client.TokenInfo) string {
	now := time.Now()
	if info.IssuedAt == 0 {
		info.IssuedAt = now.Unix()
	}
	if info.ExpiresAt == 0 {
		info.ExpiresAt = now.Add(5 * time.Minute).Unix()
	}
	if info.MaxExpiry == 0 {
		info.MaxExpiry = now.Add(24 * time.Hour).Unix()
	}
	info.KeyID = client.Ed25519KeyID(s.priv.Public().(ed25519.PublicKey))
	raw, _ := json.Marshal(info)
	payload := base64.RawURLEncoding.EncodeToString(raw)
	return payload + "." + base64.RawURLEncoding.EncodeToString(ed25519.Sign(s.priv, []byte(payload)))
}

// PublicKey is the base64url public key the SDK verifies with, as core puts
// it in LOCALITAS_IDENTITY_PUBLIC_KEY.
func (s *Signer) PublicKey() string {
	return base64.RawURLEncoding.EncodeToString(s.priv.Public().(ed25519.PublicKey))
}
