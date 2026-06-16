// Package auth 提供JWT（JSON Web Token）令牌的生成、解析和验证功能。
// 支持Access Token（访问令牌）和Refresh Token（刷新令牌）双令牌机制，
// 使用HMAC-SHA256算法签名，中间件通过TokenType区分令牌类型实现接口级权限控制。
package auth

import (
	// fmt 提供错误信息格式化与包装。
	"fmt"
	// strconv 将用户ID从int64转为字符串以填充JWT的Subject字段。
	"strconv"
	// time 提供令牌过期时间计算所需的当前时间和时间间隔。
	"time"

	// nattserver/internal/model 引入项目内User模型用于填充JWT声明信息。
	"nattserver/internal/model"

	// github.com/golang-jwt/jwt/v5 是JWT标准库，提供令牌创建、签名和解析功能。
	"github.com/golang-jwt/jwt/v5"
)

// 令牌类型常量：用于区分访问令牌和刷新令牌，中间件可据此拒绝刷新令牌访问受保护API。
const (
	// TokenTypeAccess 访问令牌类型，用于实际API请求的身份认证。
	TokenTypeAccess = "access"
	// TokenTypeRefresh 刷新令牌类型，仅用于获取新的Access Token。
	TokenTypeRefresh = "refresh"
)

// Claims JWT载荷声明结构体，包含用户身份信息和JWT标准注册声明。
// 嵌入jwt.RegisteredClaims实现JWT标准字段（有效期等）。
type Claims struct {
	// UserID 用户唯一标识。
	UserID int64 `json:"user_id"`
	// Username 用户登录名。
	Username string `json:"username"`
	// Role 用户角色（如admin管理员）。
	Role string `json:"role"`
	// TokenType 令牌类型（access或refresh），用于中间件校验。
	TokenType string `json:"token_type"`
	// jwt.RegisteredClaims 嵌入JWT标准声明（签发时间、过期时间、主题等）。
	jwt.RegisteredClaims
}

// TokenPair 令牌对结构体，包含Access Token和Refresh Token，作为登录响应返回给客户端。
type TokenPair struct {
	// AccessToken 访问令牌字符串，用于后续API请求的Authorization头。
	AccessToken string `json:"access_token"`
	// RefreshToken 刷新令牌字符串，用于在Access Token过期后获取新令牌。
	RefreshToken string `json:"refresh_token"`
	// TokenType 令牌类型标识，固定为"Bearer"。
	TokenType string `json:"token_type"`
	// ExpiresIn Access Token的有效期（秒）。
	ExpiresIn int64 `json:"expires_in"`
}

// JWTService JWT服务结构体，封装了密钥、令牌有效期和核心操作。
type JWTService struct {
	// secret 签名密钥（字节形式），用于HMAC-SHA256签名和验证。
	secret []byte
	// accessTTL Access Token的有效期（时间段）。
	accessTTL time.Duration
	// refreshTTL Refresh Token的有效期（时间段），通常比accessTTL更长。
	refreshTTL time.Duration
}

// NewJWTService 创建JWTService实例，配置签名密钥和令牌有效期。
// 参数secret：HMAC签名密钥字符串。
// 参数accessTTL：Access Token的有效期。
// 参数refreshTTL：Refresh Token的有效期。
// 返回值：初始化好的JWTService指针。
func NewJWTService(secret string, accessTTL time.Duration, refreshTTL time.Duration) *JWTService {
	return &JWTService{
		secret:     []byte(secret), // 将密钥字符串转为字节切片存储
		accessTTL:  accessTTL,      // 保存Access Token有效期
		refreshTTL: refreshTTL,     // 保存Refresh Token有效期
	}
}

