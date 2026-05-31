package auth

import (
	"crypto/rand"
	"encoding/base64"
	"path/filepath"
	"testing"

	"github.com/emmansun/gmsm/sm2"
)

func TestSM2CipherDecryptsBase64Ciphertext(t *testing.T) {
	dir := t.TempDir()
	cipher, err := NewSM2Cipher(filepath.Join(dir, "sm2_private.pem"), filepath.Join(dir, "sm2_public.pem"))
	if err != nil {
		t.Fatalf("create sm2 cipher: %v", err)
	}
	if cipher.PublicKeyPEM() == "" {
		t.Fatal("expected public key pem")
	}

	encrypted, err := sm2.Encrypt(rand.Reader, &cipher.privateKey.PublicKey, []byte("secret-password"), nil)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}

	plain, err := cipher.DecryptToString(base64.StdEncoding.EncodeToString(encrypted))
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	if plain != "secret-password" {
		t.Fatalf("unexpected plaintext: %s", plain)
	}
}
