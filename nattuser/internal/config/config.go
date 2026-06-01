package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/goccy/go-yaml"
)

const (
	DefaultPath    = "config/config.json"
	LegacyYAMLPath = "config/config.yaml"
)

type Config struct {
	App            AppConfig            `yaml:"app" json:"app"`
	HTTP           HTTPConfig           `yaml:"http" json:"http"`
	Database       DatabaseConfig       `yaml:"database" json:"database"`
	Log            LogConfig            `yaml:"log" json:"log"`
	Auth           AuthConfig           `yaml:"auth" json:"auth"`
	ServerDefaults ServerDefaultsConfig `yaml:"server_defaults" json:"server_defaults"`
	MCP            MCPConfig            `yaml:"-" json:"-"`
}

type AppConfig struct {
	Name        string `yaml:"name" json:"name"`
	Version     string `yaml:"version" json:"version"`
	Environment string `yaml:"environment" json:"environment"`
}

type HTTPConfig struct {
	Host                   string `yaml:"host" json:"host"`
	Port                   int    `yaml:"port" json:"port"`
	ReadTimeoutSeconds     int    `yaml:"read_timeout_seconds" json:"read_timeout_seconds"`
	WriteTimeoutSeconds    int    `yaml:"write_timeout_seconds" json:"write_timeout_seconds"`
	IdleTimeoutSeconds     int    `yaml:"idle_timeout_seconds" json:"idle_timeout_seconds"`
	ShutdownTimeoutSeconds int    `yaml:"shutdown_timeout_seconds" json:"shutdown_timeout_seconds"`
}

type DatabaseConfig struct {
	Path string `yaml:"path" json:"path"`
}

type LogConfig struct {
	Dir   string `yaml:"dir" json:"dir"`
	Level string `yaml:"level" json:"level"`
}

type AuthConfig struct {
	JWTSecret               string `yaml:"jwt_secret" json:"jwt_secret"`
	AccessTokenTTLMinutes   int    `yaml:"access_token_ttl_minutes" json:"access_token_ttl_minutes"`
	RefreshTokenTTLMinutes  int    `yaml:"refresh_token_ttl_minutes" json:"refresh_token_ttl_minutes"`
	SM2PrivateKeyFile       string `yaml:"sm2_private_key_file" json:"sm2_private_key_file"`
	SM2PublicKeyFile        string `yaml:"sm2_public_key_file" json:"sm2_public_key_file"`
	LoginRateLimitPerMinute int    `yaml:"login_rate_limit_per_minute" json:"login_rate_limit_per_minute"`
	AllowPlaintextPassword  bool   `yaml:"allow_plaintext_password" json:"allow_plaintext_password"`
}

type ServerDefaultsConfig struct {
	ServerHost  string `yaml:"server_host" json:"server_host"`
	ControlPort int    `yaml:"control_port" json:"control_port"`
	DataPort    int    `yaml:"data_port" json:"data_port"`
	UseTLS      bool   `yaml:"use_tls" json:"use_tls"`
}

type MCPConfig struct {
	Enabled     bool   `yaml:"-" json:"-"`
	Host        string `yaml:"-" json:"-"`
	Port        int    `yaml:"-" json:"-"`
	AccessToken string `yaml:"-" json:"-"`
}

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
	cfg.Auth.SM2PrivateKeyFile = cleanPath(cfg.Auth.SM2PrivateKeyFile)
	cfg.Auth.SM2PublicKeyFile = cleanPath(cfg.Auth.SM2PublicKeyFile)
	return cfg, nil
}

// Load("") intentionally prefers the new JSON config, but falls back to the
// legacy YAML file so existing deployments keep starting during migration.
func defaultConfigPath() (string, error) {
	if _, err := os.Stat(DefaultPath); err == nil {
		return DefaultPath, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("stat config %s: %w", DefaultPath, err)
	}
	return LegacyYAMLPath, nil
}

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

func Default() *Config {
	return &Config{
		App: AppConfig{
			Name:        "nattuser",
			Version:     "0.1.0",
			Environment: "development",
		},
		HTTP: HTTPConfig{
			Host:                   "127.0.0.1",
			Port:                   18080,
			ReadTimeoutSeconds:     10,
			WriteTimeoutSeconds:    10,
			IdleTimeoutSeconds:     60,
			ShutdownTimeoutSeconds: 10,
		},
		Database: DatabaseConfig{
			Path: "data/nattuser.db",
		},
		Log: LogConfig{
			Dir:   "logs",
			Level: "info",
		},
		Auth: AuthConfig{
			JWTSecret:               "change-me-nattuser-dev-secret",
			AccessTokenTTLMinutes:   120,
			RefreshTokenTTLMinutes:  10080,
			SM2PrivateKeyFile:       "data/sm2_private.pem",
			SM2PublicKeyFile:        "data/sm2_public.pem",
			LoginRateLimitPerMinute: 10,
		},
		ServerDefaults: ServerDefaultsConfig{
			ServerHost:  "127.0.0.1",
			ControlPort: 7000,
			DataPort:    7001,
			UseTLS:      false,
		},
	}
}

func (c *Config) Validate() error {
	if c.App.Name == "" {
		return fmt.Errorf("app.name is required")
	}
	if !validPort(c.HTTP.Port) {
		return fmt.Errorf("http.port must be between 1 and 65535")
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
	if c.ServerDefaults.ServerHost == "" {
		return fmt.Errorf("server_defaults.server_host is required")
	}
	if !validPort(c.ServerDefaults.ControlPort) {
		return fmt.Errorf("server_defaults.control_port must be between 1 and 65535")
	}
	if !validPort(c.ServerDefaults.DataPort) {
		return fmt.Errorf("server_defaults.data_port must be between 1 and 65535")
	}
	return nil
}

func (c *Config) HTTPAddr() string {
	return fmt.Sprintf("%s:%d", c.HTTP.Host, c.HTTP.Port)
}

func validPort(port int) bool {
	return port > 0 && port <= 65535
}

func cleanPath(path string) string {
	if path == "" {
		return path
	}
	return filepath.Clean(path)
}
