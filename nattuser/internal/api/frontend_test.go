package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"nattuser/internal/config"
	"nattuser/internal/db"

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

	index := getFrontend(t, router, "/")
	assertContainsAll(t, index, "工具人小良-内网穿透客户端", "客户端控制台", "sessionStorage", "natt_client_active_view")
	assertNotContainsAny(t, index, "NATT Client", "NATT User")

	for _, path := range []string{"/login.html", "/dashboard.html", "/tunnels.html", "/config.html", "/mcp.html", "/audit.html"} {
		body := getFrontend(t, router, path)
		if !strings.Contains(body, "NATT.requireAuth()") && !strings.Contains(body, `id="content"`) && !strings.Contains(body, "loginForm") {
			t.Fatalf("%s does not look like a module page: %s", path, body)
		}
	}
	_ = getFrontend(t, router, "/agreement.html")

	tunnels := getFrontend(t, router, "/tunnels.html")
	assertContainsAll(t, tunnels, "server_port", "data_port", "remote_port", "client_secret", "maskSecret", "show-secret", "renderDetailText", "show-detail", "last_error", "loadDefaults", "defaultConfig", `id="sh" class="layui-input"`, `id="sp" class="layui-input" type="number" value="`, `id="dp" class="layui-input" type="number" value="`)
	if strings.Contains(tunnels, `id="sh" class="layui-input" value="127.0.0.1"`) {
		t.Fatalf("new tunnel connection form must not prefill server host: %s", tunnels)
	}

	configPage := getFrontend(t, router, "/config.html")
	assertContainsAll(t, configPage, "renderReadonlyConfig", "hot_reload", "currentValue", "placeholder")

	mcpPage := getFrontend(t, router, "/mcp.html")
	assertContainsAll(t, mcpPage, "POST /mcp", "jsonrpc", "initialize", "tools/list", "tools/call", "client.list_tunnel_connections", "client.connect_server", "check-row")
	if strings.Contains(mcpPage, "/mcp/tools/call") {
		t.Fatalf("mcp page must not mention old tools/call endpoint: %s", mcpPage)
	}

	loginPage := getFrontend(t, router, "/login.html")
	assertContainsAll(t, loginPage, "工具人小良-内网穿透客户端", "agree_terms", "已阅读并同意", "/agreement.html", "captchaImage", "captchaRefresh", "captcha_code", "/static/js/sm2.js")
	if strings.Contains(loginPage, `value="admin"`) {
		t.Fatalf("login page must not prefill admin username: %s", loginPage)
	}
	assertNotContainsAny(t, loginPage, "NATT Client", "NATT User")

	css := getFrontend(t, router, "/static/css/app.css")
	assertContainsAll(t, css, ".app-shell", ".check-row", ".terms-row", "overflow-wrap: anywhere")

	js := getFrontend(t, router, "/static/js/app.js")
	assertContainsAll(t, js, "request: request", "escapeHtml: escapeHtml", "badge: badge", "logout: logout", "captcha_id", "image_url", "agree_terms", "loadCaptcha", "loadSM2PublicKey", "encryptPasswordForLogin", "public_key_hex")
	if strings.Contains(js, `password: $('[name="password"]').val()`) {
		t.Fatalf("app js must not submit plaintext password directly: %s", js)
	}

	sm2JS := getFrontend(t, router, "/static/js/sm2.js")
	assertContainsAll(t, sm2JS, "NATTSM2", "encryptToBase64", "SM3")
}

func getFrontend(t *testing.T, router http.Handler, path string) string {
	t.Helper()
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("%s status=%d body=%s", path, rec.Code, rec.Body.String())
	}
	return rec.Body.String()
}

func assertContainsAll(t *testing.T, body string, values ...string) {
	t.Helper()
	for _, want := range values {
		if !strings.Contains(body, want) {
			t.Fatalf("body missing %q: %s", want, body)
		}
	}
}

func assertNotContainsAny(t *testing.T, body string, values ...string) {
	t.Helper()
	for _, value := range values {
		if strings.Contains(body, value) {
			t.Fatalf("body must not contain %q: %s", value, body)
		}
	}
}
