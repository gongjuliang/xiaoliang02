package startup

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"nattuser/internal/auth"
	"nattuser/internal/config"
	"nattuser/internal/db"
	"nattuser/internal/model"
)

type initRequest struct {
	HTTPHost      string `json:"http_host"`
	HTTPPort      int    `json:"http_port"`
	ServerHost    string `json:"server_host"`
	ControlPort   int    `json:"control_port"`
	DataPort      int    `json:"data_port"`
	DatabasePath  string `json:"database_path"`
	LogDir        string `json:"log_dir"`
	JWTSecret     string `json:"jwt_secret"`
	AdminUsername string `json:"admin_username"`
	AdminPassword string `json:"admin_password"`
	Environment   string `json:"environment"`
	AgreeTerms    bool   `json:"agree_terms"`
}

func NewInitHandler(defaultCfg *config.Config, done chan<- *config.Config) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" && r.URL.Path != "/init.html" {
			http.NotFound(w, r)
			return
		}
		writeHTML(w, clientInitHTML(defaultCfg))
	})
	mux.HandleFunc("/api/init/status", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		_, err := os.Stat(config.DefaultPath)
		writeJSON(w, http.StatusOK, map[string]any{
			"code":           0,
			"message":        "ok",
			"config_exists":  err == nil,
			"default_config": defaultCfg,
		})
	})
	mux.HandleFunc("/api/init/config", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		cfg, err := buildInitialConfig(r.Context(), defaultCfg, r)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"code": 40001, "message": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"code": 0, "message": "初始化完成，正在启动正常控制台", "data": cfg})
		select {
		case done <- cfg:
		default:
		}
	})
	return mux
}

func RunInitialization(ctx context.Context, defaultCfg *config.Config) (*config.Config, error) {
	if defaultCfg == nil {
		defaultCfg = config.Default()
	}
	if err := CheckPorts([]PortCheck{{Name: "HTTP管理端口", Host: defaultCfg.HTTP.Host, Port: defaultCfg.HTTP.Port}}); err != nil {
		return nil, err
	}
	fmt.Printf("系统需要初始化，默认配置文件为 %s。\n", config.DefaultPath)
	fmt.Printf("请在浏览器打开 %s/init.html 完成初始化。\n", InitializationURL(defaultCfg))

	done := make(chan *config.Config, 1)
	server := &http.Server{
		Addr:         net.JoinHostPort(defaultCfg.HTTP.Host, strconv.Itoa(defaultCfg.HTTP.Port)),
		Handler:      NewInitHandler(defaultCfg, done),
		ReadTimeout:  time.Duration(defaultCfg.HTTP.ReadTimeoutSeconds) * time.Second,
		WriteTimeout: time.Duration(defaultCfg.HTTP.WriteTimeoutSeconds) * time.Second,
	}
	errCh := make(chan error, 1)
	go func() {
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errCh <- err
			return
		}
		errCh <- nil
	}()

	select {
	case cfg := <-done:
		fmt.Println("初始化完成，正在切换到正常控制台...")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
		<-errCh
		return cfg, nil
	case err := <-errCh:
		return nil, err
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
		return nil, ctx.Err()
	}
}

func InitializationURL(cfg *config.Config) string {
	if cfg == nil {
		cfg = config.Default()
	}
	host := strings.TrimSpace(cfg.HTTP.Host)
	if host == "" || host == "0.0.0.0" || host == "::" || host == "[::]" {
		host = "127.0.0.1"
	}
	return "http://" + net.JoinHostPort(host, strconv.Itoa(cfg.HTTP.Port))
}

