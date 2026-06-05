package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"nattserver/internal/config"
	"nattserver/internal/db"

	"github.com/gin-gonic/gin"
)

func TestRouterServesEmbeddedFrontend(t *testing.T) {
	gin.SetMode(gin.TestMode)
	dir := t.TempDir()
	cfg := config.Default()
	cfg.Database.Path = filepath.Join(dir, "test.db")
	cfg.Log.Dir = filepath.Join(dir, "logs")
	cfg.Auth.SM2PrivateKeyFile = filepath.Join(dir, "sm2_private.pem")
	cfg.Auth.SM2PublicKeyFile = filepath.Join(dir, "sm2_public.pem")
	database, err := db.Open(context.Background(), cfg.Database.Path, nil)
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	defer database.Close()

	router := NewRouter(cfg, database, nil)

	index := httptest.NewRecorder()
	router.ServeHTTP(index, httptest.NewRequest(http.MethodGet, "/", nil))
	if index.Code != http.StatusOK {
		t.Fatalf("index status=%d body=%s", index.Code, index.Body.String())
	}
	if !strings.Contains(index.Body.String(), "NATT Server") {
		t.Fatalf("index does not contain server title: %s", index.Body.String())
	}
	if !strings.Contains(index.Body.String(), "sessionStorage") || !strings.Contains(index.Body.String(), "natt_server_active_view") {
		t.Fatalf("index does not persist active view in sessionStorage: %s", index.Body.String())
	}
	for _, path := range []string{"/login.html", "/dashboard.html", "/tunnels.html", "/config.html", "/mcp.html", "/audit.html"} {
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("%s status=%d body=%s", path, rec.Code, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), "NATT") && !strings.Contains(rec.Body.String(), "NATT.requireAuth()") && !strings.Contains(rec.Body.String(), `id="content"`) {
			t.Fatalf("%s does not look like a module page: %s", path, rec.Body.String())
		}
	}

	tunnels := httptest.NewRecorder()
	router.ServeHTTP(tunnels, httptest.NewRequest(http.MethodGet, "/tunnels.html", nil))
	if !strings.Contains(tunnels.Body.String(), "状态详情") {
		t.Fatalf("tunnels page missing status detail column: %s", tunnels.Body.String())
	}
	for _, want := range []string{"t.secret", "secret_hint", "历史摘要"} {
		if !strings.Contains(tunnels.Body.String(), want) {
			t.Fatalf("tunnels page missing secret display marker %q", want)
		}
	}
	for _, want := range []string{"maskSecret", "show-secret", "renderDetailText", "show-detail", "上行字节", "下行字节", "formatBytes"} {
		if !strings.Contains(tunnels.Body.String(), want) {
			t.Fatalf("tunnels page missing tunnel UX marker %q", want)
		}
	}

	configPage := httptest.NewRecorder()
	router.ServeHTTP(configPage, httptest.NewRequest(http.MethodGet, "/config.html", nil))
	for _, want := range []string{"可热更新配置", "当前配置", "renderReadonlyConfig", "hot_reload", "当前值:", "currentValue"} {
		if !strings.Contains(configPage.Body.String(), want) {
			t.Fatalf("config page missing hot reload marker %q", want)
		}
	}

	mcpPage := httptest.NewRecorder()
	router.ServeHTTP(mcpPage, httptest.NewRequest(http.MethodGet, "/mcp.html", nil))
	for _, want := range []string{"如何使用 MCP", "/mcp/tools/call", "server.list_tunnels", "server.start_tunnel", "保存成功", "check-row"} {
		if !strings.Contains(mcpPage.Body.String(), want) {
			t.Fatalf("mcp page missing usage marker %q", want)
		}
	}

	loginPage := httptest.NewRecorder()
	router.ServeHTTP(loginPage, httptest.NewRequest(http.MethodGet, "/login.html", nil))
	for _, want := range []string{"验证码", "captchaImage", "captchaRefresh", "captcha_code"} {
		if !strings.Contains(loginPage.Body.String(), want) {
			t.Fatalf("login page missing captcha marker %q", want)
		}
	}

	css := httptest.NewRecorder()
	router.ServeHTTP(css, httptest.NewRequest(http.MethodGet, "/static/css/app.css", nil))
	if css.Code != http.StatusOK {
		t.Fatalf("css status=%d body=%s", css.Code, css.Body.String())
	}
	if !strings.Contains(css.Body.String(), ".app-shell") {
		t.Fatalf("css body missing app styles: %s", css.Body.String())
	}
	if !strings.Contains(css.Body.String(), ".check-row") {
		t.Fatalf("css body missing checkbox alignment styles: %s", css.Body.String())
	}

	js := httptest.NewRecorder()
	router.ServeHTTP(js, httptest.NewRequest(http.MethodGet, "/static/js/app.js", nil))
	if js.Code != http.StatusOK {
		t.Fatalf("js status=%d body=%s", js.Code, js.Body.String())
	}
	for _, want := range []string{"request: request", "escapeHtml: escapeHtml", "badge: badge", "logout: logout", "captcha_id", "image_url", "loadCaptcha"} {
		if !strings.Contains(js.Body.String(), want) {
			t.Fatalf("app js missing %q", want)
		}
	}
}
