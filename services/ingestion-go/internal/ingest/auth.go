package ingest

import (
	"crypto/subtle"
	"errors"
	"net/http"
	"strings"
)

var ErrUnauthorized = errors.New("unauthorized")

type APIKeyEntry struct {
	Key    string
	SiteID string
}

type APIKeyAuth struct {
	keys map[string]string
}

func NewAPIKeyAuth(entries []APIKeyEntry) *APIKeyAuth {
	keys := make(map[string]string, len(entries))
	for _, e := range entries {
		keys[e.Key] = e.SiteID
	}
	return &APIKeyAuth{keys: keys}
}

func (a *APIKeyAuth) Authenticate(r *http.Request) (string, error) {
	key := extractBearerToken(r)
	if key == "" {
		return "", ErrUnauthorized
	}
	for k, siteID := range a.keys {
		if subtle.ConstantTimeCompare([]byte(key), []byte(k)) == 1 {
			return siteID, nil
		}
	}
	return "", ErrUnauthorized
}

func extractBearerToken(r *http.Request) string {
	auth := r.Header.Get("Authorization")
	if auth == "" {
		return ""
	}
	const prefix = "Bearer "
	if len(auth) < len(prefix) || !strings.EqualFold(auth[:len(prefix)], prefix) {
		return ""
	}
	return strings.TrimSpace(auth[len(prefix):])
}
