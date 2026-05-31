package auth

import "testing"

func TestHashPasswordAndCheckPassword(t *testing.T) {
	hash, err := HashPassword("admin123456")
	if err != nil {
		t.Fatalf("hash password: %v", err)
	}
	if hash == "" || hash == "admin123456" {
		t.Fatalf("password was not hashed: %q", hash)
	}
	if !CheckPassword("admin123456", hash) {
		t.Fatal("expected password to match hash")
	}
	if CheckPassword("wrong-password", hash) {
		t.Fatal("expected wrong password to be rejected")
	}
}
