package startup

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"html"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"nattserver/internal/auth"
	"nattserver/internal/config"
	"nattserver/internal/db"
	"nattserver/internal/model"
)

const serverBrandName = "工具人小良-内网穿透服务端"

type initRequest struct {
	HTTPHost        string `json:"http_host"`
	HTTPPort        int    `json:"http_port"`
	ControlHost     string `json:"control_host"`
	ControlPort     int    `json:"control_port"`
	DataHost        string `json:"data_host"`
	DataPort        int    `json:"data_port"`
	DatabasePath    string `json:"database_path"`
	LogDir          string `json:"log_dir"`
	JWTSecret       string `json:"jwt_secret"`
	RemotePortMin   int    `json:"remote_port_min"`
	RemotePortMax   int    `json:"remote_port_max"`
	AdminUsername   string `json:"admin_username"`
	AdminPassword   string `json:"admin_password"`
	Environment     string `json:"environment"`
	AgreeTerms      bool   `json:"agree_terms"`
	WebHTTPSEnabled bool   `json:"web_https_enabled"`
	WebHTTPSMode    string `json:"web_https_mode"`
	WebHTTPSCertPEM string `json:"web_https_cert_pem"`
	WebHTTPSKeyPEM  string `json:"web_https_key_pem"`
}

func NewInitHandler(defaultCfg *config.Config, done chan<- *config.Config) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" && r.URL.Path != "/init.html" {
			http.NotFound(w, r)
			return
		}
		writeHTML(w, serverInitHTML(defaultCfg))
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
	if base == nil {
		base = config.Default()
	}
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
	if err := prepareHTTPSFiles(&cfg, req); err != nil {
		return nil, err
	}
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
	cfg.HTTP.HTTPSEnabled = req.WebHTTPSEnabled
	cfg.HTTP.CertFile = filepath.Clean("ssl/web.crt")
	cfg.HTTP.KeyFile = filepath.Clean("ssl/web.key")
	if strings.TrimSpace(req.ControlHost) != "" {
		cfg.Protocol.ControlHost = strings.TrimSpace(req.ControlHost)
	}
	if req.ControlPort > 0 {
		cfg.Protocol.ControlPort = req.ControlPort
	}
	if strings.TrimSpace(req.DataHost) != "" {
		cfg.Protocol.DataHost = strings.TrimSpace(req.DataHost)
	}
	if req.DataPort > 0 {
		cfg.Protocol.DataPort = req.DataPort
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
	if req.RemotePortMin >= 0 && req.RemotePortMax > 0 {
		cfg.Tunnel.RemotePortMin = req.RemotePortMin
		cfg.Tunnel.RemotePortMax = req.RemotePortMax
	}
}

func prepareHTTPSFiles(cfg *config.Config, req initRequest) error {
	if !cfg.HTTP.HTTPSEnabled {
		return nil
	}
	mode := strings.TrimSpace(req.WebHTTPSMode)
	if mode == "" {
		mode = "auto"
	}
	var certPEM []byte
	var keyPEM []byte
	switch mode {
	case "manual":
		certPEM = []byte(strings.TrimSpace(req.WebHTTPSCertPEM))
		keyPEM = []byte(strings.TrimSpace(req.WebHTTPSKeyPEM))
		if len(certPEM) == 0 || len(keyPEM) == 0 {
			return fmt.Errorf("请填写 HTTPS 证书和私钥")
		}
		if _, err := tls.X509KeyPair(certPEM, keyPEM); err != nil {
			return fmt.Errorf("HTTPS 证书或私钥格式不正确: %w", err)
		}
	case "auto":
		generatedCert, generatedKey, err := generateSelfSignedCertificate(cfg.HTTP.Host)
		if err != nil {
			return fmt.Errorf("生成 HTTPS 测试证书失败: %w", err)
		}
		certPEM = generatedCert
		keyPEM = generatedKey
	default:
		return fmt.Errorf("HTTPS 证书方式只能选择填写证书或自动生成测试证书")
	}
	if err := writeHTTPSFile(cfg.HTTP.CertFile, certPEM, 0o644); err != nil {
		return err
	}
	if err := writeHTTPSFile(cfg.HTTP.KeyFile, keyPEM, 0o600); err != nil {
		return err
	}
	return nil
}

