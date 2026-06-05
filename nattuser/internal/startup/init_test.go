package startup

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"nattuser/internal/auth"
	"nattuser/internal/config"
	"nattuser/internal/db"
)

func TestInitHandlerCreatesConfigFilesAndDatabase(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	cfg := clientInitTestConfig()

	done := make(chan *config.Config, 1)
	handler := NewInitHandler(cfg, done)

	page := httptest.NewRecorder()
	handler.ServeHTTP(page, httptest.NewRequest(http.MethodGet, "/init.html", nil))
	if page.Code != http.StatusOK {
		t.Fatalf("init page status=%d body=%s", page.Code, page.Body.String())
	}
	assertContainsAll(t, page.Body.String(), "初始化 工具人小良-内网穿透客户端", "web_https_enabled", "已阅读并同意《用户协议》")

	status := httptest.NewRecorder()
	handler.ServeHTTP(status, httptest.NewRequest(http.MethodGet, "/api/init/status", nil))
	if status.Code != http.StatusOK || !strings.Contains(status.Body.String(), `"config_exists":false`) {
		t.Fatalf("status=%d body=%s", status.Code, status.Body.String())
	}

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/init/config", strings.NewReader(validClientInitBody(t, cfg, map[string]any{
		"environment": "production",
		"http_port":   25520,
	}))))
	if rec.Code != http.StatusOK {
		t.Fatalf("init post status=%d body=%s", rec.Code, rec.Body.String())
	}
	select {
	case initialized := <-done:
		if initialized.HTTP.Port != 25520 {
			t.Fatalf("initialized http.port=%d want 25520", initialized.HTTP.Port)
		}
	default:
		t.Fatal("initializer did not signal completion")
	}

	content, err := os.ReadFile(config.DefaultPath)
	if err != nil {
		t.Fatalf("read generated config: %v", err)
	}
	var generated config.Config
	if err := json.Unmarshal(content, &generated); err != nil {
		t.Fatalf("decode generated config: %v", err)
	}
	if generated.HTTP.Port != 25520 || generated.Database.Path != cfg.Database.Path {
		t.Fatalf("unexpected generated config: %+v", generated)
	}
	if generated.App.Environment != "production" {
		t.Fatalf("generated environment=%q want production", generated.App.Environment)
	}
	if generated.HTTP.HTTPSEnabled {
		t.Fatal("HTTPS should be disabled when init request does not enable it")
	}
	for _, path := range []string{cfg.Database.Path, cfg.Auth.SM2PrivateKeyFile, cfg.Auth.SM2PublicKeyFile} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("expected initialized file %s: %v", path, err)
		}
	}
	database, err := db.Open(context.Background(), cfg.Database.Path, nil)
	if err != nil {
		t.Fatalf("open initialized database: %v", err)
	}
	defer database.Close()
	user, err := db.FindUserByUsername(context.Background(), database, "owner")
	if err != nil {
		t.Fatalf("find initialized admin: %v", err)
	}
	if !auth.CheckPassword("Owner1234", user.PasswordHash) {
		t.Fatal("initialized admin password hash is invalid")
	}
}

func TestInitHandlerGeneratesHTTPSCertificate(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	cfg := clientInitTestConfig()

	handler := NewInitHandler(cfg, make(chan *config.Config, 1))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/init/config", strings.NewReader(validClientInitBody(t, cfg, map[string]any{
		"web_https_enabled": true,
		"web_https_mode":    "auto",
	}))))
	if rec.Code != http.StatusOK {
		t.Fatalf("init post status=%d body=%s", rec.Code, rec.Body.String())
	}

	content, err := os.ReadFile(config.DefaultPath)
	if err != nil {
		t.Fatalf("read generated config: %v", err)
	}
	var generated config.Config
	if err := json.Unmarshal(content, &generated); err != nil {
		t.Fatalf("decode generated config: %v", err)
	}
	if !generated.HTTP.HTTPSEnabled {
		t.Fatal("generated config should enable HTTPS")
	}
	for _, path := range []string{filepath.Clean("ssl/web.crt"), filepath.Clean("ssl/web.key")} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("expected HTTPS file %s: %v", path, err)
		}
	}
}

func TestInitHandlerAcceptsManualHTTPSCertificate(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	cfg := clientInitTestConfig()
	certPEM, keyPEM, err := generateSelfSignedCertificate("127.0.0.1")
	if err != nil {
		t.Fatalf("generate test certificate: %v", err)
	}

	handler := NewInitHandler(cfg, make(chan *config.Config, 1))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/init/config", strings.NewReader(validClientInitBody(t, cfg, map[string]any{
		"web_https_enabled":  true,
		"web_https_mode":     "manual",
		"web_https_cert_pem": string(certPEM),
		"web_https_key_pem":  string(keyPEM),
	}))))
	if rec.Code != http.StatusOK {
		t.Fatalf("init post status=%d body=%s", rec.Code, rec.Body.String())
	}
	for _, path := range []string{filepath.Clean("ssl/web.crt"), filepath.Clean("ssl/web.key")} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("expected manual HTTPS file %s: %v", path, err)
		}
	}
}

