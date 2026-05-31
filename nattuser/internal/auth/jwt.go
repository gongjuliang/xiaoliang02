package auth

import (
	"fmt"
	"strconv"
	"time"

	"nattuser/internal/model"

	"github.com/golang-jwt/jwt/v5"
)

const (
	TokenTypeAccess  = "access"
	TokenTypeRefresh = "refresh"
)

type Claims struct {
	UserID    int64  `json:"user_id"`
	Username  string `json:"username"`
	Role      string `json:"role"`
	TokenType string `json:"token_type"`
	jwt.RegisteredClaims
}

type TokenPair struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int64  `json:"expires_in"`
}

type JWTService struct {
	secret     []byte
	accessTTL  time.Duration
	refreshTTL time.Duration
}

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
