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

	// nattuser/internal/model 引入项目内User模型用于填充JWT声明信息。
	"nattuser/internal/model"

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
		secret:     []byte(secret),
		accessTTL:  accessTTL,
		refreshTTL: refreshTTL,
	}
}

func (s *JWTService) GeneratePair(user model.User) (TokenPair, error) {
	// Access and refresh tokens carry the same identity but different token_type
	// values, so middleware can reject a refresh token on protected APIs.
	accessToken, err := s.generate(user, TokenTypeAccess, s.accessTTL)
	if err != nil {
		return TokenPair{}, err
	}
	refreshToken, err := s.generate(user, TokenTypeRefresh, s.refreshTTL)
	if err != nil {
		return TokenPair{}, err
	}
	return TokenPair{
		AccessToken:  accessToken,
		RefreshToken: refreshToken,
		TokenType:    "Bearer",
		ExpiresIn:    int64(s.accessTTL.Seconds()),
	}, nil
}

func (s *JWTService) ParseAccessToken(tokenString string) (*Claims, error) {
	claims, err := s.parse(tokenString)
	if err != nil {
		return nil, err
	}
	if claims.TokenType != TokenTypeAccess {
		return nil, fmt.Errorf("token is not an access token")
	}
	return claims, nil
}

func (s *JWTService) ParseRefreshToken(tokenString string) (*Claims, error) {
	claims, err := s.parse(tokenString)
	if err != nil {
		return nil, err
	}
	if claims.TokenType != TokenTypeRefresh {
		return nil, fmt.Errorf("token is not a refresh token")
	}
	return claims, nil
}

func (s *JWTService) generate(user model.User, tokenType string, ttl time.Duration) (string, error) {
	now := time.Now()
	claims := Claims{
		UserID:    user.ID,
		Username:  user.Username,
		Role:      string(user.Role),
		TokenType: tokenType,
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   strconv.FormatInt(user.ID, 10),
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(ttl)),
		},
	}
	return jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(s.secret)
}

func (s *JWTService) parse(tokenString string) (*Claims, error) {
	claims := &Claims{}
	token, err := jwt.ParseWithClaims(tokenString, claims, func(token *jwt.Token) (any, error) {
		if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %s", token.Method.Alg())
		}
		return s.secret, nil
	})
	if err != nil {
		return nil, err
	}
	if !token.Valid {
		return nil, fmt.Errorf("invalid token")
	}
	return claims, nil
}
