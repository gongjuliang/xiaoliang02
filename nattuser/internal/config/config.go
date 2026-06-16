// Package config 提供NATT客户端的配置加载、验证和默认值管理功能。
// 支持JSON和YAML两种配置格式，包含应用、HTTP、数据库、日志、认证和服务端默认端口等配置项。
package config

import (
	// encoding/json 提供JSON格式的序列化和反序列化。
	"encoding/json"
	// errors 提供错误类型定义和判断。
	"errors"
	// fmt 提供错误信息的格式化输出。
	"fmt"
	// os 提供文件系统操作。
	"os"
	// path/filepath 提供文件路径拼接和规范化。
	"path/filepath"
	// strings 提供字符串处理函数。
	"strings"

	// github.com/goccy/go-yaml 提供YAML格式的解析。
	"github.com/goccy/go-yaml"
)

const (
	// RuntimeRoot 客户端运行时文件根目录，所有相对路径都基于此目录。
	RuntimeRoot = "xiaoliang02_user"
)

var (
	// DefaultPath 默认配置文件路径，基于RuntimeRoot下的config/config.json。
	DefaultPath = filepath.Join(RuntimeRoot, "config", "config.json")
	// LegacyYAMLPath 兼容旧版YAML配置文件的路径。
	LegacyYAMLPath = filepath.Join(RuntimeRoot, "config", "config.yaml")
)

// ErrDefaultConfigMissing 默认配置文件不存在时返回的错误。
// 当默认启动路径下找不到配置文件时，触发初始化向导流程。
var ErrDefaultConfigMissing = errors.New("default config missing")

// Config 客户端顶层配置结构体，包含所有子配置模块。
type Config struct {
	App            AppConfig            `yaml:"app" json:"app"`
	HTTP           HTTPConfig           `yaml:"http" json:"http"`
	Database       DatabaseConfig       `yaml:"database" json:"database"`
	Log            LogConfig            `yaml:"log" json:"log"`
	Auth           AuthConfig           `yaml:"auth" json:"auth"`
	ServerDefaults ServerDefaultsConfig `yaml:"server_defaults" json:"server_defaults"`
	MCP            MCPConfig            `yaml:"-" json:"-"`
}

// AppConfig 应用基本配置，包含应用名称、版本号和运行环境。
type AppConfig struct {
	Name        string `yaml:"name" json:"name"`               // 应用名称
	Version     string `yaml:"version" json:"version"`         // 应用版本号
	Environment string `yaml:"environment" json:"environment"` // 运行环境（production/development）
}

// HTTPConfig HTTP服务配置，包含监听地址、端口、HTTPS和超时设置。
type HTTPConfig struct {
	Host                   string `yaml:"host" json:"host"`                                         // 监听地址
	Port                   int    `yaml:"port" json:"port"`                                         // 监听端口（默认25520）
	HTTPSEnabled           bool   `yaml:"https_enabled" json:"https_enabled"`                       // 是否启用HTTPS
	CertFile               string `yaml:"cert_file" json:"cert_file"`                               // HTTPS证书文件路径
	KeyFile                string `yaml:"key_file" json:"key_file"`                                 // HTTPS私钥文件路径
	ReadTimeoutSeconds     int    `yaml:"read_timeout_seconds" json:"read_timeout_seconds"`         // 读取超时秒数
	WriteTimeoutSeconds    int    `yaml:"write_timeout_seconds" json:"write_timeout_seconds"`       // 写入超时秒数
	IdleTimeoutSeconds     int    `yaml:"idle_timeout_seconds" json:"idle_timeout_seconds"`         // 空闲超时秒数
	ShutdownTimeoutSeconds int    `yaml:"shutdown_timeout_seconds" json:"shutdown_timeout_seconds"` // 优雅关闭超时秒数
}

// DatabaseConfig 数据库配置，指定SQLite数据库文件路径。
type DatabaseConfig struct {
	Path string `yaml:"path" json:"path"` // SQLite数据库文件路径
}

// LogConfig 日志配置，指定日志目录和日志级别。
type LogConfig struct {
	Dir   string `yaml:"dir" json:"dir"`     // 日志文件目录
	Level string `yaml:"level" json:"level"` // 日志级别（debug/info/warn/error）
}

