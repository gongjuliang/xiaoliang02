package db

import (
	"context"
	"testing"

	"nattserver/internal/model"
)

func TestTunnelPortMayBeSharedByTCPAndUDP(t *testing.T) {
	database, err := Open(context.Background(), ":memory:", nil)
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	defer database.Close()

	for _, protocol := range []model.TunnelProtocol{model.TunnelProtocolTCP, model.TunnelProtocolUDP} {
		if _, err := CreateTunnel(context.Background(), database, CreateTunnelParams{
			Name:       string(protocol),
			Protocol:   protocol,
			RemoteHost: "0.0.0.0",
			RemotePort: 28080,
		}); err != nil {
			t.Fatalf("create %s tunnel on shared port: %v", protocol, err)
		}
	}
}
