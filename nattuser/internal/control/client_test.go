package control

import (
	"context"
	"fmt"
	"net"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"nattuser/internal/config"
	"nattuser/internal/db"
	"nattuser/internal/model"
	"nattuser/internal/protocol"
)

func TestManagerAuthenticatesHeartbeatsAndReconnects(t *testing.T) {
	ctx := context.Background()
	database, err := db.Open(ctx, filepath.Join(t.TempDir(), "test.db"), nil)
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	defer database.Close()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen fake control server: %v", err)
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

	connection, err := db.CreateServerConnection(ctx, database, db.CreateServerConnectionParams{
		Name:         "test-server",
		ServerHost:   "127.0.0.1",
		ServerPort:   port,
		DataPort:     port + 1,
		ClientSecret: "natt_client_secret",
		AutoStart:    true,
	})
	if err != nil {
		t.Fatalf("create server connection: %v", err)
	}

	heartbeatSeen := make(chan struct{})
	releaseSecondConnection := make(chan struct{})
	serverDone := make(chan error, 1)
	go runFakeControlServer(t, listener, heartbeatSeen, releaseSecondConnection, serverDone)

	cfg := config.Default()
	cfg.App.Version = "test-version"
	manager := NewManagerWithOptions(cfg, database, nil, Options{
		ScanInterval:      10 * time.Millisecond,
		ReconnectInterval: 20 * time.Millisecond,
		DialTimeout:       200 * time.Millisecond,
		HeartbeatInterval: 30 * time.Millisecond,
	})
	runCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	managerDone := make(chan error, 1)
	go func() {
		managerDone <- manager.Run(runCtx)
	}()

	select {
	case <-heartbeatSeen:
	case err := <-serverDone:
		t.Fatalf("fake control server failed before heartbeat: %v", err)
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for heartbeat after reconnect")
	}

	stored, err := db.GetServerConnectionByID(ctx, database, connection.ID)
	if err != nil {
		t.Fatalf("get server connection: %v", err)
	}
	if stored.Status != model.ServerConnectionStatusConnected || stored.LastError != "" {
		t.Fatalf("stored connection after heartbeat: %+v", stored)
	}
	close(releaseSecondConnection)

	cancel()
	select {
	case err := <-managerDone:
		if err != nil {
			t.Fatalf("manager run returned error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for manager shutdown")
	}
}

func runFakeControlServer(t *testing.T, listener net.Listener, heartbeatSeen chan<- struct{}, releaseSecondConnection <-chan struct{}, done chan<- error) {
	t.Helper()
	defer close(done)
	for attempt := 1; attempt <= 2; attempt++ {
		conn, err := listener.Accept()
		if err != nil {
			done <- fmt.Errorf("accept attempt %d: %w", attempt, err)
			return
		}
		if err := handleFakeControlConn(conn, attempt, heartbeatSeen, releaseSecondConnection); err != nil {
			done <- err
			return
		}
	}
	done <- nil
}

func handleFakeControlConn(conn net.Conn, attempt int, heartbeatSeen chan<- struct{}, releaseSecondConnection <-chan struct{}) error {
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(500 * time.Millisecond))

	message, err := protocol.ReadMessage(conn)
	if err != nil {
		return fmt.Errorf("read auth attempt %d: %w", attempt, err)
	}
	if message.Type != protocol.TypeAuthRequest {
		return fmt.Errorf("auth message type=%s want %s", message.Type, protocol.TypeAuthRequest)
	}
	authReq, err := protocol.DecodePayload[protocol.AuthRequest](message)
	if err != nil {
		return err
	}
	if authReq.ClientSecret != "natt_client_secret" || authReq.ClientVersion != "test-version" || authReq.ProtocolVersion != protocol.Version {
		return fmt.Errorf("unexpected auth request: %+v", authReq)
	}
	if authReq.SystemInfo["goos"] != runtime.GOOS || authReq.SystemInfo["goarch"] != runtime.GOARCH {
		return fmt.Errorf("unexpected system info: %+v", authReq.SystemInfo)
	}

	response, err := protocol.NewMessage(protocol.TypeAuthResponse, 42, 0, "", protocol.AuthResponse{
		Success:         true,
		ClientID:        42,
		ProtocolVersion: protocol.Version,
	})
	if err != nil {
		return err
	}
	response.RequestID = message.RequestID
	if err := protocol.WriteMessage(conn, response); err != nil {
		return fmt.Errorf("write auth response attempt %d: %w", attempt, err)
	}
	if attempt == 1 {
		return nil
	}

	heartbeat, err := protocol.ReadMessage(conn)
	if err != nil {
		return fmt.Errorf("read heartbeat: %w", err)
	}
	if heartbeat.Type != protocol.TypeHeartbeat {
		return fmt.Errorf("heartbeat type=%s want %s", heartbeat.Type, protocol.TypeHeartbeat)
	}
	ack, err := protocol.NewMessage(protocol.TypeHeartbeatAck, 42, 0, "", protocol.HeartbeatAck{ServerTime: time.Now().Unix()})
	if err != nil {
		return err
	}
	ack.RequestID = heartbeat.RequestID
	if err := protocol.WriteMessage(conn, ack); err != nil {
		return fmt.Errorf("write heartbeat ack: %w", err)
	}
	close(heartbeatSeen)
	<-releaseSecondConnection
	return nil
}
