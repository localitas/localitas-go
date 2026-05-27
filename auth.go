package client

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"
)

type TokenInfo struct {
	UserID string `json:"user_id"`
	Email  string `json:"email"`
	Name   string `json:"name"`
}

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

func TokenFromRequest(r *http.Request) string {
	auth := r.Header.Get("Authorization")
	if auth == "" || !strings.HasPrefix(auth, "Bearer ") {
		return ""
	}
	return strings.TrimPrefix(auth, "Bearer ")
}

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
