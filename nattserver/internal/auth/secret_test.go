package auth

import (
	"encoding/base64"
	"strings"
	"testing"
)

func TestGenerateClientSecretAndVerifyHash(t *testing.T) {
	secret, err := GenerateClientSecret()
	if err != nil {
		t.Fatalf("generate client secret: %v", err)
	}
	if !strings.HasPrefix(secret, "natt_") {
		t.Fatalf("secret prefix=%q", secret)
	}
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(secret, "natt_"))
	if err != nil {
		t.Fatalf("decode secret: %v", err)
	}
	if len(raw) != 32 {
		t.Fatalf("secret raw length=%d", len(raw))
	}

	hash, err := HashPassword(secret)
	if err != nil {
		t.Fatalf("hash client secret: %v", err)
	}
	if !CheckPassword(secret, hash) {
		t.Fatal("expected generated secret to match hash")
	}
	if CheckPassword(secret+"x", hash) {
		t.Fatal("expected wrong client secret to be rejected")
	}
}

func TestSecretHint(t *testing.T) {
	if got := SecretHint("short"); got != "short" {
		t.Fatalf("short hint=%q", got)
	}
	if got := SecretHint("natt_abcdefghijklmnopqrstuvwxyz"); got != "natt_a...uvwxyz" {
		t.Fatalf("long hint=%q", got)
	}
}