// AuthConfig 认证配置，包含JWT、SM2加密、登录限流等安全相关设置。
type AuthConfig struct {
	JWTSecret               string `yaml:"jwt_secret" json:"jwt_secret"`                                   // JWT签名密钥
	AccessTokenTTLMinutes   int    `yaml:"access_token_ttl_minutes" json:"access_token_ttl_minutes"`       // 访问令牌有效期（分钟）
	RefreshTokenTTLMinutes  int    `yaml:"refresh_token_ttl_minutes" json:"refresh_token_ttl_minutes"`     // 刷新令牌有效期（分钟）
	SM2PrivateKeyFile       string `yaml:"sm2_private_key_file" json:"sm2_private_key_file"`               // SM2私钥文件路径
	SM2PublicKeyFile        string `yaml:"sm2_public_key_file" json:"sm2_public_key_file"`                 // SM2公钥文件路径
	LoginRateLimitPerMinute int    `yaml:"login_rate_limit_per_minute" json:"login_rate_limit_per_minute"` // 每分钟登录请求限流次数
	AllowPlaintextPassword  bool   `yaml:"allow_plaintext_password" json:"allow_plaintext_password"`       // 是否允许明文密码传输（开发环境）
}

// ServerDefaultsConfig 服务端默认配置，定义客户端连接服务端时使用的默认端口。
type ServerDefaultsConfig struct {
	ServerHost  string `yaml:"server_host" json:"server_host"`   // 默认服务端地址（客户端可在隧道连接中单独指定）
	ControlPort int    `yaml:"control_port" json:"control_port"` // 默认控制通道端口（服务端默认25511）
	DataPort    int    `yaml:"data_port" json:"data_port"`       // 默认数据通道端口（服务端默认25512）
}

// MCPConfig MCP（Model Context Protocol）服务配置。
// 这些配置不序列化到配置文件，仅在运行时使用。
type MCPConfig struct {
	Enabled     bool   `yaml:"-" json:"-"` // 是否启用MCP服务
	Host        string `yaml:"-" json:"-"` // MCP服务监听地址
	Port        int    `yaml:"-" json:"-"` // MCP服务监听端口
	AccessToken string `yaml:"-" json:"-"` // MCP访问令牌
}

// Load 从指定路径加载配置文件并解析为Config结构体。
// 如果path为空，则使用默认配置文件路径。
// 参数path：配置文件路径（空字符串表示使用默认路径）。
// 返回值：解析后的配置和可能的错误。
func Load(path string) (*Config, error) {
	if path == "" {
		resolvedPath, err := defaultConfigPath()
		if err != nil {
			return nil, err
		}
		path = resolvedPath
	}

	cfg := Default()
	content, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config %s: %w", path, err)
	}
	if err := parseConfig(path, content, cfg); err != nil {
		return nil, fmt.Errorf("parse config %s: %w", path, err)
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	cfg.Database.Path = cleanPath(cfg.Database.Path)
	cfg.Log.Dir = cleanPath(cfg.Log.Dir)
	cfg.HTTP.CertFile = cleanPath(cfg.HTTP.CertFile)
	cfg.HTTP.KeyFile = cleanPath(cfg.HTTP.KeyFile)
	cfg.Auth.SM2PrivateKeyFile = cleanPath(cfg.Auth.SM2PrivateKeyFile)
	cfg.Auth.SM2PublicKeyFile = cleanPath(cfg.Auth.SM2PublicKeyFile)
	return cfg, nil
}

// defaultConfigPath 获取默认配置文件路径。
// 检查默认JSON配置文件是否存在，不存在则返回ErrDefaultConfigMissing错误。
// 返回值：配置文件路径和可能的错误。
// Load("") is the normal startup path and requires xiaoliang02_user/config/config.json. YAML is
// still supported when an operator explicitly passes a .yaml/.yml config path.
func defaultConfigPath() (string, error) {
	if _, err := os.Stat(DefaultPath); err == nil {
		return DefaultPath, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("stat config %s: %w", DefaultPath, err)
	}
	return "", ErrDefaultConfigMissing
}

// parseConfig 根据文件扩展名解析配置文件内容。
// 支持.json和.yaml/.yml两种格式。
// 参数path：配置文件路径（用于判断扩展名）。
// 参数content：配置文件原始字节内容。
// 参数cfg：待填充的配置结构体指针。
// 返回值：解析错误。
func parseConfig(path string, content []byte, cfg *Config) error {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".json":
		return json.Unmarshal(content, cfg)
	case ".yaml", ".yml":
		return yaml.Unmarshal(content, cfg)
	default:
		return fmt.Errorf("unsupported config extension %q", filepath.Ext(path))
	}
}

