package store

import (
	"crypto/rand"
	"encoding/base64"
)

const keyPrefixLen = 8

// GenerateKey returns a new raw API key: "gw_" followed by 32 random
// bytes, base64url-encoded. It is shown to the caller exactly once — see
// Store.Create — and never persisted; only its hash is (see hashKey).
func GenerateKey() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return "gw_" + base64.RawURLEncoding.EncodeToString(buf), nil
}

// keyPrefix returns the first few characters of a raw key, safe to store
// and display so an operator can recognize a key in a list without it
// being enough to reconstruct or brute-force the real thing.
func keyPrefix(raw string) string {
	if len(raw) <= keyPrefixLen {
		return raw
	}
	return raw[:keyPrefixLen]
}
