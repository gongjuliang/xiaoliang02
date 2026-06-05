package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"strings"

	"github.com/emmansun/gmsm/sm3"
)

const saltAlphabet = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"

func HashPassword(password string) (string, error) {
	salt, err := randomSalt(8)
	if err != nil {
		return "", err
	}
	return salt + "$" + sm3Digest(salt, password), nil
}

func CheckPassword(password string, hash string) bool {
	parts := strings.Split(hash, "$")
	if len(parts) != 2 || len(parts[0]) != 8 || parts[1] == "" {
		return false
	}
	expected := sm3Digest(parts[0], password)
	return subtle.ConstantTimeCompare([]byte(expected), []byte(parts[1])) == 1
}

func IsCurrentPasswordHash(hash string) bool {
	parts := strings.Split(hash, "$")
	if len(parts) != 2 || len(parts[0]) != 8 || parts[1] == "" {
		return false
	}
	for _, ch := range parts[0] {
		if !strings.ContainsRune(saltAlphabet, ch) {
			return false
		}
	}
	return true
}

func sm3Digest(salt string, input string) string {
	encoded := base64.StdEncoding.EncodeToString([]byte(salt + input))
	sum := sm3.Sum([]byte(encoded))
	return base64.StdEncoding.EncodeToString(sum[:])
}

func randomSalt(length int) (string, error) {
	if length <= 0 {
		return "", fmt.Errorf("salt length must be positive")
	}
	buf := make([]byte, length)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate salt: %w", err)
	}
	for i := range buf {
		buf[i] = saltAlphabet[int(buf[i])%len(saltAlphabet)]
	}
	return string(buf), nil
}
