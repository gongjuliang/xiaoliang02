package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const clientYAMLConfig = `
app:
  name: nattuser-test
http:
  host: 127.0.0.1
  port: 19091
  https_enabled: true
  cert_file: ssl/../ssl/client.crt
  key_file: ssl/../ssl/client.key
database:
  path: data/../data/client.db
log:
  dir: logs/../logs
auth:
  jwt_secret: test-secret
  sm2_private_key_file: data/../keys/private.pem
  sm2_public_key_file: data/../keys/public.pem
server_defaults:
  server_host: 10.0.0.10
  control_port: 17000
  data_port: 17001
`

const clientJSONConfig = `{
  "app": {
    "name": "nattuser-test"
  },
  "http": {
    "host": "127.0.0.1",
    "port": 19091,
    "https_enabled": true,
    "cert_file": "ssl/../ssl/client.crt",
    "key_file": "ssl/../ssl/client.key"
  },
  "database": {
    "path": "data/../data/client.db"
  },
  "log": {
    "dir": "logs/../logs"
  },
  "auth": {
    "jwt_secret": "test-secret",
    "sm2_private_key_file": "data/../keys/private.pem",
    "sm2_public_key_file": "data/../keys/public.pem"
  },
  "server_defaults": {
    "server_host": "10.0.0.10",
    "control_port": 17000,
    "data_port": 17001
  }
}`

