package api

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"nattserver/internal/auth"
	"nattserver/internal/config"
	"nattserver/internal/db"

	"github.com/emmansun/gmsm/sm2"
	"github.com/emmansun/gmsm/smx509"
	"github.com/gin-gonic/gin"
)

func TestAuthFlow(t *testing.T) {
	gin.SetMode(gin.TestMode)
	dir := t.TempDir()
	cfg := config.Default()
	cfg.Database.Path = filepath.Join(dir, "test.db")
	cfg.Log.Dir = filepath.Join(dir, "logs")
	cfg.Auth.JWTSecret = "test-secret"
	cfg.Auth.SM2PrivateKeyFile = filepath.Join(dir, "sm2_private.pem")
	cfg.Auth.SM2PublicKeyFile = filepath.Join(dir, "sm2_public.pem")

	database, err := db.Open(context.Background(), cfg.Database.Path, nil)
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	defer database.Close()

	router := NewRouter(cfg, database, nil)
	publicKey := fetchPublicKey(t, router, "/api/server/v1/auth/sm2-public-key")
	encryptedPassword := encryptForPublicKey(t, publicKey, "admin123456")

	tokens := login(t, router, "/api/server/v1/auth/login", encryptedPassword)
	if tokens.AccessToken == "" || tokens.RefreshToken == "" {
		t.Fatalf("expected token pair: %+v", tokens)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/server/v1/auth/me", nil)
	req.Header.Set("Authorization", "Bearer "+tokens.AccessToken)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("auth me status=%d body=%s", rec.Code, rec.Body.String())
	}
	assertAuditLogCount(t, database, 1)
}

func TestAuthSecurityIntegration(t *testing.T) {
	t.Run("development allows plaintext login for local web console", func(t *testing.T) {
		router, database, _ := newAuthTestRouter(t, func(cfg *config.Config) {
			cfg.App.Environment = "development"
		})

		rec := loginWithStatus(t, router, "/api/server/v1/auth/login", "admin", "admin123456")
		if rec.Code != http.StatusOK {
			t.Fatalf("plaintext development login status=%d body=%s", rec.Code, rec.Body.String())
		}
		assertAuditLogCount(t, database, 1)
	})

	t.Run("production rejects plaintext login", func(t *testing.T) {
		router, database, _ := newAuthTestRouter(t, func(cfg *config.Config) {
			cfg.App.Environment = "production"
		})

		rec := loginWithStatus(t, router, "/api/server/v1/auth/login", "admin", "admin123456")
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("plaintext production login status=%d body=%s", rec.Code, rec.Body.String())
		}
		assertAuditLogCount(t, database, 1)
	})

	t.Run("expired access token is rejected and refresh token can renew session", func(t *testing.T) {
		router, database, publicKey := newAuthTestRouter(t, func(cfg *config.Config) {
			cfg.Auth.AccessTokenTTLMinutes = -1
			cfg.Auth.RefreshTokenTTLMinutes = 5
		})

		tokens := login(t, router, "/api/server/v1/auth/login", encryptForPublicKey(t, publicKey, "admin123456"))

		rec := authMe(t, router, "/api/server/v1/auth/me", "Bearer "+tokens.AccessToken)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("expired access token status=%d body=%s", rec.Code, rec.Body.String())
		}

		renewed := refresh(t, router, "/api/server/v1/auth/refresh", tokens.RefreshToken)
		if renewed.AccessToken == "" || renewed.RefreshToken == "" {
			t.Fatalf("expected renewed token pair: %+v", renewed)
		}
		assertAuditLogCount(t, database, 2)
	})

	t.Run("protected endpoints require bearer access token", func(t *testing.T) {
		router, _, publicKey := newAuthTestRouter(t, nil)
		tokens := login(t, router, "/api/server/v1/auth/login", encryptForPublicKey(t, publicKey, "admin123456"))

		for name, header := range map[string]string{
			"missing":       "",
			"refresh token": "Bearer " + tokens.RefreshToken,
			"malformed":     tokens.AccessToken,
		} {
			rec := authMe(t, router, "/api/server/v1/auth/me", header)
			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("%s auth status=%d body=%s", name, rec.Code, rec.Body.String())
			}
		}
	})

	t.Run("login endpoint bans IP after ten failures", func(t *testing.T) {
		router, database, publicKey := newAuthTestRouter(t, nil)
		encryptedWrongPassword := encryptForPublicKey(t, publicKey, "wrong-password")

		for i := 0; i < 10; i++ {
			rec := loginWithStatus(t, router, "/api/server/v1/auth/login", "admin", encryptedWrongPassword)
			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("failed login #%d status=%d body=%s", i+1, rec.Code, rec.Body.String())
			}
		}
		rec := loginWithStatus(t, router, "/api/server/v1/auth/login", "admin", encryptedWrongPassword)
		if rec.Code != http.StatusTooManyRequests {
			t.Fatalf("banned login status=%d body=%s", rec.Code, rec.Body.String())
		}
		assertResponseMessageContains(t, rec, "登录失败次数过多")
		assertResponseMessageContains(t, rec, "5 分钟")
		assertAuditLogCount(t, database, 10)
	})

	t.Run("login requires valid captcha", func(t *testing.T) {
		router, database, _ := newAuthTestRouter(t, func(cfg *config.Config) {
			cfg.App.Environment = "development"
		})

		rec := loginWithStatusWithCaptcha(t, router, "/api/server/v1/auth/login", "admin", "admin123456", "missing", "0000")
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("invalid captcha login status=%d body=%s", rec.Code, rec.Body.String())
		}
		assertResponseMessageContains(t, rec, "验证码不正确或已过期")
		assertAuditLogCount(t, database, 1)
	})
}

