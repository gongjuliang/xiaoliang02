package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const serverYAMLConfig = `
app:
  name: nattserver-test
http:
  host: 127.0.0.1
  port: 19090
  https_enabled: true
  cert_file: ssl/../ssl/server.crt
  key_file: ssl/../ssl/server.key
database:
  path: data/../data/server.db
log:
  dir: logs/../logs
auth:
  jwt_secret: test-secret
  sm2_private_key_file: data/../keys/private.pem
  sm2_public_key_file: data/../keys/public.pem
protocol:
  control_port: 17000
  data_port: 17001
tunnel:
  remote_port_min: 20000
  remote_port_max: 20010
`

const serverJSONConfig = `{
  "app": {
    "name": "nattserver-test"
  },
  "http": {
    "host": "127.0.0.1",
    "port": 19090,
    "https_enabled": true,
    "cert_file": "ssl/../ssl/server.crt",
    "key_file": "ssl/../ssl/server.key"
  },
  "database": {
    "path": "data/../data/server.db"
  },
  "log": {
    "dir": "logs/../logs"
  },
  "auth": {
    "jwt_secret": "test-secret",
    "sm2_private_key_file": "data/../keys/private.pem",
    "sm2_public_key_file": "data/../keys/public.pem"
  },
  "protocol": {
    "control_port": 17000,
    "data_port": 17001
  },
  "tunnel": {
    "remote_port_min": 20000,
    "remote_port_max": 20010
  }
}`

