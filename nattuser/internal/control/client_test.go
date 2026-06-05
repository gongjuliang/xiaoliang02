package control

import (
	"context"
	"database/sql"
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
		RemotePort:      18080,
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

func TestManagerStoresRemotePortFromAuthResponse(t *testing.T) {
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
		Name:         "remote-port-server",
		ServerHost:   "127.0.0.1",
		ServerPort:   port,
		DataPort:     port + 1,
		ClientSecret: "natt_client_secret",
		AutoStart:    true,
	})
	if err != nil {
		t.Fatalf("create server connection: %v", err)
	}

	done := make(chan error, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			done <- fmt.Errorf("accept: %w", err)
			return
		}
		defer conn.Close()
		message, err := protocol.ReadMessage(conn)
		if err != nil {
			done <- fmt.Errorf("read auth: %w", err)
			return
		}
		response, err := protocol.NewMessage(protocol.TypeAuthResponse, 42, 99, "", protocol.AuthResponse{
			Success:         true,
			ClientID:        42,
			TunnelID:        99,
			RemotePort:      19090,
			ProtocolVersion: protocol.Version,
		})
		if err != nil {
			done <- err
			return
		}
		response.RequestID = message.RequestID
		if err := protocol.WriteMessage(conn, response); err != nil {
			done <- fmt.Errorf("write response: %w", err)
			return
		}
		done <- nil
	}()

	cfg := config.Default()
	manager := NewManagerWithOptions(cfg, database, nil, Options{
		DialTimeout:       200 * time.Millisecond,
		HeartbeatInterval: time.Hour,
	})
	runCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	err = manager.connectAndServe(runCtx, connection)
	if err == nil {
		t.Fatal("connectAndServe returned nil; expected read loop to end after fake server closes")
	}
	select {
	case serverErr := <-done:
		if serverErr != nil {
			t.Fatalf("fake server failed: %v", serverErr)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for fake server")
	}
	stored, err := db.GetServerConnectionByID(ctx, database, connection.ID)
	if err != nil {
		t.Fatalf("get server connection: %v", err)
	}
	if stored.RemotePort != 19090 {
		t.Fatalf("remote_port=%d want=19090 stored=%+v", stored.RemotePort, stored)
	}
}

func TestManagerMarksConnectionErrorAndClosesDataWhenTunnelStops(t *testing.T) {
	ctx := context.Background()
	database, err := db.Open(ctx, filepath.Join(t.TempDir(), "test.db"), nil)
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	defer database.Close()

	connection, err := db.CreateServerConnection(ctx, database, db.CreateServerConnectionParams{
		Name:         "stopped-by-server",
		ServerHost:   "127.0.0.1",
		ServerPort:   7000,
		DataPort:     7001,
		ClientSecret: "xiaoliang_client_secret",
		AutoStart:    true,
	})
	if err != nil {
		t.Fatalf("create server connection: %v", err)
	}

	manager := NewManagerWithOptions(config.Default(), database, nil, Options{})
	dataSide, peerSide := net.Pipe()
	defer peerSide.Close()
	manager.registerDataConnection(connection.ID, "server-stop-data", dataSide)

	stopMessage, err := protocol.NewMessage(protocol.TypeTunnelStop, 42, 7, "", nil)
	if err != nil {
		t.Fatalf("build tunnel_stop message: %v", err)
	}
	if err := manager.handleMessage(ctx, connection.ID, nil, stopMessage); err != nil {
		t.Fatalf("handle tunnel_stop: %v", err)
	}

	stored, err := db.GetServerConnectionByID(ctx, database, connection.ID)
	if err != nil {
		t.Fatalf("get server connection: %v", err)
	}
	if stored.Status != model.ServerConnectionStatusError || stored.LastError != serverTunnelStoppedError {
		t.Fatalf("unexpected stopped status: %+v", stored)
	}
	manager.dataMu.Lock()
	_, stillActive := manager.data["server-stop-data"]
	manager.dataMu.Unlock()
	if stillActive {
		t.Fatal("data session was not removed after tunnel_stop")
	}
	_ = peerSide.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
	if _, err := peerSide.Read(make([]byte, 1)); err == nil {
		t.Fatal("data peer remained readable after tunnel_stop")
	} else if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
		t.Fatalf("data peer was not closed before deadline: %v", err)
	}
}

func TestManagerStoresStoppedTunnelAuthErrorFromServer(t *testing.T) {
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
		Name:         "server-stopped-at-auth",
		ServerHost:   "127.0.0.1",
		ServerPort:   port,
		DataPort:     port + 1,
		ClientSecret: "xiaoliang_client_secret",
		AutoStart:    true,
	})
	if err != nil {
		t.Fatalf("create server connection: %v", err)
	}

	serverDone := make(chan error, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			serverDone <- fmt.Errorf("accept: %w", err)
			return
		}
		defer conn.Close()
		message, err := protocol.ReadMessage(conn)
		if err != nil {
			serverDone <- fmt.Errorf("read auth: %w", err)
			return
		}
		if message.Type != protocol.TypeAuthRequest {
			serverDone <- fmt.Errorf("message type=%s want %s", message.Type, protocol.TypeAuthRequest)
			return
		}
		if err := protocol.WriteMessage(conn, protocol.NewErrorMessage(message.RequestID, protocol.CodeConflict, serverTunnelStoppedError)); err != nil {
			serverDone <- fmt.Errorf("write stopped error: %w", err)
			return
		}
		serverDone <- nil
	}()

	manager := NewManagerWithOptions(config.Default(), database, nil, Options{
		DialTimeout:       200 * time.Millisecond,
		ReconnectInterval: time.Second,
	})
	runCtx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		manager.runConnection(runCtx, connection)
	}()

	waitForConnectionError(t, database, connection.ID, serverTunnelStoppedError)
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for connection worker")
	}
	select {
	case err := <-serverDone:
		if err != nil {
			t.Fatalf("fake server failed: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for fake server")
	}
}

func TestManagerLocalizesProtocolAuthErrors(t *testing.T) {
	if got := localizeProtocolError(protocol.ProtocolError{Code: protocol.CodeUnauthorized, Message: "unauthorized"}); got != "秘钥错误" {
		t.Fatalf("unauthorized localized to %q", got)
	}
	if got := localizeProtocolError(protocol.ProtocolError{Code: protocol.CodeConflict, Message: "该连接正在占用，不得连接"}); got != "该连接正在占用，不得连接" {
		t.Fatalf("conflict localized to %q", got)
	}
	if got := localizeProtocolError(protocol.ProtocolError{Code: protocol.CodeConflict, Message: serverTunnelStoppedError}); got != serverTunnelStoppedError {
		t.Fatalf("stopped tunnel localized to %q", got)
	}
}

func waitForConnectionError(t *testing.T, database *sql.DB, id int64, want string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		var status model.ServerConnectionStatus
		var lastError string
		err := database.QueryRowContext(context.Background(), "SELECT status, COALESCE(last_error, '') FROM tunnel_connections WHERE id = ?", id).Scan(&status, &lastError)
		if err != nil {
			t.Fatalf("query server connection: %v", err)
		}
		if status == model.ServerConnectionStatusError && lastError == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	stored, err := db.GetServerConnectionByID(context.Background(), database, id)
	if err != nil {
		t.Fatalf("get server connection: %v", err)
	}
	t.Fatalf("connection status=%s last_error=%q want error %q", stored.Status, stored.LastError, want)
}
