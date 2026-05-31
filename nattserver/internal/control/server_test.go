package control

import (
	"context"
	"database/sql"
	"fmt"
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

func TestServerAuthenticatesHeartbeatAndMarksOfflineOnClose(t *testing.T) {
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

	port := freeTCPPort(t)
	server := NewServer(config.ProtocolConfig{
		ControlHost: "127.0.0.1",
		ControlPort: port,
	}, database, nil)
	runCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runDone := make(chan error, 1)
	go func() {
		runDone <- server.Run(runCtx)
	}()

	conn := dialWithRetry(t, fmt.Sprintf("127.0.0.1:%d", port))
	authReq, err := protocol.NewMessage(protocol.TypeAuthRequest, 0, 0, "", protocol.AuthRequest{
		ClientSecret:    "natt_client_secret",
		ClientName:      "client-a",
		ClientVersion:   "test-version",
		ProtocolVersion: protocol.Version,
	})
	if err != nil {
		t.Fatalf("build auth request: %v", err)
	}
	if err := protocol.WriteMessage(conn, authReq); err != nil {
		t.Fatalf("write auth request: %v", err)
	}
	authRespMsg, err := protocol.ReadMessage(conn)
	if err != nil {
		t.Fatalf("read auth response: %v", err)
	}
	authResp, err := protocol.DecodePayload[protocol.AuthResponse](authRespMsg)
	if err != nil {
		t.Fatalf("decode auth response: %v", err)
	}
	if authRespMsg.Type != protocol.TypeAuthResponse || !authResp.Success || authResp.ClientID != client.ID {
		t.Fatalf("unexpected auth response message=%+v payload=%+v", authRespMsg, authResp)
	}
	assertClientOnlineStatus(t, database, client.ID, model.OnlineStatusOnline)

	heartbeat, err := protocol.NewMessage(protocol.TypeHeartbeat, client.ID, 0, "", protocol.Heartbeat{ClientTime: time.Now().Unix()})
	if err != nil {
		t.Fatalf("build heartbeat: %v", err)
	}
	if err := protocol.WriteMessage(conn, heartbeat); err != nil {
		t.Fatalf("write heartbeat: %v", err)
	}
	ackMsg, err := protocol.ReadMessage(conn)
	if err != nil {
		t.Fatalf("read heartbeat ack: %v", err)
	}
	if ackMsg.Type != protocol.TypeHeartbeatAck || ackMsg.RequestID != heartbeat.RequestID {
		t.Fatalf("unexpected heartbeat ack: %+v", ackMsg)
	}

	_ = conn.Close()
	waitForClientStatus(t, database, client.ID, model.OnlineStatusOffline)
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

func TestServerMarksOfflineAfterThreeHeartbeatTimeouts(t *testing.T) {
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

	port := freeTCPPort(t)
	server := NewServerWithOptions(config.ProtocolConfig{
		ControlHost: "127.0.0.1",
		ControlPort: port,
	}, database, nil, ServerOptions{
		HeartbeatTimeout: 80 * time.Millisecond,
		HeartbeatMisses:  3,
	})
	runCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runDone := make(chan error, 1)
	go func() {
		runDone <- server.Run(runCtx)
	}()

	conn := authenticateFakeClient(t, port, "natt_client_secret")
	defer conn.Close()
	assertClientOnlineStatus(t, database, client.ID, model.OnlineStatusOnline)

	time.Sleep(180 * time.Millisecond)
	assertClientOnlineStatus(t, database, client.ID, model.OnlineStatusOnline)
	waitForClientStatus(t, database, client.ID, model.OnlineStatusOffline)

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

func TestServerRestartsAutoStartTunnelsWhenClientReconnects(t *testing.T) {
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
		Name:       "auto-echo",
		ClientID:   client.ID,
		Protocol:   model.TunnelProtocolTCP,
		LocalHost:  "127.0.0.1",
		LocalPort:  8080,
		RemoteHost: "127.0.0.1",
		RemotePort: freeTCPPort(t),
		AutoStart:  true,
	})
	if err != nil {
		t.Fatalf("create tunnel: %v", err)
	}

	controlPort := freeTCPPort(t)
	dataPort := freeTCPPort(t)
	server := NewServer(config.ProtocolConfig{
		ControlHost: "127.0.0.1",
		ControlPort: controlPort,
		DataHost:    "127.0.0.1",
		DataPort:    dataPort,
	}, database, nil)
	runCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runDone := make(chan error, 1)
	go func() {
		runDone <- server.Run(runCtx)
	}()

	firstConn := authenticateFakeClient(t, controlPort, "natt_client_secret")
	expectTunnelStartCommand(t, firstConn, tunnel.ID)
	waitForTunnelStatus(t, database, tunnel.ID, model.TunnelStatusRunning)
	_ = firstConn.Close()
	waitForClientStatus(t, database, client.ID, model.OnlineStatusOffline)
	waitForTunnelStatus(t, database, tunnel.ID, model.TunnelStatusError)

	secondConn := authenticateFakeClient(t, controlPort, "natt_client_secret")
	defer secondConn.Close()
	expectTunnelStartCommand(t, secondConn, tunnel.ID)
	waitForTunnelStatus(t, database, tunnel.ID, model.TunnelStatusRunning)

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

func TestServerRejectsInvalidClientSecretWithProtocolError(t *testing.T) {
	ctx := context.Background()
	database, err := db.Open(ctx, filepath.Join(t.TempDir(), "test.db"), nil)
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	defer database.Close()

	secretHash, err := auth.HashPassword("valid_secret")
	if err != nil {
		t.Fatalf("hash client secret: %v", err)
	}
	if _, err := db.CreateClient(ctx, database, db.CreateClientParams{
		Name:       "client-a",
		SecretHash: secretHash,
		SecretHint: auth.SecretHint("valid_secret"),
	}); err != nil {
		t.Fatalf("create client: %v", err)
	}

	port := freeTCPPort(t)
	server := NewServer(config.ProtocolConfig{
		ControlHost: "127.0.0.1",
		ControlPort: port,
	}, database, nil)
	runCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runDone := make(chan error, 1)
	go func() {
		runDone <- server.Run(runCtx)
	}()

	conn := dialWithRetry(t, fmt.Sprintf("127.0.0.1:%d", port))
	defer conn.Close()
	authReq, err := protocol.NewMessage(protocol.TypeAuthRequest, 0, 0, "", protocol.AuthRequest{
		ClientSecret:    "wrong_secret",
		ClientName:      "client-a",
		ClientVersion:   "test-version",
		ProtocolVersion: protocol.Version,
	})
	if err != nil {
		t.Fatalf("build auth request: %v", err)
	}
	if err := protocol.WriteMessage(conn, authReq); err != nil {
		t.Fatalf("write auth request: %v", err)
	}
	errorMsg, err := protocol.ReadMessage(conn)
	if err != nil {
		t.Fatalf("read auth error: %v", err)
	}
	if errorMsg.Type != protocol.TypeError || errorMsg.RequestID != authReq.RequestID {
		t.Fatalf("unexpected auth error message: %+v", errorMsg)
	}
	protocolErr, err := protocol.DecodePayload[protocol.ProtocolError](errorMsg)
	if err != nil {
		t.Fatalf("decode auth error: %v", err)
	}
	if protocolErr.Code != protocol.CodeUnauthorized {
		t.Fatalf("error code=%s want=%s", protocolErr.Code, protocol.CodeUnauthorized)
	}

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

func freeTCPPort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen free port: %v", err)
	}
	defer listener.Close()
	_, portText, err := net.SplitHostPort(listener.Addr().String())
	if err != nil {
		t.Fatalf("split listener addr: %v", err)
	}
	var port int
	if _, err := fmt.Sscanf(portText, "%d", &port); err != nil {
		t.Fatalf("parse listener port: %v", err)
	}
	return port
}

func dialWithRetry(t *testing.T, addr string) net.Conn {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", addr, 100*time.Millisecond)
		if err == nil {
			return conn
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("dial control server %s timed out", addr)
	return nil
}

func assertClientOnlineStatus(t *testing.T, database *sql.DB, id int64, want model.OnlineStatus) {
	t.Helper()
	var got model.OnlineStatus
	if err := database.QueryRowContext(context.Background(), "SELECT online_status FROM clients WHERE id = ?", id).Scan(&got); err != nil {
		t.Fatalf("query client status: %v", err)
	}
	if got != want {
		t.Fatalf("online_status=%s want=%s", got, want)
	}
}

func waitForClientStatus(t *testing.T, database *sql.DB, id int64, want model.OnlineStatus) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		var got model.OnlineStatus
		if err := database.QueryRowContext(context.Background(), "SELECT online_status FROM clients WHERE id = ?", id).Scan(&got); err != nil {
			t.Fatalf("query client status: %v", err)
		}
		if got == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	assertClientOnlineStatus(t, database, id, want)
}

func waitForTunnelStatus(t *testing.T, database *sql.DB, id int64, want model.TunnelStatus) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		var got model.TunnelStatus
		if err := database.QueryRowContext(context.Background(), "SELECT status FROM tunnels WHERE id = ?", id).Scan(&got); err != nil {
			t.Fatalf("query tunnel status: %v", err)
		}
		if got == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	var got model.TunnelStatus
	if err := database.QueryRowContext(context.Background(), "SELECT status FROM tunnels WHERE id = ?", id).Scan(&got); err != nil {
		t.Fatalf("query tunnel status: %v", err)
	}
	t.Fatalf("tunnel status=%s want=%s", got, want)
}

func expectTunnelStartCommand(t *testing.T, conn net.Conn, tunnelID int64) {
	t.Helper()
	_ = conn.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
	defer conn.SetReadDeadline(time.Time{})
	message, err := protocol.ReadMessage(conn)
	if err != nil {
		t.Fatalf("read tunnel_start command: %v", err)
	}
	if message.Type != protocol.TypeTunnelStart || message.TunnelID != tunnelID {
		t.Fatalf("unexpected tunnel_start command: %+v", message)
	}
}
