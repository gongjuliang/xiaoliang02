// Package auth 提供NATT服务端的认证与安全相关功能，
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
	// 创建32字节的缓冲区用于存储随机数
	var raw [32]byte
	// 使用crypto/rand填充密码学安全随机字节
	if _, err := rand.Read(raw[:]); err != nil {
		// 随机数生成失败时返回错误
		return "", fmt.Errorf("generate client secret: %w", err)
	}
	// 将随机字节用Base64 RawURLEncoding编码（无填充，URL安全），
	// 并添加"xiaoliang_"前缀作为密钥标识
	return "xiaoliang_" + base64.RawURLEncoding.EncodeToString(raw[:]), nil
}

// SecretHint 根据完整密钥生成一个简短的提示字符串，用于在管理界面中识别密钥。
// 规则：若密钥长度≤10则返回完整密钥；否则返回"前6字符...后6字符"的摘录格式。
// 参数secret：完整的密钥字符串。
// 返回值：用于展示的密钥提示（摘要）字符串。
func SecretHint(secret string) string {
	// 密钥较短时直接返回完整内容
	if len(secret) <= 10 {
		return secret
	}
	// 取前6个字符和后6个字符，中间用"..."连接作为提示摘要
	return secret[:6] + "..." + secret[len(secret)-6:]
}
