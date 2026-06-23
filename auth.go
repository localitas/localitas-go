package client

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"
)

// TokenInfo contains the identity claims extracted from a Localitas bearer token.
type TokenInfo struct {
	UserID string `json:"user_id"`
	Email  string `json:"email"`
	Name   string `json:"name"`
}

// ParseBearerToken decodes a Localitas bearer token (base64-encoded JSON)
// and returns the identity claims. This is a client-side parse — it does
// not validate the token against the server.
func ParseBearerToken(token string) (TokenInfo, error) {
	decoded, err := base64.StdEncoding.DecodeString(token)
	if err != nil {
		return TokenInfo{}, err
	}
	var info TokenInfo
	if err := json.Unmarshal(decoded, &info); err != nil {
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

// UserIDFromRequest extracts the user ID from a request's bearer token.
// Returns empty string if the token is missing or invalid.
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
