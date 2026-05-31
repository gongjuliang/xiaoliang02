package api

import (
	"database/sql"
	"errors"
	"net/http"
	"strings"
	"time"

	"nattserver/internal/auth"
	"nattserver/internal/config"
	"nattserver/internal/db"
	"nattserver/internal/logger"
	"nattserver/internal/model"

	"github.com/gin-gonic/gin"
)

const authClaimsKey = "auth_claims"

type AuthHandler struct {
	database               *sql.DB
	log                    *logger.Logger
	jwtService             *auth.JWTService
	sm2Cipher              *auth.SM2Cipher
	rateLimiter            *RateLimiter
	allowPlaintextPassword bool
}

type loginRequest struct {
	Username string `json:"username" binding:"required"`
	Password string `json:"password" binding:"required"`
}

type refreshRequest struct {
	RefreshToken string `json:"refresh_token" binding:"required"`
}

func NewAuthHandler(cfg config.AuthConfig, database *sql.DB, log *logger.Logger) (*AuthHandler, error) {
	sm2Cipher, err := auth.NewSM2Cipher(cfg.SM2PrivateKeyFile, cfg.SM2PublicKeyFile)
	if err != nil {
		return nil, err
	}
	jwtService := auth.NewJWTService(
		cfg.JWTSecret,
		time.Duration(cfg.AccessTokenTTLMinutes)*time.Minute,
		time.Duration(cfg.RefreshTokenTTLMinutes)*time.Minute,
	)
	return &AuthHandler{
		database:               database,
		log:                    log,
		jwtService:             jwtService,
		sm2Cipher:              sm2Cipher,
		rateLimiter:            NewRateLimiter(cfg.LoginRateLimitPerMinute, time.Minute),
		allowPlaintextPassword: cfg.AllowPlaintextPassword,
	}, nil
}

func (h *AuthHandler) RegisterRoutes(group *gin.RouterGroup) {
	group.GET("/auth/sm2-public-key", h.publicKey)
	group.POST("/auth/login", h.login)
	group.POST("/auth/refresh", h.refresh)

	protected := group.Group("")
	protected.Use(h.JWTMiddleware())
	protected.GET("/auth/me", h.me)
}

func (h *AuthHandler) JWTMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		header := c.GetHeader("Authorization")
		if header == "" {
			Fail(c, http.StatusUnauthorized, CodeUnauthorized, "authorization header is required")
			c.Abort()
			return
		}
		token := strings.TrimSpace(strings.TrimPrefix(header, "Bearer "))
		if token == header {
			Fail(c, http.StatusUnauthorized, CodeUnauthorized, "authorization bearer token is required")
			c.Abort()
			return
		}
		claims, err := h.jwtService.ParseAccessToken(token)
		if err != nil {
			Fail(c, http.StatusUnauthorized, CodeUnauthorized, "invalid or expired token")
			c.Abort()
			return
		}
		// Store parsed claims in the Gin context so protected handlers use the
		// verified identity rather than trusting request parameters.
		c.Set(authClaimsKey, claims)
		c.Next()
	}
}

func (h *AuthHandler) publicKey(c *gin.Context) {
	OK(c, gin.H{
		"algorithm":      "SM2",
		"cipher_format":  "base64",
		"public_key_pem": h.sm2Cipher.PublicKeyPEM(),
	})
}

func (h *AuthHandler) login(c *gin.Context) {
	ip := c.ClientIP()
	if !h.rateLimiter.Allow(ip) {
		Fail(c, http.StatusTooManyRequests, CodeTooMany, "too many login attempts")
		return
	}

	var req loginRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		Fail(c, http.StatusBadRequest, CodeBadRequest, "username and password are required")
		return
	}

	// The public login contract expects SM2-encrypted passwords. Plaintext is a
	// deliberate development escape hatch controlled by config, not a fallback.
	password, err := h.sm2Cipher.DecryptToString(req.Password)
	if err != nil && h.allowPlaintextPassword {
		password = req.Password
	} else if err != nil {
		_ = db.InsertAuditLog(c.Request.Context(), h.database, req.Username, "login_failed", "user", req.Username, "SM2 password decrypt failed", ip)
		Fail(c, http.StatusBadRequest, CodeBadRequest, "invalid encrypted password")
		return
	}

	user, err := db.FindUserByUsername(c.Request.Context(), h.database, req.Username)
	if err != nil {
		_ = db.InsertAuditLog(c.Request.Context(), h.database, req.Username, "login_failed", "user", req.Username, "user not found", ip)
		Fail(c, http.StatusUnauthorized, CodeUnauthorized, "invalid username or password")
		return
	}
	if !auth.CheckPassword(password, user.PasswordHash) {
		_ = db.InsertAuditLog(c.Request.Context(), h.database, user.Username, "login_failed", "user", req.Username, "password mismatch", ip)
		Fail(c, http.StatusUnauthorized, CodeUnauthorized, "invalid username or password")
		return
	}

	tokens, err := h.jwtService.GeneratePair(user)
	if err != nil {
		Fail(c, http.StatusInternalServerError, CodeInternalError, "generate token failed")
		return
	}
	_ = db.InsertAuditLog(c.Request.Context(), h.database, user.Username, "login", "user", req.Username, "login success", ip)
	OK(c, tokens)
}

func (h *AuthHandler) refresh(c *gin.Context) {
	var req refreshRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		Fail(c, http.StatusBadRequest, CodeBadRequest, "refresh_token is required")
		return
	}
	claims, err := h.jwtService.ParseRefreshToken(req.RefreshToken)
	if err != nil {
		Fail(c, http.StatusUnauthorized, CodeUnauthorized, "invalid or expired refresh token")
		return
	}
	user, err := db.FindUserByUsername(c.Request.Context(), h.database, claims.Username)
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			Fail(c, http.StatusUnauthorized, CodeUnauthorized, "user no longer exists")
			return
		}
		Fail(c, http.StatusInternalServerError, CodeInternalError, "load user failed")
		return
	}
	tokens, err := h.jwtService.GeneratePair(user)
	if err != nil {
		Fail(c, http.StatusInternalServerError, CodeInternalError, "generate token failed")
		return
	}
	_ = db.InsertAuditLog(c.Request.Context(), h.database, user.Username, "token_refresh", "user", user.Username, "refresh token success", c.ClientIP())
	OK(c, tokens)
}

func (h *AuthHandler) me(c *gin.Context) {
	claims, ok := c.MustGet(authClaimsKey).(*auth.Claims)
	if !ok {
		Fail(c, http.StatusUnauthorized, CodeUnauthorized, "invalid token claims")
		return
	}
	OK(c, model.User{
		ID:       claims.UserID,
		Username: claims.Username,
		Role:     model.UserRole(claims.Role),
	})
}
