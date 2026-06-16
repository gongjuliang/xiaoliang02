// Package auth 提供SM2国密非对称加密功能。
// SM2是中国国家密码管理局发布的椭圆曲线公钥密码算法，等效于ECC（椭圆曲线加密）。
// 服务端持有私钥用于解密，公钥暴露给浏览器端用于加密敏感数据（如密码），
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
// 服务端持有SM2私钥并仅将公钥暴露给浏览器端；
// 首次启动时自动生成密钥对保证本地开发的自包含性。
// 参数privateKeyFile：私钥文件的存储路径。
// 参数publicKeyFile：公钥文件的存储路径（可导出供前端获取）。
// 返回值：初始化好的SM2Cipher指针和可能的错误。
func NewSM2Cipher(privateKeyFile string, publicKeyFile string) (*SM2Cipher, error) {
	// 服务端持有SM2私钥，仅将公钥暴露给浏览器前端；
	// 首次启动时自动生成密钥对，使本地开发环境自给自足。
	// 加载或创建SM2私钥
	privateKey, err := loadOrCreateSM2PrivateKey(privateKeyFile)
	if err != nil {
		return nil, err
	}
	// 从私钥提取公钥并序列化为PEM格式字符串
	publicPEM, err := marshalPublicKeyPEM(privateKey)
	if err != nil {
		return nil, err
	}
	// 如果指定了公钥文件路径，将公钥PEM写入文件供外部获取
	if publicKeyFile != "" {
		if err := writeFileIfChanged(publicKeyFile, []byte(publicPEM), 0o644); err != nil {
			return nil, err
		}
	}
	// 返回创建的SM2Cipher实例
	return &SM2Cipher{
		privateKey: privateKey, // 保存私钥用于解密
		publicPEM:  publicPEM,  // 保存PEM公钥供导出
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
	// 使用椭圆曲线标准Marshal将公钥点序列化为字节
	raw := elliptic.Marshal(c.privateKey.Curve, c.privateKey.X, c.privateKey.Y)
	// 转为十六进制字符串
	return hex.EncodeToString(raw)
}

// DecryptToString 使用SM2私钥解密Base64/Hex编码的密文，返回明文字符串。
// 参数ciphertext：Base64或Hex编码的密文字符串（来自浏览器端SM2加密）。
// 返回值：解密后的明文字符串和可能的错误。
func (c *SM2Cipher) DecryptToString(ciphertext string) (string, error) {
	// 解码密文（支持Base64标准/无填充和Hex格式）
	raw, err := decodeCiphertext(ciphertext)
	if err != nil {
		return "", err
	}
	// 使用SM2私钥解密密文
	plain, err := sm2.Decrypt(c.privateKey, raw)
	if err != nil {
		return "", fmt.Errorf("sm2 decrypt: %w", err)
	}
	// 将解密结果转为字符串返回
	return string(plain), nil
}

// loadOrCreateSM2PrivateKey 加载已存在的SM2私钥或生成新的密钥对。
// 参数path：私钥文件路径。
// 返回值：加载或生成的SM2私钥和可能的错误。
func loadOrCreateSM2PrivateKey(path string) (*sm2.PrivateKey, error) {
	// 尝试读取并解析已存在的私钥文件
	if content, err := os.ReadFile(path); err == nil {
		// 文件存在且读取成功，解析PEM内容为私钥
		return parsePrivateKeyPEM(content)
	} else if !os.IsNotExist(err) {
		// 读取文件时发生非"不存在"的错误，返回错误
		return nil, fmt.Errorf("read sm2 private key: %w", err)
	}

	// 文件不存在，生成新的SM2密钥对
	privateKey, err := sm2.GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate sm2 private key: %w", err)
	}
	// 将私钥序列化为PEM格式字节
	content, err := marshalPrivateKeyPEM(privateKey)
	if err != nil {
		return nil, err
	}
	// 将PEM私钥写入文件（权限0o600：仅文件所有者可读写）
	if err := writeFileIfChanged(path, content, 0o600); err != nil {
		return nil, err
	}
	return privateKey, nil
}