func TestLoadMergesYAMLAndCleansPaths(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(configPath, []byte(clientYAMLConfig), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := Load(configPath)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if cfg.App.Name != "nattuser-test" || cfg.HTTP.Host != "127.0.0.1" || cfg.HTTP.Port != 19091 {
		t.Fatalf("unexpected loaded config: %+v", cfg)
	}
	if cfg.Database.Path != filepath.Clean("data/../data/client.db") {
		t.Fatalf("database path=%q", cfg.Database.Path)
	}
	if cfg.Log.Dir != filepath.Clean("logs/../logs") {
		t.Fatalf("log dir=%q", cfg.Log.Dir)
	}
	if cfg.Auth.SM2PrivateKeyFile != filepath.Clean("data/../keys/private.pem") {
		t.Fatalf("private key file=%q", cfg.Auth.SM2PrivateKeyFile)
	}
	if cfg.ServerDefaults.ServerHost != "10.0.0.10" ||
		cfg.ServerDefaults.ControlPort != 17000 ||
		cfg.ServerDefaults.DataPort != 17001 {
		t.Fatalf("unexpected server defaults: %+v", cfg.ServerDefaults)
	}
}

func TestLoadMergesJSONAndCleansPaths(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.json")
	if err := os.WriteFile(configPath, []byte(clientJSONConfig), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := Load(configPath)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	assertLoadedClientConfig(t, cfg)
}

func TestDefaultUsesNewStartupPorts(t *testing.T) {
	cfg := Default()
	if cfg.App.Environment != "production" {
		t.Fatalf("app.environment=%q want production", cfg.App.Environment)
	}
	if DefaultPath != filepath.Clean("xiaoliang02_user/config/config.json") {
		t.Fatalf("DefaultPath=%q want xiaoliang02_user/config/config.json", DefaultPath)
	}
	if LegacyYAMLPath != filepath.Clean("xiaoliang02_user/config/config.yaml") {
		t.Fatalf("LegacyYAMLPath=%q want xiaoliang02_user/config/config.yaml", LegacyYAMLPath)
	}
	if cfg.HTTP.Port != 25520 {
		t.Fatalf("http.port=%d want 25520", cfg.HTTP.Port)
	}
	if cfg.HTTP.HTTPSEnabled {
		t.Fatal("http.https_enabled default must be false")
	}
	if cfg.HTTP.CertFile != filepath.Clean("xiaoliang02_user/ssl/web.crt") || cfg.HTTP.KeyFile != filepath.Clean("xiaoliang02_user/ssl/web.key") {
		t.Fatalf("default HTTPS files=%q,%q want xiaoliang02_user/ssl/web.crt,xiaoliang02_user/ssl/web.key", cfg.HTTP.CertFile, cfg.HTTP.KeyFile)
	}
	if cfg.Database.Path != filepath.Clean("xiaoliang02_user/data/nattuser.db") {
		t.Fatalf("database.path=%q want xiaoliang02_user/data/nattuser.db", cfg.Database.Path)
	}
	if cfg.Log.Dir != filepath.Clean("xiaoliang02_user/logs") {
		t.Fatalf("log.dir=%q want xiaoliang02_user/logs", cfg.Log.Dir)
	}
	if cfg.Auth.SM2PrivateKeyFile != filepath.Clean("xiaoliang02_user/data/sm2_private.pem") ||
		cfg.Auth.SM2PublicKeyFile != filepath.Clean("xiaoliang02_user/data/sm2_public.pem") {
		t.Fatalf("default SM2 files=%q,%q", cfg.Auth.SM2PrivateKeyFile, cfg.Auth.SM2PublicKeyFile)
	}
	if cfg.ServerDefaults.ServerHost != "" {
		t.Fatalf("server_defaults.server_host=%q want empty default", cfg.ServerDefaults.ServerHost)
	}
	if cfg.ServerDefaults.ControlPort != 25511 {
		t.Fatalf("server_defaults.control_port=%d want 25511", cfg.ServerDefaults.ControlPort)
	}
	if cfg.ServerDefaults.DataPort != 25512 {
		t.Fatalf("server_defaults.data_port=%d want 25512", cfg.ServerDefaults.DataPort)
	}
}

func TestLoadDefaultPrefersJSONAndRequiresJSON(t *testing.T) {
	t.Run("prefers json", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.MkdirAll(filepath.Join(dir, "xiaoliang02_user", "config"), 0o755); err != nil {
			t.Fatalf("create config dir: %v", err)
		}
		if err := os.WriteFile(filepath.Join(dir, "xiaoliang02_user", "config", "config.yaml"), []byte("app:\n  name: yaml-default\n"), 0o644); err != nil {
			t.Fatalf("write yaml config: %v", err)
		}
		if err := os.WriteFile(filepath.Join(dir, "xiaoliang02_user", "config", "config.json"), []byte(`{"app":{"name":"json-default"}}`), 0o644); err != nil {
			t.Fatalf("write json config: %v", err)
		}
		t.Chdir(dir)

		cfg, err := Load("")
		if err != nil {
			t.Fatalf("load default config: %v", err)
		}
		if cfg.App.Name != "json-default" {
			t.Fatalf("app.name=%s want json-default", cfg.App.Name)
		}
	})

	t.Run("missing json enters initialization instead of yaml fallback", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.MkdirAll(filepath.Join(dir, "xiaoliang02_user", "config"), 0o755); err != nil {
			t.Fatalf("create config dir: %v", err)
		}
		if err := os.WriteFile(filepath.Join(dir, "xiaoliang02_user", "config", "config.yaml"), []byte("app:\n  name: yaml-default\n"), 0o644); err != nil {
			t.Fatalf("write yaml config: %v", err)
		}
		t.Chdir(dir)

		_, err := Load("")
		if !errors.Is(err, ErrDefaultConfigMissing) {
			t.Fatalf("err=%v want ErrDefaultConfigMissing", err)
		}
	})

	t.Run("does not read old root config path", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.Mkdir(filepath.Join(dir, "config"), 0o755); err != nil {
			t.Fatalf("create old config dir: %v", err)
		}
		if err := os.WriteFile(filepath.Join(dir, "config", "config.json"), []byte(`{"app":{"name":"old-root-config"}}`), 0o644); err != nil {
			t.Fatalf("write old config: %v", err)
		}
		t.Chdir(dir)

		_, err := Load("")
		if !errors.Is(err, ErrDefaultConfigMissing) {
			t.Fatalf("err=%v want ErrDefaultConfigMissing", err)
		}
	})
}

