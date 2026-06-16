// Package api 提供认证相关的Web API处理器。
// 包含SM2公钥获取、图片验证码、用户登录（SM2加密密码验证+JWT签发）、
// Token刷新和当前用户信息查询等完整的认证流程端点。
package api

import (
	"database/sql"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"nattserver/internal/auth"
	"nattserver/internal/config"
	"nattserver/internal/db"
	"nattserver/internal/logger"
	"nattserver/internal/model"

	"github.com/gin-gonic/gin"
)

// authClaimsKey 在Gin上下文中存储JWT Claims的键名。
const authClaimsKey = "auth_claims"

// AuthHandler 认证API处理器，提供SM2公钥获取、验证码、登录、Token刷新等完整认证流程。
// 集成JWT服务、SM2加解密、速率限制和验证码校验。
type AuthHandler struct {
	database               *sql.DB          // 数据库连接
	log                    *logger.Logger   // 日志记录器
	jwtService             *auth.JWTService // JWT令牌服务
	sm2Cipher              *auth.SM2Cipher  // SM2国密加解密器
	rateLimiter            *RateLimiter     // 登录速率限制器
	captchaStore           *CaptchaStore    // 验证码存储
	allowPlaintextPassword bool             // 是否允许明文密码（仅开发环境）
}

// loginRequest 登录请求参数，包含SM2加密密码和验证码。
type loginRequest struct {
	Username    string `json:"username" binding:"required"`     // 用户名
	Password    string `json:"password" binding:"required"`     // SM2加密的密码
	CaptchaID   string `json:"captcha_id" binding:"required"`   // 验证码ID
	CaptchaCode string `json:"captcha_code" binding:"required"` // 验证码答案
	AgreeTerms  bool   `json:"agree_terms"`                     // 是否同意用户协议
}

// refreshRequest Token刷新请求参数。
type refreshRequest struct {
	RefreshToken string `json:"refresh_token" binding:"required"` // 刷新令牌
}

// NewAuthHandler 创建认证API处理器，初始化SM2加密器、JWT服务和限流器。
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

// RegisterRoutes 注册认证相关的REST端点：SM2公钥/验证码/登录/刷新/当前用户。
// 其中/me端点需要JWT鉴权中间件保护。
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

// JWTMiddleware 创建JWT鉴权中间件，从Authorization头提取Bearer Token并验证。
// 验证通过后，将解析出的Claims存入Gin上下文供后续处理器使用。
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

// publicKey 返回SM2公钥的PEM和Hex两种格式，供前端加密密码使用。
func (h *AuthHandler) publicKey(c *gin.Context) {
	OK(c, gin.H{
		"algorithm":      "SM2",
		"cipher_format":  "base64",
		"public_key_pem": h.sm2Cipher.PublicKeyPEM(),
		"public_key_hex": h.sm2Cipher.PublicKeyHex(),
	})
}

// captcha 生成算术验证码，返回captcha_id和图片URL（算式不暴露给前端DOM）。
func (h *AuthHandler) captcha(c *gin.Context) {
	challenge, err := h.captchaStore.Create()
	if err != nil {
		Fail(c, http.StatusInternalServerError, CodeInternalError, "生成验证码失败")
		return
	}
	challenge.ImageURL = strings.TrimSuffix(c.Request.URL.Path, "/captcha") + "/captcha/" + url.PathEscape(challenge.ID) + "/image"
	OK(c, challenge)
}

// captchaImage 根据captcha_id渲染并返回PNG格式的验证码图片。
func (h *AuthHandler) captchaImage(c *gin.Context) {
	raw, err := h.captchaStore.Image(c.Param("id"))
	if err != nil {
		Fail(c, http.StatusNotFound, CodeNotFound, "验证码不存在或已过期")
		return
	}
	c.Data(http.StatusOK, "image/png", raw)
}

// login 处理用户登录：速率限制→验证码校验→SM2密码解密→数据库验证→签发JWT令牌对。
// 失败累计触发递进式IP封禁(5分/10分/30分/1时/6时)，成功重置计数并写入审计日志。
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
	if !req.AgreeTerms {
		Fail(c, http.StatusBadRequest, CodeBadRequest, "请先阅读并同意用户协议")
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

// refresh 使用Refresh Token验证用户身份并签发新的Access/Refresh令牌对。
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

// me 返回当前JWT认证用户的ID、用户名和角色信息（需JWT鉴权）。
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