func buildInitialConfig(ctx context.Context, base *config.Config, r *http.Request) (*config.Config, error) {
	cfg := *base
	var req initRequest
	if r.Body != nil {
		decoder := json.NewDecoder(r.Body)
		if err := decoder.Decode(&req); err != nil && err.Error() != "EOF" {
			return nil, fmt.Errorf("JSON格式错误")
		}
	}
	adminUsername, adminPassword, environment, err := validateInitRequest(req, cfg.App.Environment)
	if err != nil {
		return nil, err
	}
	cfg.App.Environment = environment
	applyInitRequest(&cfg, req)
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if err := writeConfigFile(&cfg); err != nil {
		return nil, err
	}
	if err := ensureRuntimeFiles(ctx, &cfg, adminUsername, adminPassword); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func validateInitRequest(req initRequest, defaultEnvironment string) (string, string, string, error) {
	username := strings.TrimSpace(req.AdminUsername)
	if username == "" {
		return "", "", "", fmt.Errorf("控制台账号不能为空")
	}
	password := strings.TrimSpace(req.AdminPassword)
	if !validAdminPassword(password) {
		return "", "", "", fmt.Errorf("控制台密码至少 8 位，且必须同时包含字母和数字")
	}
	if !req.AgreeTerms {
		return "", "", "", fmt.Errorf("请先阅读并同意用户协议")
	}
	environment := strings.TrimSpace(req.Environment)
	if environment == "" {
		environment = strings.TrimSpace(defaultEnvironment)
	}
	if environment == "" {
		environment = "development"
	}
	if environment != "development" && environment != "production" {
		return "", "", "", fmt.Errorf("运行模式只能选择测试模式或生产模式")
	}
	return username, password, environment, nil
}

func validAdminPassword(password string) bool {
	if len(password) < 8 {
		return false
	}
	hasLetter := false
	hasDigit := false
	for _, r := range password {
		if r >= '0' && r <= '9' {
			hasDigit = true
			continue
		}
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') {
			hasLetter = true
		}
	}
	return hasLetter && hasDigit
}

func applyInitRequest(cfg *config.Config, req initRequest) {
	if strings.TrimSpace(req.HTTPHost) != "" {
		cfg.HTTP.Host = strings.TrimSpace(req.HTTPHost)
	}
	if req.HTTPPort > 0 {
		cfg.HTTP.Port = req.HTTPPort
	}
	if strings.TrimSpace(req.ServerHost) != "" {
		cfg.ServerDefaults.ServerHost = strings.TrimSpace(req.ServerHost)
	}
	if req.ControlPort > 0 {
		cfg.ServerDefaults.ControlPort = req.ControlPort
	}
	if req.DataPort > 0 {
		cfg.ServerDefaults.DataPort = req.DataPort
	}
	if strings.TrimSpace(req.DatabasePath) != "" {
		cfg.Database.Path = strings.TrimSpace(req.DatabasePath)
	}
	if strings.TrimSpace(req.LogDir) != "" {
		cfg.Log.Dir = strings.TrimSpace(req.LogDir)
	}
	if strings.TrimSpace(req.JWTSecret) != "" {
		cfg.Auth.JWTSecret = strings.TrimSpace(req.JWTSecret)
	}
}

func writeConfigFile(cfg *config.Config) error {
	if err := os.MkdirAll(filepath.Dir(config.DefaultPath), 0o755); err != nil {
		return fmt.Errorf("创建配置目录失败: %w", err)
	}
	content, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("生成配置失败: %w", err)
	}
	if err := os.WriteFile(config.DefaultPath, append(content, '\n'), 0o644); err != nil {
		return fmt.Errorf("写入配置文件失败: %w", err)
	}
	return nil
}

func ensureRuntimeFiles(ctx context.Context, cfg *config.Config, adminUsername string, adminPassword string) error {
	if err := os.MkdirAll(cfg.Log.Dir, 0o755); err != nil {
		return fmt.Errorf("创建日志目录失败: %w", err)
	}
	if _, err := auth.NewSM2Cipher(cfg.Auth.SM2PrivateKeyFile, cfg.Auth.SM2PublicKeyFile); err != nil {
		return fmt.Errorf("初始化SM2密钥失败: %w", err)
	}
	database, err := db.Open(ctx, cfg.Database.Path, nil)
	if err != nil {
		return fmt.Errorf("初始化数据库失败: %w", err)
	}
	defer database.Close()
	count, err := db.CountUsers(ctx, database)
	if err != nil {
		return fmt.Errorf("检查控制台账号失败: %w", err)
	}
	if count > 0 {
		return nil
	}
	hash, err := auth.HashPassword(adminPassword)
	if err != nil {
		return fmt.Errorf("生成控制台密码哈希失败: %w", err)
	}
	if _, err := db.CreateUser(ctx, database, db.CreateUserParams{
		Username:     adminUsername,
		PasswordHash: hash,
		Role:         model.UserRoleAdmin,
	}); err != nil {
		return fmt.Errorf("创建控制台管理员失败: %w", err)
	}
	return nil
}

