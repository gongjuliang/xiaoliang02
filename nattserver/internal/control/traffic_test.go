package control

import (
	"bufio"
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"nattserver/internal/auth"
	"nattserver/internal/config"
	"nattserver/internal/db"
	"nattserver/internal/model"
	"nattserver/internal/protocol"
)

func TestTrafficStatsFlushPeriodicallyWhileConnectionIsActive(t *testing.T) {
	ctx := context.Background()
	database, err := db.Open(ctx, filepath.Join(t.TempDir(), "test.db"), nil)
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	defer database.Close()

	secret := "natt_client_secret"
	secretHash, err := auth.HashPassword(secret)
	if err != nil {
		t.Fatalf("hash client secret: %v", err)
	}
	client, err := db.CreateClient(ctx, database, db.CreateClientParams{
		Name:       "client-a",
		SecretHash: secretHash,
		SecretHint: auth.SecretHint(secret),
	})
	if err != nil {
		t.Fatalf("create client: %v", err)
	}
	localAddr, stopEcho := startEchoServer(t)
	defer stopEcho()
	localHost, localPort := splitAddr(t, localAddr)
	remotePort := freeTCPPort(t)
	tunnel, err := db.CreateTunnel(ctx, database, db.CreateTunnelParams{
		Name:       "echo-periodic-stats",
		ClientID:   client.ID,
		Protocol:   model.TunnelProtocolTCP,
		LocalHost:  localHost,
		LocalPort:  localPort,
		RemoteHost: "127.0.0.1",
		RemotePort: remotePort,
	})
	if err != nil {
		t.Fatalf("create tunnel: %v", err)
	}

	controlPort := freeTCPPort(t)
	dataPort := freeTCPPort(t)
	server := NewServerWithOptions(config.ProtocolConfig{
		ControlHost: "127.0.0.1",
		ControlPort: controlPort,
		DataHost:    "127.0.0.1",
		DataPort:    dataPort,
	}, database, nil, ServerOptions{
		TrafficFlushInterval: 20 * time.Millisecond,
	})
	runCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runDone := make(chan error, 1)
	go func() {
		runDone <- server.Run(runCtx)
	}()

	controlConn := authenticateFakeClient(t, controlPort, secret)
	defer controlConn.Close()
	if _, err := server.StartTunnel(ctx, tunnel.ID); err != nil {
		t.Fatalf("start tunnel: %v", err)
	}
	if _, err := protocol.ReadMessage(controlConn); err != nil {
		t.Fatalf("read tunnel_start: %v", err)
	}
	publicConn := dialWithRetry(t, fmt.Sprintf("127.0.0.1:%d", remotePort))
	defer publicConn.Close()
	dataOpen, err := protocol.ReadMessage(controlConn)
	if err != nil {
		t.Fatalf("read data_open: %v", err)
	}
	clientDone := make(chan error, 1)
	go runProtocolDataClient(t, dataOpen, dataPort, secret, localAddr, clientDone)

	if _, err := publicConn.Write([]byte("periodic stats\n")); err != nil {
		t.Fatalf("write public connection: %v", err)
	}
	_ = publicConn.SetReadDeadline(time.Now().Add(2 * time.Second))
	got, err := bufio.NewReader(publicConn).ReadString('\n')
	if err != nil {
		t.Fatalf("read public echo: %v", err)
	}
	if got != "periodic stats\n" {
		t.Fatalf("echo response=%q want periodic stats", got)
	}
	waitForTrafficStat(t, database, tunnel.ID, 1, 1)

	_ = publicConn.Close()
	select {
	case err := <-clientDone:
		if err != nil {
			t.Fatalf("protocol data client failed: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for protocol data client")
	}
	if _, err := server.StopTunnel(ctx, tunnel.ID); err != nil {
		t.Fatalf("stop tunnel: %v", err)
	}
	_ = controlConn.Close()
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

func TestTrafficRecorderFlushesDeltasPeriodically(t *testing.T) {
	ctx := context.Background()
	database, err := db.Open(ctx, filepath.Join(t.TempDir(), "test.db"), nil)
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	defer database.Close()

	secretHash, err := auth.HashPassword("natt_client_secret")
	if err != nil {
		t.Fatalf("hash client secret: %v", err)
	}
	client, err := db.CreateClient(ctx, database, db.CreateClientParams{
		Name:       "client-a",
		SecretHash: secretHash,
		SecretHint: auth.SecretHint("natt_client_secret"),
	})
	if err != nil {
		t.Fatalf("create client: %v", err)
	}
	tunnel, err := db.CreateTunnel(ctx, database, db.CreateTunnelParams{
		Name:       "stats",
		ClientID:   client.ID,
		Protocol:   model.TunnelProtocolTCP,
		LocalHost:  "127.0.0.1",
		LocalPort:  8080,
		RemoteHost: "127.0.0.1",
		RemotePort: freeTCPPort(t),
	})
	if err != nil {
		t.Fatalf("create tunnel: %v", err)
	}

	recorder := newTrafficRecorder(database, nil, 10*time.Millisecond)
	runCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go recorder.run(runCtx)

	recorder.recordConnectionOpen(tunnel.ID)
	recorder.recordTrafficDelta(tunnel.ID, 11, 13)
	waitForTrafficStatValues(t, database, tunnel.ID, 1, 1, 11, 13)

	recorder.recordConnectionClose(tunnel.ID)
	waitForTrafficStatValues(t, database, tunnel.ID, 1, 0, 11, 13)
}

func waitForTrafficStatValues(t *testing.T, database interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, tunnelID int64, connectionCount int64, activeConnections int64, bytesIn int64, bytesOut int64) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		var gotConnectionCount int64
		var gotActiveConnections int64
		var gotBytesIn int64
		var gotBytesOut int64
		if err := database.QueryRowContext(context.Background(), `
SELECT connection_count, active_connections, bytes_in, bytes_out
FROM traffic_stats
WHERE tunnel_id = ?;`, tunnelID).Scan(&gotConnectionCount, &gotActiveConnections, &gotBytesIn, &gotBytesOut); err != nil {
			t.Fatalf("query traffic stats: %v", err)
		}
		if gotConnectionCount == connectionCount && gotActiveConnections == activeConnections && gotBytesIn == bytesIn && gotBytesOut == bytesOut {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("traffic stats did not reach connection_count=%d active_connections=%d bytes_in=%d bytes_out=%d", connectionCount, activeConnections, bytesIn, bytesOut)
}
