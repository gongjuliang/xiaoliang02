package control

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"nattserver/internal/auth"
	"nattserver/internal/db"
	"nattserver/internal/model"
)

func TestTunnelStateCacheOverlaysOfflineStateUntilRetentionExpires(t *testing.T) {
	now := time.Date(2026, time.August, 1, 20, 42, 0, 0, time.UTC)
	cache := newTunnelStateCache(nil, time.Hour, 2*time.Minute)

	cache.markOffline(7, controlOfflineMessage, now)
	overlaid := cache.overlay(model.Tunnel{ID: 7, Status: model.TunnelStatusRunning})
	if overlaid.Status != model.TunnelStatusError || overlaid.LastError != controlOfflineMessage {
		t.Fatalf("overlaid tunnel=%+v, want offline error state", overlaid)
	}

	cache.markPersisted(7, now)
	cache.removeExpired(now.Add(2*time.Minute + time.Nanosecond))
	overlaid = cache.overlay(model.Tunnel{ID: 7, Status: model.TunnelStatusRunning})
	if overlaid.Status != model.TunnelStatusRunning || overlaid.LastError != "" {
		t.Fatalf("expired tunnel state=%+v, want database state without cache overlay", overlaid)
	}
}

func TestTunnelStateCacheFlushesDirtyOfflineState(t *testing.T) {
	ctx := context.Background()
	database, err := db.Open(ctx, filepath.Join(t.TempDir(), "test.db"), nil)
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	defer database.Close()

	secret := "xiaoliang_cache_secret"
	secretHash, err := auth.HashPassword(secret)
	if err != nil {
		t.Fatalf("hash secret: %v", err)
	}
	tunnel, err := db.CreateTunnel(ctx, database, db.CreateTunnelParams{
		Name: "state-cache", Protocol: model.TunnelProtocolUDP,
		RemoteHost: "127.0.0.1", RemotePort: 24001,
	})
	if err != nil {
		t.Fatalf("create tunnel: %v", err)
	}
	if _, err := db.CreateTunnelKey(ctx, database, db.CreateTunnelKeyParams{TunnelID: tunnel.ID, SecretHash: secretHash, SecretHint: auth.SecretHint(secret), SecretPlain: secret}); err != nil {
		t.Fatalf("create key: %v", err)
	}
	if _, err := db.SetTunnelStatus(ctx, database, tunnel.ID, model.TunnelStatusRunning, ""); err != nil {
		t.Fatalf("set running: %v", err)
	}

	cache := newTunnelStateCache(database, time.Hour, 2*time.Minute)
	cache.markOffline(tunnel.ID, controlOfflineMessage, time.Now())
	if err := cache.flush(ctx); err != nil {
		t.Fatalf("flush state cache: %v", err)
	}

	stored, err := db.GetTunnelByID(ctx, database, tunnel.ID)
	if err != nil {
		t.Fatalf("load tunnel: %v", err)
	}
	if stored.Status != model.TunnelStatusError || stored.LastError != controlOfflineMessage {
		t.Fatalf("stored tunnel=%+v, want offline state", stored)
	}
}