// parsePrivateKeyPEM 从PEM编码的字节中解析出SM2私钥。
// 参数content：PEM格式的私钥文件字节内容。
// 返回值：解析出的SM2私钥和可能的错误。
func parsePrivateKeyPEM(content []byte) (*sm2.PrivateKey, error) {
	// 解析PEM块
	block, _ := pem.Decode(content)
	if block == nil {
		return nil, fmt.Errorf("invalid sm2 private key pem")
	}
	// 将DER编码的字节解析为SM2私钥
	privateKey, err := smx509.ParseSM2PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse sm2 private key: %w", err)
	}
	return privateKey, nil
}

// marshalPrivateKeyPEM 将SM2私钥序列化为PEM格式字节。
// 参数privateKey：SM2私钥指针。
// 返回值：PEM编码的私钥字节和可能的错误。
func marshalPrivateKeyPEM(privateKey *sm2.PrivateKey) ([]byte, error) {
	// 将私钥序列化为DER字节
	der, err := smx509.MarshalSM2PrivateKey(privateKey)
	if err != nil {
		return nil, fmt.Errorf("marshal sm2 private key: %w", err)
	}
	// 包装为PEM块并编码为内存字节
	return pem.EncodeToMemory(&pem.Block{
		Type:  "SM2 PRIVATE KEY", // PEM块类型标签
		Bytes: der,               // DER编码的私钥字节
	}), nil
}

// marshalPublicKeyPEM 从SM2私钥中提取公钥并序列化为PEM格式字符串。
// 参数privateKey：SM2私钥（公钥可从私钥推导）。
// 返回值：PEM格式的公钥字符串和可能的错误。
func marshalPublicKeyPEM(privateKey *sm2.PrivateKey) (string, error) {
	// 将公钥序列化为PKIX标准的DER字节
	der, err := smx509.MarshalPKIXPublicKey(&privateKey.PublicKey)
	if err != nil {
		return "", fmt.Errorf("marshal sm2 public key: %w", err)
	}
	// 包装为PEM块并编码为字符串
	return string(pem.EncodeToMemory(&pem.Block{
		Type:  "PUBLIC KEY", // PEM块类型标签
		Bytes: der,          // DER编码的公钥字节
	})), nil
}

// decodeCiphertext 尝试以多种编码格式解码密文字符串。
// 依次尝试Base64标准编码、Base64无填充编码、十六进制编码。
// 参数ciphertext：待解码的密文字符串。
// 返回值：解码后的原始字节和可能的错误。
func decodeCiphertext(ciphertext string) ([]byte, error) {
	// 去除首尾空白字符
	value := strings.TrimSpace(ciphertext)
	// 密文不能为空
	if value == "" {
		return nil, fmt.Errorf("ciphertext is required")
	}
	// 尝试Base64标准编码解码
	if raw, err := base64.StdEncoding.DecodeString(value); err == nil {
		return raw, nil
	}
	// 尝试Base64无填充编码解码（RawStdEncoding）
	if raw, err := base64.RawStdEncoding.DecodeString(value); err == nil {
		return raw, nil
	}
	// 尝试十六进制解码
	if raw, err := hex.DecodeString(value); err == nil {
		return raw, nil
	}
	// 所有格式解码均失败
	return nil, fmt.Errorf("ciphertext must be base64 or hex")
}

// writeFileIfChanged 将内容写入文件，但如果文件已存在且内容相同则跳过写入。
// 这样可以减少不必要的磁盘I/O。
// 参数path：目标文件路径。
// 参数content：要写入的内容。
// 参数perm：文件权限（如0o600、0o644）。
// 返回值：可能的错误。
func writeFileIfChanged(path string, content []byte, perm os.FileMode) error {
	// 确保目标文件所在目录存在（权限755）
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create key dir: %w", err)
	}
	// 如果文件已存在且内容相同，跳过写入
	if existing, err := os.ReadFile(path); err == nil && string(existing) == string(content) {
		return nil
	}
	// 写入文件内容
	if err := os.WriteFile(path, content, perm); err != nil {
		return fmt.Errorf("write key file %s: %w", path, err)
	}
	return nil
}
