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
