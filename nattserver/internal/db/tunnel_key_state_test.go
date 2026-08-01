package db

import (
	"context"
	"path/filepath"
	"testing"

	"nattserver/internal/auth"
	"nattserver/internal/model"
)

func TestMarkTunnelControlOfflineUpdatesKeyAndRunningTunnel(t *testing.T) {
	ctx := context.Background()
	database, err := Open(ctx, filepath.Join(t.TempDir(), "test.db"), nil)
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	defer database.Close()

	secret := "xiaoliang_test_secret"
	secretHash, err := auth.HashPassword(secret)
	if err != nil {
		t.Fatalf("hash secret: %v", err)
	}
	tunnel, err := CreateTunnel(ctx, database, CreateTunnelParams{
		Name: "tunnel", Protocol: model.TunnelProtocolUDP,
		LocalHost: "127.0.0.1", LocalPort: 1, RemoteHost: "127.0.0.1", RemotePort: 24000,
	})
	if err != nil {
		t.Fatalf("create tunnel: %v", err)
	}
	if _, err := CreateTunnelKey(ctx, database, CreateTunnelKeyParams{TunnelID: tunnel.ID, SecretHash: secretHash, SecretHint: auth.SecretHint(secret), SecretPlain: secret}); err != nil {
		t.Fatalf("create tunnel key: %v", err)
	}
	if _, err := SetTunnelStatus(ctx, database, tunnel.ID, model.TunnelStatusRunning, ""); err != nil {
		t.Fatalf("set running: %v", err)
	}

	if err := MarkTunnelControlOffline(ctx, database, tunnel.ID, "隧道控制连接已离线"); err != nil {
		t.Fatalf("mark control offline: %v", err)
	}

	key, err := GetTunnelKeyByTunnelID(ctx, database, tunnel.ID)
	if err != nil {
		t.Fatalf("load tunnel key: %v", err)
	}
	if key.OnlineStatus != model.OnlineStatusOffline {
		t.Fatalf("key online status=%q, want offline", key.OnlineStatus)
	}
	stored, err := GetTunnelByID(ctx, database, tunnel.ID)
	if err != nil {
		t.Fatalf("load tunnel: %v", err)
	}
	if stored.Status != model.TunnelStatusError || stored.LastError != "隧道控制连接已离线" {
		t.Fatalf("stored tunnel=%+v, want offline error", stored)
	}
}
