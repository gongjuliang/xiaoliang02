package startup

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"html/template"
	"io/fs"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	embedfiles "nattuser/Web/EmbedFiles"
	"nattuser/internal/auth"
	"nattuser/internal/config"
	"nattuser/internal/db"
	"nattuser/internal/model"
)

const clientBrandName = "工具人小良-内网穿透客户端"

type initRequest struct {
	HTTPHost        string `json:"http_host"`
	HTTPPort        int    `json:"http_port"`
	ServerHost      string `json:"server_host"`
	ControlPort     int    `json:"control_port"`
	DataPort        int    `json:"data_port"`
	DatabasePath    string `json:"database_path"`
	LogDir          string `json:"log_dir"`
	JWTSecret       string `json:"jwt_secret"`
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
	tmpl := template.Must(template.New("").ParseFS(embedfiles.WebFs, "Templates/init.html", "Templates/agreement.html"))
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" && r.URL.Path != "/init.html" {
			http.NotFound(w, r)
			return
		}
		writeTemplate(w, tmpl, "init.html", newInitPageData(defaultCfg))
	})
	mux.HandleFunc("/agreement.html", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		writeTemplate(w, tmpl, "agreement.html", nil)
	})
	if mdFS, err := fs.Sub(embedfiles.WebFs, "Static/md"); err == nil {
		mux.Handle("/static/md/", http.StripPrefix("/static/md/", http.FileServer(http.FS(mdFS))))
	}
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

type initPageData struct {
	BrandName    string
	Environment  string
	HTTPHost     string
	HTTPPort     int
	ControlPort  int
	DataPort     int
	DatabasePath string
	HTTPSEnabled bool
	CertFile     string
	KeyFile      string
}

func newInitPageData(cfg *config.Config) initPageData {
	if cfg == nil {
		cfg = config.Default()
	}
	return initPageData{
		BrandName:    clientBrandName,
		Environment:  cfg.App.Environment,
		HTTPHost:     cfg.HTTP.Host,
		HTTPPort:     cfg.HTTP.Port,
		ControlPort:  cfg.ServerDefaults.ControlPort,
		DataPort:     cfg.ServerDefaults.DataPort,
		DatabasePath: cfg.Database.Path,
		HTTPSEnabled: cfg.HTTP.HTTPSEnabled,
		CertFile:     cfg.HTTP.CertFile,
		KeyFile:      cfg.HTTP.KeyFile,
	}
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
	if strings.TrimSpace(req.JWTSecret) == "" {
		if err := assignRandomJWTSecret(&cfg); err != nil {
			return nil, err
		}
	}
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

func assignRandomJWTSecret(cfg *config.Config) error {
	secret, err := generateJWTSecret()
	if err != nil {
		return fmt.Errorf("生成 JWT 密钥失败: %w", err)
	}
	cfg.Auth.JWTSecret = secret
	return nil
}

func generateJWTSecret() (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

func applyInitRequest(cfg *config.Config, req initRequest) {
	if strings.TrimSpace(req.HTTPHost) != "" {
		cfg.HTTP.Host = strings.TrimSpace(req.HTTPHost)
	}
	if req.HTTPPort > 0 {
		cfg.HTTP.Port = req.HTTPPort
	}
	cfg.HTTP.HTTPSEnabled = req.WebHTTPSEnabled
	cfg.HTTP.CertFile = filepath.Clean(filepath.Join(config.RuntimeRoot, "ssl", "web.crt"))
	cfg.HTTP.KeyFile = filepath.Clean(filepath.Join(config.RuntimeRoot, "ssl", "web.key"))
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
			CommonName: clientBrandName + "测试证书",
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

func writeTemplate(w http.ResponseWriter, tmpl *template.Template, name string, data any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := tmpl.ExecuteTemplate(w, name, data); err != nil {
		http.Error(w, "render template failed", http.StatusInternalServerError)
	}
}

func writeJSON(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(data)
}
