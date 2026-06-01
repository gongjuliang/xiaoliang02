package main

import (
	"context"
	"testing"
)

func TestBuildServiceRunnersUsesHTTPAndControlOnly(t *testing.T) {
	runner := func(context.Context) error { return nil }

	if got := len(buildServiceRunners(runner, runner)); got != 2 {
		t.Fatalf("runners=%d want=2", got)
	}
}

func TestConfigFlagDefaultUsesAutoDiscovery(t *testing.T) {
	if got := configFlagDefault(); got != "" {
		t.Fatalf("config flag default=%q want empty auto-discovery path", got)
	}
}