func newAuthTestRouter(t *testing.T, configure func(*config.Config)) (http.Handler, *sql.DB, string) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	dir := t.TempDir()
	cfg := config.Default()
	cfg.Database.Path = filepath.Join(dir, "test.db")
	cfg.Log.Dir = filepath.Join(dir, "logs")
	cfg.Auth.JWTSecret = "test-secret"
	cfg.Auth.SM2PrivateKeyFile = filepath.Join(dir, "sm2_private.pem")
	cfg.Auth.SM2PublicKeyFile = filepath.Join(dir, "sm2_public.pem")
	if configure != nil {
		configure(cfg)
	}

	database, err := db.Open(context.Background(), cfg.Database.Path, nil)
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() {
		if err := database.Close(); err != nil {
			t.Fatalf("close database: %v", err)
		}
	})

	router := NewRouter(cfg, database, nil)
	publicKey := fetchPublicKey(t, router, "/api/server/v1/auth/sm2-public-key")
	return router, database, publicKey
}

func fetchPublicKey(t *testing.T, router http.Handler, path string) string {
	t.Helper()
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("public key status=%d body=%s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Code int             `json:"code"`
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode public key response: %v", err)
	}
	var data struct {
		PublicKeyPEM string `json:"public_key_pem"`
	}
	if err := json.Unmarshal(resp.Data, &data); err != nil {
		t.Fatalf("decode public key data: %v", err)
	}
	if data.PublicKeyPEM == "" {
		t.Fatal("expected public key pem")
	}
	return data.PublicKeyPEM
}

func encryptForPublicKey(t *testing.T, publicKeyPEM string, plaintext string) string {
	t.Helper()
	block, _ := pem.Decode([]byte(publicKeyPEM))
	if block == nil {
		t.Fatal("invalid public key pem")
	}
	parsed, err := smx509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		t.Fatalf("parse public key: %v", err)
	}
	publicKey, ok := parsed.(*ecdsa.PublicKey)
	if !ok {
		t.Fatalf("unexpected public key type: %T", parsed)
	}
	ciphertext, err := sm2.Encrypt(rand.Reader, publicKey, []byte(plaintext), nil)
	if err != nil {
		t.Fatalf("encrypt password: %v", err)
	}
	return base64.StdEncoding.EncodeToString(ciphertext)
}

func login(t *testing.T, router http.Handler, path string, encryptedPassword string) auth.TokenPair {
	t.Helper()
	rec := loginWithStatus(t, router, path, "admin", encryptedPassword)
	if rec.Code != http.StatusOK {
		t.Fatalf("login status=%d body=%s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Code int             `json:"code"`
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode login response: %v", err)
	}
	var tokens auth.TokenPair
	if err := json.Unmarshal(resp.Data, &tokens); err != nil {
		t.Fatalf("decode token pair: %v", err)
	}
	return tokens
}

func loginWithStatus(t *testing.T, router http.Handler, path string, username string, encryptedPassword string) *httptest.ResponseRecorder {
	t.Helper()
	captcha := fetchCaptcha(t, router, strings.TrimSuffix(path, "/login")+"/captcha")
	return loginWithStatusWithCaptcha(t, router, path, username, encryptedPassword, captcha.ID, solveCaptcha(t, captcha.Question))
}

func loginWithStatusWithCaptcha(t *testing.T, router http.Handler, path string, username string, encryptedPassword string, captchaID string, captchaCode string) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(map[string]string{
		"username":     username,
		"password":     encryptedPassword,
		"captcha_id":   captchaID,
		"captcha_code": captchaCode,
	})
	if err != nil {
		t.Fatalf("marshal login body: %v", err)
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(rec, req)
	return rec
}

type testCaptcha struct {
	ID       string `json:"captcha_id"`
	Question string `json:"question"`
}

func fetchCaptcha(t *testing.T, router http.Handler, path string) testCaptcha {
	t.Helper()
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("captcha status=%d body=%s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Code int             `json:"code"`
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode captcha response: %v", err)
	}
	var captcha testCaptcha
	if err := json.Unmarshal(resp.Data, &captcha); err != nil {
		t.Fatalf("decode captcha data: %v", err)
	}
	if captcha.ID == "" || captcha.Question == "" {
		t.Fatalf("captcha missing data: %+v", captcha)
	}
	return captcha
}

func solveCaptcha(t *testing.T, question string) string {
	t.Helper()
	var left, right int
	if _, err := fmt.Sscanf(question, "%d + %d = ?", &left, &right); err != nil {
		t.Fatalf("parse captcha question %q: %v", question, err)
	}
	return fmt.Sprintf("%d", left+right)
}

func refresh(t *testing.T, router http.Handler, path string, refreshToken string) auth.TokenPair {
	t.Helper()
	body, err := json.Marshal(map[string]string{
		"refresh_token": refreshToken,
	})
	if err != nil {
		t.Fatalf("marshal refresh body: %v", err)
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("refresh status=%d body=%s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Code int             `json:"code"`
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode refresh response: %v", err)
	}
	var tokens auth.TokenPair
	if err := json.Unmarshal(resp.Data, &tokens); err != nil {
		t.Fatalf("decode token pair: %v", err)
	}
	return tokens
}

func authMe(t *testing.T, router http.Handler, path string, authorization string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if authorization != "" {
		req.Header.Set("Authorization", authorization)
	}
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

func assertAuditLogCount(t *testing.T, database *sql.DB, want int) {
	t.Helper()
	_, total, err := db.ListAuditLogs(context.Background(), database, 100, 0)
	if err != nil {
		t.Fatalf("list audit logs: %v", err)
	}
	if int(total) != want {
		t.Fatalf("audit log count=%d want=%d", total, want)
	}
}