func writeHTML(w http.ResponseWriter, body string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(body))
}

func writeJSON(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(data)
}

func selectedAttr(current string, value string) string {
	if strings.TrimSpace(current) == value {
		return "selected"
	}
	return ""
}

func clientInitHTML(cfg *config.Config) string {
	return fmt.Sprintf(`<!doctype html>
<html lang="zh-CN">
<head>
  <meta charset="utf-8">
  <title>初始化 NATT User</title>
  <style>
    body{font-family:Arial,"Microsoft YaHei",sans-serif;margin:40px;background:#f7f8fa;color:#1f2937}
    .box{max-width:760px;background:#fff;border:1px solid #e5e7eb;border-radius:8px;padding:24px}
    label{display:block;margin-top:14px}
    input,select{width:100%%;box-sizing:border-box;padding:9px;border:1px solid #d1d5db;border-radius:6px}
    .check-row{display:flex;align-items:center;gap:8px;margin-top:16px}
    .check-row input{width:auto}
    button{margin-top:18px;padding:10px 18px;border:0;border-radius:6px;background:#1677ff;color:#fff;cursor:pointer}
    pre{white-space:pre-wrap;color:#0f766e}
  </style>
</head>
<body>
<div class="box">
  <h1>初始化 NATT User</h1>
  <label>控制台账号<input id="admin_username" autocomplete="username"></label>
  <label>控制台密码<input id="admin_password" type="password" autocomplete="new-password" placeholder="至少 8 位，必须包含字母和数字"></label>
  <label>运行模式<select id="environment">
    <option value="development" %s>测试模式</option>
    <option value="production" %s>生产模式</option>
  </select></label>
  <label>HTTP 端口<input id="http_port" type="number" value="%d"></label>
  <label>默认服务端地址<input id="server_host" value="%s"></label>
  <label>默认控制端口<input id="control_port" type="number" value="%d"></label>
  <label>默认数据端口<input id="data_port" type="number" value="%d"></label>
  <label>数据库路径<input id="database_path" value="%s"></label>
  <label class="check-row"><input id="agree_terms" type="checkbox"><span>已阅读并同意《用户协议》</span></label>
  <button onclick="submitInit()">保存并启动</button>
  <pre id="result"></pre>
</div>
<script>
function submitInit(){
  fetch('/api/init/config',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({
    admin_username:document.getElementById('admin_username').value,
    admin_password:document.getElementById('admin_password').value,
    environment:document.getElementById('environment').value,
    agree_terms:document.getElementById('agree_terms').checked,
    http_port:Number(document.getElementById('http_port').value),
    server_host:document.getElementById('server_host').value,
    control_port:Number(document.getElementById('control_port').value),
    data_port:Number(document.getElementById('data_port').value),
    database_path:document.getElementById('database_path').value
  })}).then(function(r){return r.json();}).then(function(d){
    document.getElementById('result').textContent=d.message||'初始化完成，正在启动控制台';
    if(d.code===0){
      var port=d.data&&d.data.http&&d.data.http.port;
      var target=port ? (window.location.protocol+'//'+window.location.hostname+':'+port+'/login.html') : '/login.html';
      setTimeout(function(){ window.location.href=target; }, 1800);
    }
  });
}
</script>
</body>
</html>`, selectedAttr(cfg.App.Environment, "development"), selectedAttr(cfg.App.Environment, "production"), cfg.HTTP.Port, html.EscapeString(cfg.ServerDefaults.ServerHost), cfg.ServerDefaults.ControlPort, cfg.ServerDefaults.DataPort, html.EscapeString(cfg.Database.Path))
}