func generateSelfSignedCertificate(host string) ([]byte, []byte, error) {
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, nil, err
	}
	serialNumber, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, nil, err
	}
	template := x509.Certificate{
		SerialNumber: serialNumber,
		Subject: pkix.Name{
			CommonName: serverBrandName + "测试证书",
		},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().AddDate(1, 0, 0),
		KeyUsage:              x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}
	addCertificateHost(&template, host)
	certDER, err := x509.CreateCertificate(rand.Reader, &template, &template, &privateKey.PublicKey, privateKey)
	if err != nil {
		return nil, nil, err
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(privateKey)})
	return certPEM, keyPEM, nil
}

func addCertificateHost(cert *x509.Certificate, host string) {
	host = strings.TrimSpace(host)
	if host == "" || host == "0.0.0.0" || host == "::" || host == "[::]" {
		cert.DNSNames = append(cert.DNSNames, "localhost")
		cert.IPAddresses = append(cert.IPAddresses, net.ParseIP("127.0.0.1"))
		return
	}
	if ip := net.ParseIP(strings.Trim(host, "[]")); ip != nil {
		cert.IPAddresses = append(cert.IPAddresses, ip)
		return
	}
	cert.DNSNames = append(cert.DNSNames, host)
}

func writeHTTPSFile(path string, content []byte, perm os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("创建 HTTPS 证书目录失败: %w", err)
	}
	if err := os.WriteFile(path, content, perm); err != nil {
		return fmt.Errorf("写入 HTTPS 证书文件失败: %w", err)
	}
	return nil
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
		return fmt.Errorf("初始化 SM2 密钥失败: %w", err)
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

func checkedAttr(value bool) string {
	if value {
		return "checked"
	}
	return ""
}

