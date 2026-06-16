// Package auth 提供NATT客户端的认证与安全相关功能，
// 包括客户端密钥生成、密码哈希、JWT令牌管理、SM2国密加密等。
package auth

import (
	// crypto/rand 提供密码学安全随机数，用于生成不可预测的客户端密钥。
	"crypto/rand"
	// encoding/base64 提供Base64编码，将随机字节转为URL安全的字符串。
	"encoding/base64"
	// fmt 提供错误信息的格式化输出。
	"fmt"
)

// GenerateClientSecret 生成一个符合"xiaoliang_"前缀格式的客户端密钥。
// 使用32字节（256位）的密码学安全随机数，经Base64 URL编码后加入前缀。
// 返回值：格式为"xiaoliang_xxxx"的密钥字符串和可能的错误。
func GenerateClientSecret() (string, error) {
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("generate client secret: %w", err)
	}
	return "xiaoliang_" + base64.RawURLEncoding.EncodeToString(raw[:]), nil
}

// SecretHint 根据完整密钥生成一个简短的提示字符串，用于在管理界面中识别密钥。
// 规则：若密钥长度≤10则返回完整密钥；否则返回"前6字符...后6字符"的摘录格式。
// 参数secret：完整的密钥字符串。
// 返回值：用于展示的密钥提示（摘要）字符串。
func SecretHint(secret string) string {
	if len(secret) <= 10 {
		return secret
	}
	return secret[:6] + "..." + secret[len(secret)-6:]
}
