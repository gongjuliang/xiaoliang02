package auth

import (
	"crypto/elliptic"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/emmansun/gmsm/sm2"
	"github.com/emmansun/gmsm/smx509"
)

type SM2Cipher struct {
	privateKey *sm2.PrivateKey
	publicPEM  string
}

func NewSM2Cipher(privateKeyFile string, publicKeyFile string) (*SM2Cipher, error) {
	// The server owns the SM2 private key and exposes only the public key to the
	// browser; generating it on first boot keeps local development self-contained.
	privateKey, err := loadOrCreateSM2PrivateKey(privateKeyFile)
	if err != nil {
		return nil, err
	}
	publicPEM, err := marshalPublicKeyPEM(privateKey)
	if err != nil {
		return nil, err
	}
	if publicKeyFile != "" {
		if err := writeFileIfChanged(publicKeyFile, []byte(publicPEM), 0o644); err != nil {
			return nil, err
		}
	}
	return &SM2Cipher{
		privateKey: privateKey,
		publicPEM:  publicPEM,
	}, nil
}

func (c *SM2Cipher) PublicKeyPEM() string {
	return c.publicPEM
}

func (c *SM2Cipher) PublicKeyHex() string {
	raw := elliptic.Marshal(c.privateKey.Curve, c.privateKey.X, c.privateKey.Y)
	return hex.EncodeToString(raw)
}

func (c *SM2Cipher) DecryptToString(ciphertext string) (string, error) {
	raw, err := decodeCiphertext(ciphertext)
	if err != nil {
		return "", err
	}
	plain, err := sm2.Decrypt(c.privateKey, raw)
	if err != nil {
		return "", fmt.Errorf("sm2 decrypt: %w", err)
	}
	return string(plain), nil
}

func loadOrCreateSM2PrivateKey(path string) (*sm2.PrivateKey, error) {
	if content, err := os.ReadFile(path); err == nil {
		return parsePrivateKeyPEM(content)
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("read sm2 private key: %w", err)
	}

	privateKey, err := sm2.GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate sm2 private key: %w", err)
	}
	content, err := marshalPrivateKeyPEM(privateKey)
	if err != nil {
		return nil, err
	}
	if err := writeFileIfChanged(path, content, 0o600); err != nil {
		return nil, err
	}
	return privateKey, nil
}

func parsePrivateKeyPEM(content []byte) (*sm2.PrivateKey, error) {
	block, _ := pem.Decode(content)
	if block == nil {
		return nil, fmt.Errorf("invalid sm2 private key pem")
	}
	privateKey, err := smx509.ParseSM2PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse sm2 private key: %w", err)
	}
	return privateKey, nil
}

func marshalPrivateKeyPEM(privateKey *sm2.PrivateKey) ([]byte, error) {
	der, err := smx509.MarshalSM2PrivateKey(privateKey)
	if err != nil {
		return nil, fmt.Errorf("marshal sm2 private key: %w", err)
	}
	return pem.EncodeToMemory(&pem.Block{
		Type:  "SM2 PRIVATE KEY",
		Bytes: der,
	}), nil
}

func marshalPublicKeyPEM(privateKey *sm2.PrivateKey) (string, error) {
	der, err := smx509.MarshalPKIXPublicKey(&privateKey.PublicKey)
	if err != nil {
		return "", fmt.Errorf("marshal sm2 public key: %w", err)
	}
	return string(pem.EncodeToMemory(&pem.Block{
		Type:  "PUBLIC KEY",
		Bytes: der,
	})), nil
}

func decodeCiphertext(ciphertext string) ([]byte, error) {
	value := strings.TrimSpace(ciphertext)
	if value == "" {
		return nil, fmt.Errorf("ciphertext is required")
	}
	if raw, err := base64.StdEncoding.DecodeString(value); err == nil {
		return raw, nil
	}
	if raw, err := base64.RawStdEncoding.DecodeString(value); err == nil {
		return raw, nil
	}
	if raw, err := hex.DecodeString(value); err == nil {
		return raw, nil
	}
	return nil, fmt.Errorf("ciphertext must be base64 or hex")
}

func writeFileIfChanged(path string, content []byte, perm os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create key dir: %w", err)
	}
	if existing, err := os.ReadFile(path); err == nil && string(existing) == string(content) {
		return nil
	}
	if err := os.WriteFile(path, content, perm); err != nil {
		return fmt.Errorf("write key file %s: %w", path, err)
	}
	return nil
}
