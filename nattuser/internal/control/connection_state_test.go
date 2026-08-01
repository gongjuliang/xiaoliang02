package control

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"nattuser/internal/db"
	"nattuser/internal/model"
)

func TestConnectionStateCacheOverlaysLiveStateUntilExpired(t *testing.T) {
	now := time.Date(2026, time.August, 1, 20, 42, 0, 0, time.UTC)
	cache := newConnectionStateCache(nil, time.Hour, 2*time.Minute)

	cache.set(11, model.ServerConnectionStatusError, "服务端公网端口被占用", 25530, now)
	overlaid := cache.overlay(model.ServerConnection{ID: 11, Status: model.ServerConnectionStatusConnected})
	if overlaid.Status != model.ServerConnectionStatusError || overlaid.LastError != "服务端公网端口被占用" || overlaid.RemotePort != 25530 {
		t.Fatalf("overlaid connection=%+v, want live error state", overlaid)
	}

	cache.markPersisted(11, now)
	cache.removeExpired(now.Add(2*time.Minute + time.Nanosecond))
	overlaid = cache.overlay(model.ServerConnection{ID: 11, Status: model.ServerConnectionStatusConnected})
	if overlaid.Status != model.ServerConnectionStatusConnected || overlaid.LastError != "" {
		t.Fatalf("expired connection state=%+v, want database state without cache overlay", overlaid)
	}
}

func TestConnectionStateCacheFlushesDirtyState(t *testing.T) {
	ctx := context.Background()
	database, err := db.Open(ctx, filepath.Join(t.TempDir(), "test.db"), nil)
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	defer database.Close()

	connection, err := db.CreateServerConnection(ctx, database, db.CreateServerConnectionParams{
		Name: "state-cache", ServerHost: "127.0.0.1", ServerPort: 25511, DataPort: 25512,
		ClientSecret: "xiaoliang_cache_secret", LocalHost: "127.0.0.1", LocalPort: 8080,
	})
	if err != nil {
		t.Fatalf("create server connection: %v", err)
	}

	cache := newConnectionStateCache(database, time.Hour, 2*time.Minute)
	cache.set(connection.ID, model.ServerConnectionStatusError, "服务端公网端口被占用", 25531, time.Now())
	if err := cache.flush(ctx); err != nil {
		t.Fatalf("flush connection state: %v", err)
	}

	stored, err := db.GetServerConnectionByID(ctx, database, connection.ID)
	if err != nil {
		t.Fatalf("load server connection: %v", err)
	}
	if stored.Status != model.ServerConnectionStatusError || stored.LastError != "服务端公网端口被占用" || stored.RemotePort != 25531 {
		t.Fatalf("stored connection=%+v, want flushed state", stored)
	}
}
