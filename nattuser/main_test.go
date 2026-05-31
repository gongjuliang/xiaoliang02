package main

import (
	"context"
	"testing"

	"nattuser/internal/config"
)

func TestBuildServiceRunnersIncludesMCPOnlyWhenEnabled(t *testing.T) {
	runner := func(context.Context) error { return nil }

	if got := len(buildServiceRunners(false, runner, runner, runner)); got != 2 {
		t.Fatalf("disabled MCP runners=%d want=2", got)
	}
	if got := len(buildServiceRunners(true, runner, runner, runner)); got != 3 {
		t.Fatalf("enabled MCP runners=%d want=3", got)
	}
}

func TestConfigFlagDefaultUsesAutoDiscovery(t *testing.T) {
	if got := configFlagDefault(); got != "" {
		t.Fatalf("config flag default=%q want empty auto-discovery path", got)
	}
}

func TestMCPHTTPConfigUsesMCPAddressAndHTTPTimeouts(t *testing.T) {
	base := config.Default()
	base.HTTP.ReadTimeoutSeconds = 3
	base.HTTP.WriteTimeoutSeconds = 4
	base.HTTP.IdleTimeoutSeconds = 5
	base.HTTP.ShutdownTimeoutSeconds = 6
	base.MCP.Host = "127.0.0.2"
	base.MCP.Port = 19093

	httpCfg := mcpHTTPConfig(base.MCP, base.HTTP)
	if httpCfg.Host != "127.0.0.2" || httpCfg.Port != 19093 {
		t.Fatalf("unexpected MCP HTTP address: %+v", httpCfg)
	}
	if httpCfg.ReadTimeoutSeconds != 3 ||
		httpCfg.WriteTimeoutSeconds != 4 ||
		httpCfg.IdleTimeoutSeconds != 5 ||
		httpCfg.ShutdownTimeoutSeconds != 6 {
		t.Fatalf("MCP HTTP config did not inherit timeouts: %+v", httpCfg)
	}
}