func serverInitHTML(cfg *config.Config) string {
	return fmt.Sprintf(`<!doctype html>
<html lang="zh-CN">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>初始化 %s</title>
  <style>
    body{font-family:Arial,"Microsoft YaHei",sans-serif;margin:0;background:#f4f7fb;color:#1f2937;padding:32px}
    .box{max-width:860px;margin:0 auto;background:#fff;border:1px solid #e5e7eb;border-radius:8px;padding:24px;box-shadow:0 16px 40px rgba(15,23,42,.08)}
    h1{margin:0 0 6px;font-size:24px;line-height:1.25}
    p{margin:0 0 18px;color:#64748b}
    label{display:block;margin-top:14px}
    input,select,textarea{width:100%%;box-sizing:border-box;padding:9px;border:1px solid #d1d5db;border-radius:6px}
    textarea{min-height:120px;font-family:Consolas,monospace}
    .grid{display:grid;grid-template-columns:repeat(3,minmax(0,1fr));gap:12px}
    .check-row{display:flex;align-items:center;gap:8px;margin-top:16px}
    .check-row input{width:auto;margin:0}
    .https-options{display:none;margin-top:12px;padding:14px;border:1px solid #e5e7eb;border-radius:8px;background:#f8fafc}
    .https-options.show{display:block}
    .radio-row{display:flex;align-items:center;gap:18px;margin-top:10px}
    .radio-row label{display:flex;align-items:center;gap:6px;margin:0}
    .radio-row input{width:auto}
    button{margin-top:18px;padding:10px 18px;border:0;border-radius:6px;background:#0f766e;color:#fff;cursor:pointer}
    pre{white-space:pre-wrap;color:#0f766e}
    @media(max-width:720px){body{padding:16px}.grid{grid-template-columns:1fr}}
  </style>
</head>
<body>
<div class="box">
  <h1>初始化 %s</h1>
  <p>首次启动需要创建配置文件、数据库、密钥文件和控制台管理员。</p>
  <label>控制台账号<input id="admin_username" autocomplete="username"></label>
  <label>控制台密码<input id="admin_password" type="password" autocomplete="new-password" placeholder="至少 8 位，必须包含字母和数字"></label>
  <label>运行模式<select id="environment">
    <option value="development" %s>测试模式</option>
    <option value="production" %s>生产模式</option>
  </select></label>
  <div class="grid">
    <label>Web 端口<input id="http_port" type="number" value="%d"></label>
    <label>控制端口<input id="control_port" type="number" value="%d"></label>
    <label>数据端口<input id="data_port" type="number" value="%d"></label>
  </div>
  <label>数据库路径<input id="database_path" value="%s"></label>
  <label class="check-row"><input id="web_https_enabled" type="checkbox" %s><span>Web 控制台启用 HTTPS</span></label>
  <div id="https_options" class="https-options">
    <div class="radio-row">
      <label><input name="web_https_mode" type="radio" value="auto" checked>自动生成测试证书</label>
      <label><input name="web_https_mode" type="radio" value="manual">填写证书</label>
    </div>
    <p>证书会保存到 ssl/web.crt，私钥会保存到 ssl/web.key。自动生成的测试证书不建议用于生产环境。</p>
    <label>HTTPS 证书 PEM<textarea id="web_https_cert_pem" placeholder="-----BEGIN CERTIFICATE-----"></textarea></label>
    <label>HTTPS 私钥 PEM<textarea id="web_https_key_pem" placeholder="-----BEGIN PRIVATE KEY----- 或 -----BEGIN RSA PRIVATE KEY-----"></textarea></label>
  </div>
  <label class="check-row"><input id="agree_terms" type="checkbox"><span>已阅读并同意《用户协议》</span></label>
  <button onclick="submitInit()">保存并启动</button>
  <pre id="result"></pre>
</div>
<script>
function updateHTTPSOptions(){
  var enabled=document.getElementById('web_https_enabled').checked;
  document.getElementById('https_options').className=enabled?'https-options show':'https-options';
}
document.getElementById('web_https_enabled').addEventListener('change',updateHTTPSOptions);
updateHTTPSOptions();
function selectedHTTPSMode(){
  var item=document.querySelector('input[name="web_https_mode"]:checked');
  return item?item.value:'auto';
}
function submitInit(){
  fetch('/api/init/config',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({
    admin_username:document.getElementById('admin_username').value,
    admin_password:document.getElementById('admin_password').value,
    environment:document.getElementById('environment').value,
    agree_terms:document.getElementById('agree_terms').checked,
    http_port:Number(document.getElementById('http_port').value),
    control_port:Number(document.getElementById('control_port').value),
    data_port:Number(document.getElementById('data_port').value),
    database_path:document.getElementById('database_path').value,
    web_https_enabled:document.getElementById('web_https_enabled').checked,
    web_https_mode:selectedHTTPSMode(),
    web_https_cert_pem:document.getElementById('web_https_cert_pem').value,
    web_https_key_pem:document.getElementById('web_https_key_pem').value
  })}).then(function(r){return r.json();}).then(function(d){
    document.getElementById('result').textContent=d.message||'初始化完成，正在启动控制台';
    if(d.code===0){
      var port=d.data&&d.data.http&&d.data.http.port;
      var https=d.data&&d.data.http&&d.data.http.https_enabled;
      var scheme=https?'https':'http';
      var target=port ? (scheme+'://'+window.location.hostname+':'+port+'/login.html') : '/login.html';
      setTimeout(function(){ window.location.href=target; }, 1800);
    }
  });
}
</script>
</body>
</html>`, serverBrandName, serverBrandName, selectedAttr(cfg.App.Environment, "development"), selectedAttr(cfg.App.Environment, "production"), cfg.HTTP.Port, cfg.Protocol.ControlPort, cfg.Protocol.DataPort, html.EscapeString(cfg.Database.Path), checkedAttr(cfg.HTTP.HTTPSEnabled))
}
