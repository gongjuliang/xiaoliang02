package config

import "testing"

func TestDefaultEnablesIPv6HTTPListener(t *testing.T) {
	if DefaultIPv6HTTPHost() != "::" {
		t.Fatalf("http ipv6 host=%q want ::", DefaultIPv6HTTPHost())
	}
}
