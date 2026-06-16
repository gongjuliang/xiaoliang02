// Package auth 提供SM2国密非对称加密功能。
// SM2是中国国家密码管理局发布的椭圆曲线公钥密码算法，等效于ECC（椭圆曲线加密）。
// 客户端持有私钥用于解密，公钥暴露给浏览器端用于加密敏感数据（如密码），
// 实现端到端的机密性保护。首次启动时自动生成密钥对并持久化到文件。
package auth

import (
	// crypto/elliptic 提供椭圆曲线的标准接口和Marshal函数。
	"crypto/elliptic"
	// crypto/rand 提供密码学安全随机数，用于密钥生成。
	"crypto/rand"
	// encoding/base64 提供Base64编解码，用于密文的文本化传输。
	"encoding/base64"
	// encoding/hex 提供十六进制编解码，用于公钥的Hex格式导出。
	"encoding/hex"
	// encoding/pem 提供PEM格式的密钥编解码。
	"encoding/pem"
	// fmt 提供错误信息的格式化。
	"fmt"
	// os 提供文件系统操作（读取、写入、权限设置）。
	"os"
	// path/filepath 提供文件路径操作（目录创建、路径拼接）。
	"path/filepath"
	// strings 提供字符串修剪功能。
	"strings"

	// github.com/emmansun/gmsm/sm2 提供SM2国密算法实现（密钥生成、加密、解密）。
	"github.com/emmansun/gmsm/sm2"
	// github.com/emmansun/gmsm/smx509 提供SM2密钥的X509序列化和解析。
	"github.com/emmansun/gmsm/smx509"
)

// SM2Cipher SM2加密器，封装私钥和PEM格式公钥，提供加解密和公钥导出功能。
type SM2Cipher struct {
	// privateKey SM2私钥，用于解密客户端使用公钥加密的数据。
	privateKey *sm2.PrivateKey
	// publicPEM PEM编码格式的公钥字符串，可直接传输给浏览器前端使用。
	publicPEM string
}

// NewSM2Cipher 创建SM2加密器实例，加载或生成SM2密钥对。
// 客户端持有SM2私钥并仅将公钥暴露给浏览器端；
// 首次启动时自动生成密钥对保证本地开发的自包含性。
// 参数privateKeyFile：私钥文件的存储路径。
// 参数publicKeyFile：公钥文件的存储路径（可导出供前端获取）。
// 返回值：初始化好的SM2Cipher指针和可能的错误。
func NewSM2Cipher(privateKeyFile string, publicKeyFile string) (*SM2Cipher, error) {
	// The client UI owns its SM2 private key and exposes only the public key to
	// the browser; generating it on first boot keeps local development simple.
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

// PublicKeyPEM 返回PEM编码格式的公钥字符串。
// 该公钥通过HTTP接口提供给浏览器前端，用于加密密码等敏感数据。
// 返回值：PEM格式的公钥字符串。
func (c *SM2Cipher) PublicKeyPEM() string {
	return c.publicPEM
}

// PublicKeyHex 返回十六进制编码的公钥字符串。
// 将椭圆曲线上的公钥点（X,Y坐标）序列化为HEX字符串，供前端SM2库使用。
// 返回值：十六进制编码的公钥字符串。
func (c *SM2Cipher) PublicKeyHex() string {
	raw := elliptic.Marshal(c.privateKey.Curve, c.privateKey.X, c.privateKey.Y)
	return hex.EncodeToString(raw)
}

// DecryptToString 使用SM2私钥解密Base64/Hex编码的密文，返回明文字符串。
// 参数ciphertext：Base64或Hex编码的密文字符串（来自浏览器端SM2加密）。
// 返回值：解密后的明文字符串和可能的错误。
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
