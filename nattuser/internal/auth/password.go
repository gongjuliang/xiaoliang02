// Package auth 提供密码哈希和验证功能。
// 使用SM3国密算法进行密码哈希，采用随机盐值+SM3摘要的加盐哈希方案，
// 并使用恒定时间比较（ConstantTimeCompare）防御时序攻击。
package auth

import (
	// crypto/rand 提供密码学安全随机数，用于生成随机盐值。
	"crypto/rand"
	// crypto/subtle 提供恒定时间比较函数，防止基于时间的侧信道攻击。
	"crypto/subtle"
	// encoding/base64 提供Base64编码，用于SM3摘要结果的文本化存储。
	"encoding/base64"
	// fmt 提供错误信息的格式化输出。
	"fmt"
	// strings 提供字符串分割和字符匹配功能。
	"strings"

	// github.com/emmansun/gmsm/sm3 提供SM3国密哈希算法实现（等效于SHA-256安全级别）。
	"github.com/emmansun/gmsm/sm3"
)

// saltAlphabet 盐值字符集：大小写字母和数字（62个字符）。
// 从中随机选取字符组成8位盐值。
const saltAlphabet = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"

// HashPassword 对明文密码进行SM3加盐哈希，返回存储格式的哈希字符串。
// 存储格式："8位盐值$SM3摘要(Base64编码)"
// 参数password：用户输入的明文密码。
// 返回值：格式为"salt$hash"的哈希字符串和可能的错误。
func HashPassword(password string) (string, error) {
	salt, err := randomSalt(8)
	if err != nil {
		return "", err
	}
	return salt + "$" + sm3Digest(salt, password), nil
}

// CheckPassword 验证明文密码是否与存储的哈希值匹配。
// 使用恒定时间比较防止时序攻击。
// 参数password：用户输入的明文密码。
// 参数hash：数据库中存储的哈希字符串（格式："salt$hash"）。
// 返回值：匹配返回true，否则返回false。
func CheckPassword(password string, hash string) bool {
	parts := strings.Split(hash, "$")
	if len(parts) != 2 || len(parts[0]) != 8 || parts[1] == "" {
		return false
	}
	expected := sm3Digest(parts[0], password)
	return subtle.ConstantTimeCompare([]byte(expected), []byte(parts[1])) == 1
}

// IsCurrentPasswordHash 判断给定的哈希字符串是否符合当前密码哈希格式。
// 用于检测旧格式密码，以便在登录时触发升级重新哈希。
// 参数hash：待检测的哈希字符串。
// 返回值：符合当前格式返回true。
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

// sm3Digest 计算SM3摘要：先对"盐值+密码"进行Base64编码，再对编码结果计算SM3哈希。
// 这种两级处理方式增加了哈希的复杂度。
// 参数salt：随机盐值。
// 参数input：明文数据（密码）。
// 返回值：Base64编码的SM3哈希值。
func sm3Digest(salt string, input string) string {
	encoded := base64.StdEncoding.EncodeToString([]byte(salt + input))
	sum := sm3.Sum([]byte(encoded))
	return base64.StdEncoding.EncodeToString(sum[:])
}

// randomSalt 从saltAlphabet字符集中生成指定长度的随机盐值。
// 使用crypto/rand生成密码学安全的随机字节，映射到字符集的索引。
// 参数length：盐值长度（必须为正数）。
// 返回值：生成的盐值字符串和可能的错误。
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
