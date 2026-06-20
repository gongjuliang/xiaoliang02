package db

import (
	"context"
	"path/filepath"
	"testing"

	"nattuser/internal/model"
)

func TestListControlServerConnectionsReturnsAutoStartAndActiveStatuses(t *testing.T) {
	ctx := context.Background()
	database, err := Open(ctx, filepath.Join(t.TempDir(), "test.db"), nil)
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	defer database.Close()

	autoStart, err := CreateServerConnection(ctx, database, CreateServerConnectionParams{
		Name:         "auto",
		ServerHost:   "127.0.0.1",
		ServerPort:   7000,
		DataPort:     7001,
		ClientSecret: "secret-auto",
		AutoStart:    true,
	})
	if err != nil {
		t.Fatalf("create auto connection: %v", err)
	}
	manual, err := CreateServerConnection(ctx, database, CreateServerConnectionParams{
		Name:         "manual",
		ServerHost:   "127.0.0.1",
		ServerPort:   7002,
		DataPort:     7003,
		ClientSecret: "secret-manual",
	})
	if err != nil {
		t.Fatalf("create manual connection: %v", err)
	}
	failed, err := CreateServerConnection(ctx, database, CreateServerConnectionParams{
		Name:         "failed",
		ServerHost:   "127.0.0.1",
		ServerPort:   7004,
		DataPort:     7005,
		ClientSecret: "secret-failed",
	})
	if err != nil {
		t.Fatalf("create failed connection: %v", err)
	}
	if _, err := SetServerConnectionStatus(ctx, database, manual.ID, model.ServerConnectionStatusConnected, ""); err != nil {
		t.Fatalf("mark manual connected: %v", err)
	}
	if _, err := SetServerConnectionStatus(ctx, database, failed.ID, model.ServerConnectionStatusError, "temporary dial error"); err != nil {
		t.Fatalf("mark failed error: %v", err)
	}

	connections, err := ListControlServerConnections(ctx, database)
	if err != nil {
		t.Fatalf("list control server connections: %v", err)
	}
	if len(connections) != 3 {
		t.Fatalf("control connection count=%d want=3", len(connections))
	}
	got := map[int64]bool{}
	for _, connection := range connections {
		got[connection.ID] = true
	}
	for _, id := range []int64{autoStart.ID, manual.ID, failed.ID} {
		if !got[id] {
			t.Fatalf("connection %d missing from control list: %+v", id, connections)
		}
	}
}

func TestControlStatusMarkersUpdateServerConnectionState(t *testing.T) {
	ctx := context.Background()
	database, err := Open(ctx, filepath.Join(t.TempDir(), "test.db"), nil)
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	defer database.Close()

	connection, err := CreateServerConnection(ctx, database, CreateServerConnectionParams{
		Name:         "public",
		ServerHost:   "127.0.0.1",
		ServerPort:   7000,
		DataPort:     7001,
		ClientSecret: "secret",
	})
	if err != nil {
		t.Fatalf("create connection: %v", err)
	}

	if err := MarkServerConnectionConnecting(ctx, database, connection.ID); err != nil {
		t.Fatalf("mark connecting: %v", err)
	}
	connection, err = GetServerConnectionByID(ctx, database, connection.ID)
	if err != nil {
		t.Fatalf("get connection: %v", err)
	}
	if connection.Status != model.ServerConnectionStatusConnecting || connection.LastError != "" {
		t.Fatalf("after connecting: %+v", connection)
	}

	if err := MarkServerConnectionConnected(ctx, database, connection.ID); err != nil {
		t.Fatalf("mark connected: %v", err)
	}
	if err := MarkServerConnectionHeartbeat(ctx, database, connection.ID); err != nil {
		t.Fatalf("mark heartbeat: %v", err)
	}
	connection, err = GetServerConnectionByID(ctx, database, connection.ID)
	if err != nil {
		t.Fatalf("get connection after heartbeat: %v", err)
	}
	if connection.Status != model.ServerConnectionStatusConnected || connection.LastError != "" {
		t.Fatalf("after heartbeat: %+v", connection)
	}

	if err := MarkServerConnectionError(ctx, database, connection.ID, "dial failed"); err != nil {
		t.Fatalf("mark error: %v", err)
	}
	connection, err = GetServerConnectionByID(ctx, database, connection.ID)
	if err != nil {
		t.Fatalf("get connection after error: %v", err)
	}
	if connection.Status != model.ServerConnectionStatusError || connection.LastError != "dial failed" {
		t.Fatalf("after error: %+v", connection)
	}

	if err := MarkServerConnectionStopped(ctx, database, connection.ID); err != nil {
		t.Fatalf("mark stopped: %v", err)
	}
	connection, err = GetServerConnectionByID(ctx, database, connection.ID)
	if err != nil {
		t.Fatalf("get connection after stopped: %v", err)
	}
	if connection.Status != model.ServerConnectionStatusStopped || connection.LastError != "" {
		t.Fatalf("after stopped: %+v", connection)
	}
}

func TestManualServerConnectionStartStopTogglesAutoStart(t *testing.T) {
	ctx := context.Background()
	database, err := Open(ctx, filepath.Join(t.TempDir(), "test.db"), nil)
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	defer database.Close()

	connection, err := CreateServerConnection(ctx, database, CreateServerConnectionParams{
		Name:         "manual-toggle",
		ServerHost:   "127.0.0.1",
		ServerPort:   7000,
		DataPort:     7001,
		ClientSecret: "secret",
		AutoStart:    true,
	})
	if err != nil {
		t.Fatalf("create connection: %v", err)
	}
	if err := MarkServerConnectionError(ctx, database, connection.ID, "temporary error"); err != nil {
		t.Fatalf("mark error: %v", err)
	}

	stopped, err := MarkServerConnectionManualStop(ctx, database, connection.ID)
	if err != nil {
		t.Fatalf("manual stop: %v", err)
	}
	if stopped.Status != model.ServerConnectionStatusStopped || stopped.AutoStart || stopped.LastError != "" {
		t.Fatalf("manual stop connection=%+v want stopped auto_start=false last_error empty", stopped)
	}

	started, err := MarkServerConnectionManualStart(ctx, database, connection.ID)
	if err != nil {
		t.Fatalf("manual start: %v", err)
	}
	if started.Status != model.ServerConnectionStatusConnected || !started.AutoStart || started.LastError != "" {
		t.Fatalf("manual start connection=%+v want connected auto_start=true last_error empty", started)
	}
}
