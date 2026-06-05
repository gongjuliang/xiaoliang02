package auth

import (
	"regexp"
	"strings"
	"testing"
)

func TestHashPasswordAndCheckPassword(t *testing.T) {
	password := "Example1234"
	hash, err := HashPassword(password)
	if err != nil {
		t.Fatalf("hash password: %v", err)
	}
	if hash == "" || hash == password {
		t.Fatalf("password was not hashed: %q", hash)
	}
	if !regexp.MustCompile(`^[A-Za-z0-9]{8}\$[A-Za-z0-9+/=]+$`).MatchString(hash) {
		t.Fatalf("hash format=%q", hash)
	}
	if strings.HasPrefix(hash, "$2") {
		t.Fatalf("hash must not use bcrypt format: %q", hash)
	}
	if !CheckPassword(password, hash) {
		t.Fatal("expected password to match hash")
	}
	if CheckPassword("wrong-password", hash) {
		t.Fatal("expected wrong password to be rejected")
	}
	if CheckPassword(password, "$2a$10$7EqJtq98hPqEX7fNZaFWoOhiAZu0kt5AwRccDzTQINJ1k.Q6dGkEq") {
		t.Fatal("expected legacy bcrypt hash to be rejected")
	}
}