// GeneratePair 为一用户同时生成Access Token和Refresh Token令牌对。
// Access和Refresh令牌携带相同的用户身份信息，但TokenType字段值不同，
// 这样中间件可以在调用受保护API时拒绝对Refresh Token的访问。
// 参数user：需要生成令牌的用户模型。
// 返回值：包含两个令牌的TokenPair和可能的错误。
func (s *JWTService) GeneratePair(user model.User) (TokenPair, error) {
	// Access Token和Refresh Token携带相同的身份信息但TokenType不同，
	// 中间件可以通过TokenType拒绝将Refresh Token用于API访问。
	// 生成Access Token（TokenType="access"）
	accessToken, err := s.generate(user, TokenTypeAccess, s.accessTTL)
	if err != nil {
		return TokenPair{}, err
	}
	// 生成Refresh Token（TokenType="refresh"），有效期更长
	refreshToken, err := s.generate(user, TokenTypeRefresh, s.refreshTTL)
	if err != nil {
		return TokenPair{}, err
	}
	// 构建并返回令牌对，TokenType固定为"Bearer"符合HTTP认证规范
	return TokenPair{
		AccessToken:  accessToken,                  // 访问令牌
		RefreshToken: refreshToken,                 // 刷新令牌
		TokenType:    "Bearer",                     // HTTP Bearer认证类型
		ExpiresIn:    int64(s.accessTTL.Seconds()), // 过期时间（秒）
	}, nil
}

// ParseAccessToken 解析并验证Access Token，返回解析出的Claims。
// 会额外校验TokenType必须为"access"，拒绝Refresh Token。
// 参数tokenString：JWT令牌字符串。
// 返回值：解析出的Claims指针和可能的错误。
func (s *JWTService) ParseAccessToken(tokenString string) (*Claims, error) {
	// 调用通用解析方法获取Claims
	claims, err := s.parse(tokenString)
	if err != nil {
		return nil, err
	}
	// 验证TokenType必须为access，拒绝refresh令牌访问API
	if claims.TokenType != TokenTypeAccess {
		return nil, fmt.Errorf("token is not an access token")
	}
	return claims, nil
}

// ParseRefreshToken 解析并验证Refresh Token，返回解析出的Claims。
// 会额外校验TokenType必须为"refresh"。
// 参数tokenString：JWT令牌字符串。
// 返回值：解析出的Claims指针和可能的错误。
func (s *JWTService) ParseRefreshToken(tokenString string) (*Claims, error) {
	// 调用通用解析方法获取Claims
	claims, err := s.parse(tokenString)
	if err != nil {
		return nil, err
	}
	// 验证TokenType必须为refresh
	if claims.TokenType != TokenTypeRefresh {
		return nil, fmt.Errorf("token is not a refresh token")
	}
	return claims, nil
}

// generate 内部方法：根据用户信息和令牌参数生成一个JWT令牌字符串。
// 参数user：用户模型，提供ID、用户名和角色信息。
// 参数tokenType：令牌类型（"access"或"refresh"）。
// 参数ttl：令牌有效期时长。
// 返回值：签名的JWT字符串和可能的错误。
func (s *JWTService) generate(user model.User, tokenType string, ttl time.Duration) (string, error) {
	// 获取当前时间作为签发时间基准
	now := time.Now()
	// 构建Claims声明，填充用户身份和JWT标准字段
	claims := Claims{
		UserID:    user.ID,           // 用户ID
		Username:  user.Username,     // 用户名
		Role:      string(user.Role), // 角色（转为字符串）
		TokenType: tokenType,         // 令牌类型标识
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   strconv.FormatInt(user.ID, 10),   // 主题：用户ID的十进制字符串
			IssuedAt:  jwt.NewNumericDate(now),          // 签发时间（iat）
			ExpiresAt: jwt.NewNumericDate(now.Add(ttl)), // 过期时间（exp）= 当前时间 + 有效期
		},
	}
	// 使用HMAC-SHA256算法签名并生成JWT字符串
	return jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(s.secret)
}

// parse 内部方法：解析并验证JWT令牌，返回Claims（不区分TokenType）。
// 参数tokenString：JWT令牌字符串。
// 返回值：解析出的Claims指针和可能的错误（签名无效、过期等）。
func (s *JWTService) parse(tokenString string) (*Claims, error) {
	// 创建空的Claims用于接收解析结果
	claims := &Claims{}
	// 解析JWT令牌，同时传入密钥回调函数以验证签名
	token, err := jwt.ParseWithClaims(tokenString, claims, func(token *jwt.Token) (any, error) {
		// 验证签名算法必须是HMAC类型，防止算法混淆攻击
		if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %s", token.Method.Alg())
		}
		// 返回签名密钥用于验证
		return s.secret, nil
	})
	// 解析过程中出错（签名无效、格式错误等）
	if err != nil {
		return nil, err
	}
	// 检查令牌整体是否有效（包括过期时间校验）
	if !token.Valid {
		return nil, fmt.Errorf("invalid token")
	}
	// 返回解析出的Claims
	return claims, nil
}
