package control

import "testing"

func TestWildcardHostsAreNotAdvertisedAsDialTargets(t *testing.T) {
	for _, host := range []string{"", "0.0.0.0", "::", "[::]"} {
		if got := advertisedDataHost(host); got != "" {
			t.Fatalf("advertisedDataHost(%q)=%q want empty", host, got)
		}
	}
}

func TestEmptyTunnelHostDefaultsToDualStackWildcard(t *testing.T) {
	if got := hostOrDefault(""); got != "[::]" {
		t.Fatalf("hostOrDefault(empty)=%q want [::]", got)
	}
}
