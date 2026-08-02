package control

import (
	"context"
	"database/sql"
	"net"
	"path/filepath"
	"testing"
	"time"

	"nattserver/internal/auth"
	"nattserver/internal/config"
	"nattserver/internal/db"
	"nattserver/internal/model"
	"nattserver/internal/protocol"
)

func TestHeartbeatRestartsMissingUDPRelayOnExistingRuntime(t *testing.T) {
	ctx := context.Background()
	database := openUDPRecoveryDatabase(t, ctx)
	tunnel := createUDPRecoveryTunnel(t, ctx, database, model.TunnelStatusError, "UDP中继连接已离线")
	server := NewServer(config.ProtocolConfig{}, database, nil)

	serverConn, peerConn := net.Pipe()
	defer serverConn.Close()
	defer peerConn.Close()
	activeClient := &clientConn{tunnelID: tunnel.ID, conn: serverConn}
	server.active[tunnel.ID] = activeClient
	server.udpTunnels[tunnel.ID] = newUDPRecoveryRuntime(tunnel)

	heartbeat, err := protocol.NewMessage(protocol.TypeHeartbeat, 0, tunnel.ID, "", protocol.Heartbeat{ClientTime: time.Now().Unix()})
	if err != nil {
		t.Fatalf("build heartbeat: %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- server.handleMessage(ctx, activeClient, heartbeat) }()

	expectTunnelStartAndHeartbeatAck(t, peerConn, tunnel.ID, heartbeat.RequestID)
	if err := <-done; err != nil {
		t.Fatalf("handle heartbeat: %v", err)
	}
	waitForTunnelStatus(t, database, tunnel.ID, model.TunnelStatusWaiting)
}

func TestHeartbeatRecreatesUDPRuntimeAfterServerRestart(t *testing.T) {
	ctx := context.Background()
	database := openUDPRecoveryDatabase(t, ctx)
	tunnel := createUDPRecoveryTunnel(t, ctx, database, model.TunnelStatusRunning, "")
	server := NewServer(config.ProtocolConfig{}, database, nil)

	serverConn, peerConn := net.Pipe()
	defer serverConn.Close()
	defer peerConn.Close()
	activeClient := &clientConn{tunnelID: tunnel.ID, conn: serverConn}
	server.active[tunnel.ID] = activeClient

	heartbeat, err := protocol.NewMessage(protocol.TypeHeartbeat, 0, tunnel.ID, "", protocol.Heartbeat{ClientTime: time.Now().Unix()})
	if err != nil {
		t.Fatalf("build heartbeat: %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- server.handleMessage(ctx, activeClient, heartbeat) }()

	expectTunnelStartAndHeartbeatAck(t, peerConn, tunnel.ID, heartbeat.RequestID)
	if err := <-done; err != nil {
		t.Fatalf("handle heartbeat: %v", err)
	}
	active := server.removeActiveUDPTunnel(tunnel.ID)
	if active == nil {
		t.Fatal("UDP runtime was not recreated")
	}
	active.stop()
	waitForTunnelStatus(t, database, tunnel.ID, model.TunnelStatusWaiting)
}

func TestAuthenticationRestartsUDPRelayAfterClientReconnect(t *testing.T) {
	ctx := context.Background()
	database := openUDPRecoveryDatabase(t, ctx)
	tunnel := createUDPRecoveryTunnel(t, ctx, database, model.TunnelStatusError, udpRelayOfflineMessage)
	controlPort := freeTCPPort(t)
	dataPort := freeTCPPort(t)
	server := NewServer(config.ProtocolConfig{
		ControlHost: "127.0.0.1", ControlPort: controlPort,
		DataHost: "127.0.0.1", DataPort: dataPort,
	}, database, nil)

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	runDone := make(chan error, 1)
	go func() { runDone <- server.Run(runCtx) }()

	conn := authenticateFakeClient(t, controlPort, "xiaoliang_udp_recovery_secret")
	defer conn.Close()
	expectTunnelStartCommand(t, conn, tunnel.ID)
	waitForTunnelStatus(t, database, tunnel.ID, model.TunnelStatusWaiting)

	cancel()
	select {
	case err := <-runDone:
		if err != nil {
			t.Fatalf("server run returned error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for server shutdown")
	}
}

func TestHeartbeatDoesNotRecoverStoppedOrUnrelatedUDPError(t *testing.T) {
	tests := []struct {
		name      string
		status    model.TunnelStatus
		lastError string
	}{
		{name: "stopped", status: model.TunnelStatusStopped},
		{name: "unrelated error", status: model.TunnelStatusError, lastError: "公网UDP端口被占用"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			database := openUDPRecoveryDatabase(t, ctx)
			tunnel := createUDPRecoveryTunnel(t, ctx, database, tt.status, tt.lastError)
			server := NewServer(config.ProtocolConfig{}, database, nil)

			serverConn, peer := net.Pipe()
			defer serverConn.Close()
			defer peer.Close()
			activeClient := &clientConn{tunnelID: tunnel.ID, conn: serverConn}
			server.active[tunnel.ID] = activeClient

			heartbeat, err := protocol.NewMessage(protocol.TypeHeartbeat, 0, tunnel.ID, "", protocol.Heartbeat{ClientTime: time.Now().Unix()})
			if err != nil {
				t.Fatalf("build heartbeat: %v", err)
			}
			done := make(chan error, 1)
			go func() { done <- server.handleMessage(ctx, activeClient, heartbeat) }()

			message, err := protocol.ReadMessage(peer)
			if err != nil {
				t.Fatalf("read heartbeat ack: %v", err)
			}
			if message.Type != protocol.TypeHeartbeatAck {
				t.Fatalf("unexpected recovery command: %+v", message)
			}
			if err := <-done; err != nil {
				t.Fatalf("handle heartbeat: %v", err)
			}
			if active := server.getActiveUDPTunnel(tunnel.ID); active != nil {
				t.Fatal("UDP runtime should not have been created")
			}
		})
	}
}

func openUDPRecoveryDatabase(t *testing.T, ctx context.Context) *sql.DB {
	t.Helper()
	database, err := db.Open(ctx, filepath.Join(t.TempDir(), "udp-recovery.db"), nil)
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })
	return database
}

func createUDPRecoveryTunnel(t *testing.T, ctx context.Context, database *sql.DB, status model.TunnelStatus, lastError string) model.Tunnel {
	t.Helper()
	const secret = "xiaoliang_udp_recovery_secret"
	secretHash, err := auth.HashPassword(secret)
	if err != nil {
		t.Fatalf("hash secret: %v", err)
	}
	tunnel, err := db.CreateTunnel(ctx, database, db.CreateTunnelParams{
		Name:       "udp-recovery",
		Protocol:   model.TunnelProtocolUDP,
		RemoteHost: "127.0.0.1",
		RemotePort: freeUDPPort(t),
		AutoStart:  true,
	})
	if err != nil {
		t.Fatalf("create UDP tunnel: %v", err)
	}
	if _, err := db.CreateTunnelKey(ctx, database, db.CreateTunnelKeyParams{
		TunnelID: tunnel.ID, SecretHash: secretHash, SecretHint: auth.SecretHint(secret), SecretPlain: secret,
	}); err != nil {
		t.Fatalf("create tunnel key: %v", err)
	}
	updated, err := db.SetTunnelStatus(ctx, database, tunnel.ID, status, lastError)
	if err != nil {
		t.Fatalf("set tunnel status: %v", err)
	}
	return updated
}

func newUDPRecoveryRuntime(tunnel model.Tunnel) *activeUDPTunnel {
	return &activeUDPTunnel{
		tunnel:      tunnel,
		stopCh:      make(chan struct{}),
		sessions:    make(map[string]*udpServerSession),
		sessionIDs:  make(map[string]*udpServerSession),
		maxSessions: 1024,
	}
}
