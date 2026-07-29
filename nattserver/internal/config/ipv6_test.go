package config

import "testing"

func TestDefaultEnablesIPv6ListenersAndUDPSessionDefaults(t *testing.T) {
	if DefaultIPv6HTTPHost() != "::" || DefaultIPv6ControlHost() != "::" || DefaultIPv6DataHost() != "::" || DefaultIPv6TunnelHost() != "::" {
		t.Fatal("IPv6 wildcard defaults must be enabled")
	}
	if DefaultUDPSessionIdleSeconds() != 60 || DefaultUDPMaxSessions() != 1024 {
		t.Fatal("unexpected UDP runtime defaults")
	}
}