func TestLoadRejectsUnsupportedExtension(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(configPath, []byte("app.name = 'nattuser'"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	if _, err := Load(configPath); err == nil || !strings.Contains(err.Error(), "unsupported config extension") {
		t.Fatalf("err=%v want unsupported extension", err)
	}
}

func TestJSONAndYAMLConfigLoadEquivalent(t *testing.T) {
	dir := t.TempDir()
	yamlPath := filepath.Join(dir, "config.yaml")
	jsonPath := filepath.Join(dir, "config.json")
	if err := os.WriteFile(yamlPath, []byte(clientYAMLConfig), 0o644); err != nil {
		t.Fatalf("write yaml config: %v", err)
	}
	if err := os.WriteFile(jsonPath, []byte(clientJSONConfig), 0o644); err != nil {
		t.Fatalf("write json config: %v", err)
	}

	yamlCfg, err := Load(yamlPath)
	if err != nil {
		t.Fatalf("load yaml config: %v", err)
	}
	jsonCfg, err := Load(jsonPath)
	if err != nil {
		t.Fatalf("load json config: %v", err)
	}
	if *yamlCfg != *jsonCfg {
		t.Fatalf("json/yaml configs differ:\nyaml=%+v\njson=%+v", yamlCfg, jsonCfg)
	}
}

func TestValidateRejectsInvalidPorts(t *testing.T) {
	cases := map[string]func(*Config){
		"http port below range": func(cfg *Config) {
			cfg.HTTP.Port = 0
		},
		"control port above range": func(cfg *Config) {
			cfg.ServerDefaults.ControlPort = 70000
		},
		"data port below range": func(cfg *Config) {
			cfg.ServerDefaults.DataPort = -1
		},
	}

	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			cfg := Default()
			mutate(cfg)
			if err := cfg.Validate(); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}

func TestValidateAllowsMCPWithoutDeprecatedHostPort(t *testing.T) {
	cfg := Default()
	cfg.MCP.Enabled = true
	cfg.MCP.Host = ""
	cfg.MCP.Port = 0
	cfg.MCP.AccessToken = "client-mcp-token"

	if err := cfg.Validate(); err != nil {
		t.Fatalf("validate with HTTP-hosted MCP: %v", err)
	}
}

func TestDefaultMCPDoesNotExposeDedicatedAddress(t *testing.T) {
	cfg := Default()
	if cfg.MCP.Host != "" || cfg.MCP.Port != 0 {
		t.Fatalf("default MCP address=%s:%d want empty deprecated address", cfg.MCP.Host, cfg.MCP.Port)
	}
}

func assertLoadedClientConfig(t *testing.T, cfg *Config) {
	t.Helper()
	if cfg.App.Name != "nattuser-test" || cfg.HTTP.Host != "127.0.0.1" || cfg.HTTP.Port != 19091 {
		t.Fatalf("unexpected loaded config: %+v", cfg)
	}
	if cfg.Database.Path != filepath.Clean("data/../data/client.db") {
		t.Fatalf("database path=%q", cfg.Database.Path)
	}
	if cfg.Log.Dir != filepath.Clean("logs/../logs") {
		t.Fatalf("log dir=%q", cfg.Log.Dir)
	}
	if cfg.Auth.SM2PrivateKeyFile != filepath.Clean("data/../keys/private.pem") {
		t.Fatalf("private key file=%q", cfg.Auth.SM2PrivateKeyFile)
	}
	if !cfg.HTTP.HTTPSEnabled {
		t.Fatal("http.https_enabled should load true")
	}
	if cfg.HTTP.CertFile != filepath.Clean("ssl/../ssl/client.crt") {
		t.Fatalf("cert file=%q", cfg.HTTP.CertFile)
	}
	if cfg.HTTP.KeyFile != filepath.Clean("ssl/../ssl/client.key") {
		t.Fatalf("key file=%q", cfg.HTTP.KeyFile)
	}
	if cfg.ServerDefaults.ServerHost != "10.0.0.10" ||
		cfg.ServerDefaults.ControlPort != 17000 ||
		cfg.ServerDefaults.DataPort != 17001 {
		t.Fatalf("unexpected server defaults: %+v", cfg.ServerDefaults)
	}
}
