package api

import (
	"database/sql"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"nattuser/internal/auth"
	"nattuser/internal/config"
	"nattuser/internal/db"
	"nattuser/internal/logger"
	"nattuser/internal/model"

	"github.com/gin-gonic/gin"
)

const authClaimsKey = "auth_claims"

type AuthHandler struct {
	database               *sql.DB
	log                    *logger.Logger
	jwtService             *auth.JWTService
	sm2Cipher              *auth.SM2Cipher
	rateLimiter            *RateLimiter
	captchaStore           *CaptchaStore
	allowPlaintextPassword bool
}

type loginRequest struct {
	Username    string `json:"username" binding:"required"`
	Password    string `json:"password" binding:"required"`
	CaptchaID   string `json:"captcha_id" binding:"required"`
	CaptchaCode string `json:"captcha_code" binding:"required"`
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
		captchaStore:           NewCaptchaStore(),
		allowPlaintextPassword: cfg.AllowPlaintextPassword,
	}, nil
}

func (h *AuthHandler) RegisterRoutes(group *gin.RouterGroup) {
	group.GET("/auth/sm2-public-key", h.publicKey)
	group.GET("/auth/captcha", h.captcha)
	group.GET("/auth/captcha/:id/image", h.captchaImage)
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
			Fail(c, http.StatusUnauthorized, CodeUnauthorized, "缺少 Authorization 请求头")
			c.Abort()
			return
		}
		token := strings.TrimSpace(strings.TrimPrefix(header, "Bearer "))
		if token == header {
			Fail(c, http.StatusUnauthorized, CodeUnauthorized, "缺少 Bearer 访问令牌")
			c.Abort()
			return
		}
		claims, err := h.jwtService.ParseAccessToken(token)
		if err != nil {
			Fail(c, http.StatusUnauthorized, CodeUnauthorized, "访问令牌无效或已过期")
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

func (h *AuthHandler) captcha(c *gin.Context) {
	challenge, err := h.captchaStore.Create()
	if err != nil {
		Fail(c, http.StatusInternalServerError, CodeInternalError, "生成验证码失败")
		return
	}
	challenge.ImageURL = strings.TrimSuffix(c.Request.URL.Path, "/captcha") + "/captcha/" + url.PathEscape(challenge.ID) + "/image"
	OK(c, challenge)
}

func (h *AuthHandler) captchaImage(c *gin.Context) {
	raw, err := h.captchaStore.Image(c.Param("id"))
	if err != nil {
		Fail(c, http.StatusNotFound, CodeNotFound, "验证码不存在或已过期")
		return
	}
	c.Data(http.StatusOK, "image/png", raw)
}

func (h *AuthHandler) login(c *gin.Context) {
	ip := c.ClientIP()
	if wait, ok := h.rateLimiter.Allow(ip); !ok {
		Fail(c, http.StatusTooManyRequests, CodeTooMany, "登录失败次数过多，请 "+formatBanDuration(wait)+" 后再试")
		return
	}

	var req loginRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		Fail(c, http.StatusBadRequest, CodeBadRequest, "username、password、captcha_id 和 captcha_code 为必填项")
		return
	}
	if !h.captchaStore.Verify(req.CaptchaID, req.CaptchaCode) {
		h.rateLimiter.RecordFailure(ip)
		_ = db.InsertAuditLog(c.Request.Context(), h.database, req.Username, "login_failed", "user", req.Username, "captcha invalid", ip)
		Fail(c, http.StatusBadRequest, CodeBadRequest, "验证码不正确或已过期")
		return
	}

	// The public login contract expects SM2-encrypted passwords. Plaintext is a
	// deliberate development escape hatch controlled by config, not a fallback.
	password, err := h.sm2Cipher.DecryptToString(req.Password)
	if err != nil && h.allowPlaintextPassword {
		password = req.Password
	} else if err != nil {
		h.rateLimiter.RecordFailure(ip)
		_ = db.InsertAuditLog(c.Request.Context(), h.database, req.Username, "login_failed", "user", req.Username, "SM2 password decrypt failed", ip)
		Fail(c, http.StatusBadRequest, CodeBadRequest, "加密密码不正确")
		return
	}

	user, err := db.FindUserByUsername(c.Request.Context(), h.database, req.Username)
	if err != nil {
		h.rateLimiter.RecordFailure(ip)
		_ = db.InsertAuditLog(c.Request.Context(), h.database, req.Username, "login_failed", "user", req.Username, "user not found", ip)
		Fail(c, http.StatusUnauthorized, CodeUnauthorized, "用户名或密码不正确")
		return
	}
	if !auth.CheckPassword(password, user.PasswordHash) {
		h.rateLimiter.RecordFailure(ip)
		_ = db.InsertAuditLog(c.Request.Context(), h.database, user.Username, "login_failed", "user", req.Username, "password mismatch", ip)
		Fail(c, http.StatusUnauthorized, CodeUnauthorized, "用户名或密码不正确")
		return
	}

	tokens, err := h.jwtService.GeneratePair(user)
	if err != nil {
		Fail(c, http.StatusInternalServerError, CodeInternalError, "生成令牌失败")
		return
	}
	h.rateLimiter.RecordSuccess(ip)
	_ = db.InsertAuditLog(c.Request.Context(), h.database, user.Username, "login", "user", req.Username, "login success", ip)
	OK(c, tokens)
}

func (h *AuthHandler) refresh(c *gin.Context) {
	var req refreshRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		Fail(c, http.StatusBadRequest, CodeBadRequest, "refresh_token 为必填项")
		return
	}
	claims, err := h.jwtService.ParseRefreshToken(req.RefreshToken)
	if err != nil {
		Fail(c, http.StatusUnauthorized, CodeUnauthorized, "刷新令牌无效或已过期")
		return
	}
	user, err := db.FindUserByUsername(c.Request.Context(), h.database, claims.Username)
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			Fail(c, http.StatusUnauthorized, CodeUnauthorized, "用户不存在")
			return
		}
		Fail(c, http.StatusInternalServerError, CodeInternalError, "加载用户失败")
		return
	}
	tokens, err := h.jwtService.GeneratePair(user)
	if err != nil {
		Fail(c, http.StatusInternalServerError, CodeInternalError, "生成令牌失败")
		return
	}
	_ = db.InsertAuditLog(c.Request.Context(), h.database, user.Username, "token_refresh", "user", user.Username, "refresh token success", c.ClientIP())
	OK(c, tokens)
}

func (h *AuthHandler) me(c *gin.Context) {
	claims, ok := c.MustGet(authClaimsKey).(*auth.Claims)
	if !ok {
		Fail(c, http.StatusUnauthorized, CodeUnauthorized, "访问令牌信息不正确")
		return
	}
	OK(c, model.User{
		ID:       claims.UserID,
		Username: claims.Username,
		Role:     model.UserRole(claims.Role),
	})
}
