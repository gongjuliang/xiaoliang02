package auth

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
)

func GenerateClientSecret() (string, error) {
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("generate client secret: %w", err)
	}
	return "xiaoliang_" + base64.RawURLEncoding.EncodeToString(raw[:]), nil
}

func SecretHint(secret string) string {
	if len(secret) <= 10 {
		return secret
	}
	return secret[:6] + "..." + secret[len(secret)-6:]
}