func TestInitHandlerRejectsInvalidManualHTTPSCertificate(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	cfg := clientInitTestConfig()

	handler := NewInitHandler(cfg, make(chan *config.Config, 1))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/init/config", strings.NewReader(validClientInitBody(t, cfg, map[string]any{
		"web_https_enabled":  true,
		"web_https_mode":     "manual",
		"web_https_cert_pem": "",
		"web_https_key_pem":  "",
	}))))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d want 400 body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "请填写 HTTPS 证书和私钥") {
		t.Fatalf("body=%s want manual HTTPS validation message", rec.Body.String())
	}
}

func TestInitHandlerRejectsInvalidAdminSetup(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	cfg := clientInitTestConfig()

	handler := NewInitHandler(cfg, make(chan *config.Config, 1))
	cases := []struct {
		name      string
		overrides map[string]any
		want      string
	}{
		{name: "missing username", overrides: map[string]any{"admin_username": " "}, want: "控制台账号不能为空"},
		{name: "short password", overrides: map[string]any{"admin_password": "Abc123"}, want: "控制台密码至少 8 位"},
		{name: "password without digit", overrides: map[string]any{"admin_password": "abcdefgh"}, want: "控制台密码至少 8 位"},
		{name: "password without letter", overrides: map[string]any{"admin_password": "12345678"}, want: "控制台密码至少 8 位"},
		{name: "invalid environment", overrides: map[string]any{"environment": "staging"}, want: "运行模式只能选择测试模式或生产模式"},
		{name: "terms not agreed", overrides: map[string]any{"agree_terms": false}, want: "请先阅读并同意用户协议"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/init/config", strings.NewReader(validClientInitBody(t, cfg, tc.overrides))))
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status=%d want 400 body=%s", rec.Code, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), tc.want) {
				t.Fatalf("body=%s want contains %q", rec.Body.String(), tc.want)
			}
		})
	}
}

func TestInitializationURLUsesLoopbackForWildcardHost(t *testing.T) {
	cfg := config.Default()
	cfg.HTTP.Host = "0.0.0.0"
	cfg.HTTP.Port = 25520

	if got := InitializationURL(cfg); got != "http://127.0.0.1:25520" {
		t.Fatalf("initialization url=%q want loopback url", got)
	}
}

func TestRunInitializationReturnsSubmittedConfig(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	cfg := clientInitTestConfig()
	cfg.HTTP.Host = "127.0.0.1"
	cfg.HTTP.Port = freeTCPPort(t)
	cfg.Database.Path = filepath.Join("data", "init-run.db")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	resultCh := make(chan *config.Config, 1)
	errCh := make(chan error, 1)
	go func() {
		result, err := RunInitialization(ctx, cfg)
		if err != nil {
			errCh <- err
			return
		}
		resultCh <- result
	}()

	resp, err := postWithRetry("http://127.0.0.1:"+strconv.Itoa(cfg.HTTP.Port)+"/api/init/config", validClientInitBody(t, cfg, map[string]any{
		"http_port": 25520,
	}))
	if err != nil {
		t.Fatalf("post init config: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("post status=%d", resp.StatusCode)
	}

	select {
	case result := <-resultCh:
		if result.HTTP.Port != 25520 {
			t.Fatalf("result http.port=%d want 25520", result.HTTP.Port)
		}
	case err := <-errCh:
		t.Fatalf("run initialization: %v", err)
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for initialization")
	}
}

func clientInitTestConfig() *config.Config {
	cfg := config.Default()
	cfg.Database.Path = filepath.Join("data", "init-client.db")
	cfg.Log.Dir = "logs"
	cfg.Auth.SM2PrivateKeyFile = filepath.Join("data", "sm2_private.pem")
	cfg.Auth.SM2PublicKeyFile = filepath.Join("data", "sm2_public.pem")
	return cfg
}

func validClientInitBody(t *testing.T, cfg *config.Config, overrides map[string]any) string {
	t.Helper()
	body := map[string]any{
		"admin_username": "owner",
		"admin_password": "Owner1234",
		"environment":    "development",
		"agree_terms":    true,
		"http_port":      cfg.HTTP.Port,
		"server_host":    cfg.ServerDefaults.ServerHost,
		"control_port":   cfg.ServerDefaults.ControlPort,
		"data_port":      cfg.ServerDefaults.DataPort,
		"database_path":  cfg.Database.Path,
	}
	for key, value := range overrides {
		body[key] = value
	}
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal init body: %v", err)
	}
	return string(raw)
}

func freeTCPPort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen free port: %v", err)
	}
	defer listener.Close()
	_, portText, err := net.SplitHostPort(listener.Addr().String())
	if err != nil {
		t.Fatalf("split listener addr: %v", err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatalf("parse port: %v", err)
	}
	return port
}

func postWithRetry(url string, body string) (*http.Response, error) {
	var lastErr error
	for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); {
		resp, err := http.Post(url, "application/json", strings.NewReader(body))
		if err == nil {
			return resp, nil
		}
		lastErr = err
		time.Sleep(10 * time.Millisecond)
	}
	return nil, lastErr
}

func assertContainsAll(t *testing.T, body string, values ...string) {
	t.Helper()
	for _, want := range values {
		if !strings.Contains(body, want) {
			t.Fatalf("body missing %q: %s", want, body)
		}
	}
}
