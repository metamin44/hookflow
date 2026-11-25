// Package apikey implements HookFlow machine credentials: token generation,
// Argon2id verification, scope interpretation and the api_keys repository.
//
// A raw API key exists only in the operator create command's output. Nothing
// in this package stores, logs or formats a token or a verifier.
package apikey

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
)

// Approved token shape.
const (
	// TokenBytes is the cryptographically random payload length.
	TokenBytes = 32
	// TokenLength is the unpadded base64url length of TokenBytes.
	TokenLength = 43
	// PrefixLength is the non-secret lookup fragment stored in key_prefix.
	PrefixLength = 8
)

// ErrInvalidToken reports a token that does not match the approved format.
var ErrInvalidToken = errors.New("api key token is invalid")

// Generate returns a fresh raw token and its non-secret prefix.
func Generate(reader io.Reader) (token string, prefix string, err error) {
	if reader == nil {
		reader = rand.Reader
	}
	payload := make([]byte, TokenBytes)
	if _, err := io.ReadFull(reader, payload); err != nil {
		return "", "", fmt.Errorf("generate api key: %w", err)
	}
	token = base64.RawURLEncoding.EncodeToString(payload)
	return token, token[:PrefixLength], nil
}

// ParseToken validates the token format and returns the lookup prefix.
func ParseToken(token string) (string, error) {
	if len(token) != TokenLength {
		return "", ErrInvalidToken
	}
	for i := 0; i < len(token); i++ {
		b := token[i]
		if (b >= 'A' && b <= 'Z') || (b >= 'a' && b <= 'z') || (b >= '0' && b <= '9') ||
			b == '-' || b == '_' {
			continue
		}
		return "", ErrInvalidToken
	}
	decoded, err := base64.RawURLEncoding.Strict().DecodeString(token)
	if err != nil || len(decoded) != TokenBytes {
		return "", ErrInvalidToken
	}
	if base64.RawURLEncoding.EncodeToString(decoded) != token {
		return "", ErrInvalidToken
	}
	return token[:PrefixLength], nil
}
