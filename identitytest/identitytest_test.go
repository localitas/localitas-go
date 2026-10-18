package identitytest

import (
	"errors"
	"testing"
	"time"

	client "github.com/localitas/localitas-go"
)

func TestSigner_TokensVerifyWithTheSDK(t *testing.T) {
	s := New(t)
	info, err := client.ParseBearerToken(s.Token(client.TokenInfo{UserID: "bob", Email: "bob@test.local", Permission: "write"}))
	if err != nil {
		t.Fatal(err)
	}
	if info.UserID != "bob" || info.Permission != "write" {
		t.Fatalf("claims %+v", info)
	}
	expired := s.Token(client.TokenInfo{UserID: "bob", IssuedAt: time.Now().Add(-time.Hour).Unix(), ExpiresAt: time.Now().Add(-30 * time.Minute).Unix()})
	if _, err := client.ParseBearerToken(expired); !errors.Is(err, client.ErrTokenExpired) {
		t.Fatalf("expired token: %v", err)
	}
}