// Default 创建并返回客户端的默认配置。
// 包含所有子模块的合理默认值，适用于快速启动。
// 返回值：默认配置的Config指针。
func Default() *Config {
	return &Config{
		App: AppConfig{
			Name:        "nattuser",
			Version:     "0.1.0",
			Environment: "production",
		},
		HTTP: HTTPConfig{
			Host:                   "0.0.0.0",
			Port:                   25520,
			HTTPSEnabled:           false,
			CertFile:               filepath.Join(RuntimeRoot, "ssl", "web.crt"),
			KeyFile:                filepath.Join(RuntimeRoot, "ssl", "web.key"),
			ReadTimeoutSeconds:     10,
			WriteTimeoutSeconds:    10,
			IdleTimeoutSeconds:     60,
			ShutdownTimeoutSeconds: 10,
		},
		Database: DatabaseConfig{
			Path: filepath.Join(RuntimeRoot, "data", "nattuser.db"),
		},
		Log: LogConfig{
			Dir:   filepath.Join(RuntimeRoot, "logs"),
			Level: "info",
		},
		Auth: AuthConfig{
			JWTSecret:               "change-me-nattuser-dev-secret",
			AccessTokenTTLMinutes:   120,
			RefreshTokenTTLMinutes:  10080,
			SM2PrivateKeyFile:       filepath.Join(RuntimeRoot, "data", "sm2_private.pem"),
			SM2PublicKeyFile:        filepath.Join(RuntimeRoot, "data", "sm2_public.pem"),
			LoginRateLimitPerMinute: 10,
		},
		ServerDefaults: ServerDefaultsConfig{
			ControlPort: 25511,
			DataPort:    25512,
		},
	}
}

// Validate 验证配置项的合法性。
// 检查必填字段、端口范围、文件路径等配置是否符合要求。
// 返回值：验证失败时返回描述错误的error。
func (c *Config) Validate() error {
	if c.App.Name == "" {
		return fmt.Errorf("app.name is required")
	}
	if !validPort(c.HTTP.Port) {
		return fmt.Errorf("http.port must be between 1 and 65535")
	}
	if c.HTTP.HTTPSEnabled {
		if strings.TrimSpace(c.HTTP.CertFile) == "" {
			return fmt.Errorf("http.cert_file is required when HTTPS is enabled")
		}
		if strings.TrimSpace(c.HTTP.KeyFile) == "" {
			return fmt.Errorf("http.key_file is required when HTTPS is enabled")
		}
	}
	if c.Database.Path == "" {
		return fmt.Errorf("database.path is required")
	}
	if c.Log.Dir == "" {
		return fmt.Errorf("log.dir is required")
	}
	if c.Auth.JWTSecret == "" {
		return fmt.Errorf("auth.jwt_secret is required")
	}
	if c.Auth.AccessTokenTTLMinutes <= 0 {
		return fmt.Errorf("auth.access_token_ttl_minutes must be greater than 0")
	}
	if c.Auth.RefreshTokenTTLMinutes <= 0 {
		return fmt.Errorf("auth.refresh_token_ttl_minutes must be greater than 0")
	}
	if c.Auth.SM2PrivateKeyFile == "" {
		return fmt.Errorf("auth.sm2_private_key_file is required")
	}
	if c.Auth.SM2PublicKeyFile == "" {
		return fmt.Errorf("auth.sm2_public_key_file is required")
	}
	if c.Auth.LoginRateLimitPerMinute <= 0 {
		return fmt.Errorf("auth.login_rate_limit_per_minute must be greater than 0")
	}
	if !validPort(c.ServerDefaults.ControlPort) {
		return fmt.Errorf("server_defaults.control_port must be between 1 and 65535")
	}
	if !validPort(c.ServerDefaults.DataPort) {
		return fmt.Errorf("server_defaults.data_port must be between 1 and 65535")
	}
	return nil
}

// HTTPAddr 返回HTTP服务的监听地址字符串。
// 格式为"host:port"，例如"127.0.0.1:25520"。
// 返回值：格式化的监听地址字符串。
func (c *Config) HTTPAddr() string {
	return fmt.Sprintf("%s:%d", c.HTTP.Host, c.HTTP.Port)
}

// validPort 检查端口号是否在有效范围内（1-65535）。
// 参数port：待检查的端口号。
// 返回值：端口是否有效。
func validPort(port int) bool {
	return port > 0 && port <= 65535
}

// cleanPath 规范化文件路径（去除多余的分隔符和相对路径符号）。
// 空路径直接返回空字符串，不做处理。
// 参数path：待规范化的文件路径。
// 返回值：规范化后的文件路径。
func cleanPath(path string) string {
	if path == "" {
		return path
	}
	return filepath.Clean(path)
}