func TestLoadMergesYAMLAndCleansPaths(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(configPath, []byte(serverYAMLConfig), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := Load(configPath)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if cfg.App.Name != "nattserver-test" || cfg.HTTP.Host != "127.0.0.1" || cfg.HTTP.Port != 19090 {
		t.Fatalf("unexpected loaded config: %+v", cfg)
	}
	if cfg.Database.Path != filepath.Clean("data/../data/server.db") {
		t.Fatalf("database path=%q", cfg.Database.Path)
	}
	if cfg.Log.Dir != filepath.Clean("logs/../logs") {
		t.Fatalf("log dir=%q", cfg.Log.Dir)
	}
	if cfg.Auth.SM2PrivateKeyFile != filepath.Clean("data/../keys/private.pem") {
		t.Fatalf("private key file=%q", cfg.Auth.SM2PrivateKeyFile)
	}
	if cfg.Protocol.ControlPort != 17000 || cfg.Protocol.DataPort != 17001 {
		t.Fatalf("unexpected protocol config: %+v", cfg.Protocol)
	}
	if cfg.Tunnel.RemotePortMin != 20000 || cfg.Tunnel.RemotePortMax != 20010 {
		t.Fatalf("unexpected tunnel range: %+v", cfg.Tunnel)
	}
}

func TestLoadMergesJSONAndCleansPaths(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.json")
	if err := os.WriteFile(configPath, []byte(serverJSONConfig), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := Load(configPath)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	assertLoadedServerConfig(t, cfg)
}

func TestDefaultUsesNewStartupPortsAndTunnelRange(t *testing.T) {
	cfg := Default()
	if cfg.HTTP.Port != 25510 {
		t.Fatalf("http.port=%d want 25510", cfg.HTTP.Port)
	}
	if cfg.HTTP.HTTPSEnabled {
		t.Fatal("http.https_enabled default must be false")
	}
	if cfg.HTTP.CertFile != filepath.Clean("ssl/web.crt") || cfg.HTTP.KeyFile != filepath.Clean("ssl/web.key") {
		t.Fatalf("default HTTPS files=%q,%q want ssl/web.crt,ssl/web.key", cfg.HTTP.CertFile, cfg.HTTP.KeyFile)
	}
	if cfg.Protocol.ControlPort != 25511 {
		t.Fatalf("protocol.control_port=%d want 25511", cfg.Protocol.ControlPort)
	}
	if cfg.Protocol.DataPort != 25512 {
		t.Fatalf("protocol.data_port=%d want 25512", cfg.Protocol.DataPort)
	}
	if cfg.Tunnel.RemotePortMin != 0 || cfg.Tunnel.RemotePortMax != 65535 {
		t.Fatalf("tunnel range=%d-%d want 0-65535", cfg.Tunnel.RemotePortMin, cfg.Tunnel.RemotePortMax)
	}
}

func TestLoadDefaultPrefersJSONAndRequiresJSON(t *testing.T) {
	t.Run("prefers json", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.Mkdir(filepath.Join(dir, "config"), 0o755); err != nil {
			t.Fatalf("create config dir: %v", err)
		}
		if err := os.WriteFile(filepath.Join(dir, "config", "config.yaml"), []byte("app:\n  name: yaml-default\n"), 0o644); err != nil {
			t.Fatalf("write yaml config: %v", err)
		}
		if err := os.WriteFile(filepath.Join(dir, "config", "config.json"), []byte(`{"app":{"name":"json-default"}}`), 0o644); err != nil {
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
		if err := os.Mkdir(filepath.Join(dir, "config"), 0o755); err != nil {
			t.Fatalf("create config dir: %v", err)
		}
		if err := os.WriteFile(filepath.Join(dir, "config", "config.yaml"), []byte("app:\n  name: yaml-default\n"), 0o644); err != nil {
			t.Fatalf("write yaml config: %v", err)
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
	if err := os.WriteFile(configPath, []byte("app.name = 'nattserver'"), 0o644); err != nil {
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
	if err := os.WriteFile(yamlPath, []byte(serverYAMLConfig), 0o644); err != nil {
		t.Fatalf("write yaml config: %v", err)
	}
	if err := os.WriteFile(jsonPath, []byte(serverJSONConfig), 0o644); err != nil {
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

func TestValidateRejectsInvalidPortsAndTunnelRange(t *testing.T) {
	cases := map[string]func(*Config){
		"http port below range": func(cfg *Config) {
			cfg.HTTP.Port = 0
		},
		"control port above range": func(cfg *Config) {
			cfg.Protocol.ControlPort = 70000
		},
		"data port below range": func(cfg *Config) {
			cfg.Protocol.DataPort = -1
		},
		"remote port min below range": func(cfg *Config) {
			cfg.Tunnel.RemotePortMin = -1
		},
		"remote port max above range": func(cfg *Config) {
			cfg.Tunnel.RemotePortMax = 70000
		},
		"remote port min above max": func(cfg *Config) {
			cfg.Tunnel.RemotePortMin = 30000
			cfg.Tunnel.RemotePortMax = 20000
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
	cfg.MCP.AccessToken = "server-mcp-token"

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

func assertLoadedServerConfig(t *testing.T, cfg *Config) {
	t.Helper()
	if cfg.App.Name != "nattserver-test" || cfg.HTTP.Host != "127.0.0.1" || cfg.HTTP.Port != 19090 {
		t.Fatalf("unexpected loaded config: %+v", cfg)
	}
	if cfg.Database.Path != filepath.Clean("data/../data/server.db") {
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
	if cfg.HTTP.CertFile != filepath.Clean("ssl/../ssl/server.crt") {
		t.Fatalf("cert file=%q", cfg.HTTP.CertFile)
	}
	if cfg.HTTP.KeyFile != filepath.Clean("ssl/../ssl/server.key") {
		t.Fatalf("key file=%q", cfg.HTTP.KeyFile)
	}
	if cfg.Protocol.ControlPort != 17000 || cfg.Protocol.DataPort != 17001 {
		t.Fatalf("unexpected protocol config: %+v", cfg.Protocol)
	}
	if cfg.Tunnel.RemotePortMin != 20000 || cfg.Tunnel.RemotePortMax != 20010 {
		t.Fatalf("unexpected tunnel range: %+v", cfg.Tunnel)
	}
}
