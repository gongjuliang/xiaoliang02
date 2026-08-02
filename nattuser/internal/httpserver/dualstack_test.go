package httpserver

import "testing"

func TestListenTCPDualKeepsIPv4WhenIPv6Fails(t *testing.T) {
	listeners, ipv6Err, err := listenTCPDual("127.0.0.1", "invalid-ipv6-host", 0)
	if err != nil {
		t.Fatalf("listen IPv4: %v", err)
	}
	defer closeListeners(listeners)
	if len(listeners) != 1 {
		t.Fatalf("listeners=%d want 1", len(listeners))
	}
	if ipv6Err == nil {
		t.Fatal("expected IPv6 listener error")
	}
}

func TestDualStackWildcardNormalizesForBothFamilies(t *testing.T) {
	if got := normalizeIPv4Host("[::]"); got != "0.0.0.0" {
		t.Fatalf("normalizeIPv4Host([::])=%q want 0.0.0.0", got)
	}
	if got := normalizeIPv6Host("[::]"); got != "::" {
		t.Fatalf("normalizeIPv6Host([::])=%q want ::", got)
	}
	if got := normalizeIPv6Host("0.0.0.0"); got != "::" {
		t.Fatalf("normalizeIPv6Host(0.0.0.0)=%q want ::", got)
	}
}
