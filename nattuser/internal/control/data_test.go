package control

import (
	"bufio"
	"context"
	"database/sql"
	"fmt"
	"net"
	"path/filepath"
	"testing"
	"time"

	"nattuser/internal/config"
	"nattuser/internal/db"
	"nattuser/internal/model"
	"nattuser/internal/protocol"
)

func TestManagerOpensDataConnectionAndForwardsToLocalTCPService(t *testing.T) {
	ctx := context.Background()
	database, err := db.Open(ctx, filepath.Join(t.TempDir(), "test.db"), nil)
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	defer database.Close()

	localAddr, stopEcho := startLocalEchoServer(t)
	defer stopEcho()
	localHost, localPort := splitHostPort(t, localAddr)

	dataListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen fake data server: %v", err)
	}
	defer dataListener.Close()
	dataHost, dataPort := splitHostPort(t, dataListener.Addr().String())

	controlListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen fake control server: %v", err)
	}
	defer controlListener.Close()
	controlHost, controlPort := splitHostPort(t, controlListener.Addr().String())

	connection, err := db.CreateServerConnection(ctx, database, db.CreateServerConnectionParams{
		Name:         "test-server",
		ServerHost:   controlHost,
		ServerPort:   controlPort,
		DataPort:     dataPort,
		ClientSecret: "natt_client_secret",
		AutoStart:    true,
	})
	if err != nil {
		t.Fatalf("create server connection: %v", err)
	}
	createLocalTunnelBinding(t, ctx, database, connection.ID, 7, localHost, localPort, true)

	dataDone := make(chan error, 1)
	go runFakeDataServer(t, dataListener, dataDone)
	controlDone := make(chan error, 1)
	go runDataOpenControlServer(t, controlListener, dataHost, dataPort, controlDone)

	cfg := config.Default()
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

	waitForDataDone(t, dataDone, controlDone)

	stored, err := db.GetServerConnectionByID(ctx, database, connection.ID)
	if err != nil {
		t.Fatalf("get server connection: %v", err)
	}
	if stored.Status != model.ServerConnectionStatusConnected {
		t.Fatalf("server connection status=%s want=%s", stored.Status, model.ServerConnectionStatusConnected)
	}

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

func TestManagerSendsDataCloseWhenLocalTCPServiceIsUnavailable(t *testing.T) {
	ctx := context.Background()
	database, err := db.Open(ctx, filepath.Join(t.TempDir(), "test.db"), nil)
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	defer database.Close()

	unusedLocalListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve local port: %v", err)
	}
	localHost, localPort := splitHostPort(t, unusedLocalListener.Addr().String())
	_ = unusedLocalListener.Close()

	dataListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen fake data server: %v", err)
	}
	defer dataListener.Close()
	dataHost, dataPort := splitHostPort(t, dataListener.Addr().String())

	controlListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen fake control server: %v", err)
	}
	defer controlListener.Close()
	controlHost, controlPort := splitHostPort(t, controlListener.Addr().String())

	connection, err := db.CreateServerConnection(ctx, database, db.CreateServerConnectionParams{
		Name:         "test-server",
		ServerHost:   controlHost,
		ServerPort:   controlPort,
		DataPort:     dataPort,
		ClientSecret: "natt_client_secret",
		AutoStart:    true,
	})
	if err != nil {
		t.Fatalf("create server connection: %v", err)
	}
	createLocalTunnelBinding(t, ctx, database, connection.ID, 7, localHost, localPort, true)

	dataDone := make(chan error, 1)
	go expectDataBindOnly(t, dataListener, dataDone)
	closeDone := make(chan error, 1)
	go runDataCloseControlServer(t, controlListener, dataHost, dataPort, closeDone)

	cfg := config.Default()
	manager := NewManagerWithOptions(cfg, database, nil, Options{
		ScanInterval:      10 * time.Millisecond,
		ReconnectInterval: 20 * time.Millisecond,
		DialTimeout:       100 * time.Millisecond,
		HeartbeatInterval: time.Second,
	})
	runCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	managerDone := make(chan error, 1)
	go func() {
		managerDone <- manager.Run(runCtx)
	}()

	select {
	case err := <-closeDone:
		if err != nil {
			t.Fatalf("fake control server failed: %v", err)
		}
	case err := <-dataDone:
		t.Fatalf("fake data server failed before data_close: %v", err)
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for data_close")
	}

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

func TestManagerClosesDataConnectionWhenServerSendsDataClose(t *testing.T) {
	ctx := context.Background()
	database, err := db.Open(ctx, filepath.Join(t.TempDir(), "test.db"), nil)
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	defer database.Close()

	localAddr, stopEcho := startLocalEchoServer(t)
	defer stopEcho()
	localHost, localPort := splitHostPort(t, localAddr)

	dataListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen fake data server: %v", err)
	}
	defer dataListener.Close()
	dataHost, dataPort := splitHostPort(t, dataListener.Addr().String())

	controlListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen fake control server: %v", err)
	}
	defer controlListener.Close()
	controlHost, controlPort := splitHostPort(t, controlListener.Addr().String())

	connection, err := db.CreateServerConnection(ctx, database, db.CreateServerConnectionParams{
		Name:         "test-server",
		ServerHost:   controlHost,
		ServerPort:   controlPort,
		DataPort:     dataPort,
		ClientSecret: "natt_client_secret",
		AutoStart:    true,
	})
	if err != nil {
		t.Fatalf("create server connection: %v", err)
	}
	createLocalTunnelBinding(t, ctx, database, connection.ID, 7, localHost, localPort, true)

	dataBound := make(chan struct{})
	dataDone := make(chan error, 1)
	go expectDataConnectionClosedAfterBind(t, dataListener, "conn-close", dataBound, dataDone)
	controlDone := make(chan error, 1)
	go runServerDataCloseControlServer(t, controlListener, dataHost, dataPort, dataBound, controlDone)

	cfg := config.Default()
	manager := NewManagerWithOptions(cfg, database, nil, Options{
		ScanInterval:      10 * time.Millisecond,
		ReconnectInterval: 20 * time.Millisecond,
		DialTimeout:       200 * time.Millisecond,
		HeartbeatInterval: time.Second,
	})
	runCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	managerDone := make(chan error, 1)
	go func() {
		managerDone <- manager.Run(runCtx)
	}()

	waitForDataDone(t, dataDone, controlDone)

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

func TestManagerSendsDataCloseWhenLocalTunnelBindingIsMissing(t *testing.T) {
	ctx := context.Background()
	database, err := db.Open(ctx, filepath.Join(t.TempDir(), "test.db"), nil)
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	defer database.Close()

	dataHost := "127.0.0.1"
	dataPort := 1

	controlListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen fake control server: %v", err)
	}
	defer controlListener.Close()
	controlHost, controlPort := splitHostPort(t, controlListener.Addr().String())

	if _, err := db.CreateServerConnection(ctx, database, db.CreateServerConnectionParams{
		Name:         "test-server",
		ServerHost:   controlHost,
		ServerPort:   controlPort,
		DataPort:     dataPort,
		ClientSecret: "natt_client_secret",
		AutoStart:    true,
	}); err != nil {
		t.Fatalf("create server connection: %v", err)
	}

	closeDone := make(chan error, 1)
	go runDataCloseControlServer(t, controlListener, dataHost, dataPort, closeDone)

	cfg := config.Default()
	manager := NewManagerWithOptions(cfg, database, nil, Options{
		ScanInterval:      10 * time.Millisecond,
		ReconnectInterval: 20 * time.Millisecond,
		DialTimeout:       100 * time.Millisecond,
		HeartbeatInterval: time.Second,
	})
	runCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	managerDone := make(chan error, 1)
	go func() {
		managerDone <- manager.Run(runCtx)
	}()

	select {
	case err := <-closeDone:
		if err != nil {
			t.Fatalf("fake control server failed: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for data_close")
	}

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

func runDataOpenControlServer(t *testing.T, listener net.Listener, dataHost string, dataPort int, done chan<- error) {
	t.Helper()
	defer close(done)

	conn, err := listener.Accept()
	if err != nil {
		done <- fmt.Errorf("accept control connection: %w", err)
		return
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(1 * time.Second))

	authReqMessage, err := protocol.ReadMessage(conn)
	if err != nil {
		done <- fmt.Errorf("read auth request: %w", err)
		return
	}
	authReq, err := protocol.DecodePayload[protocol.AuthRequest](authReqMessage)
	if err != nil {
		done <- err
		return
	}
	if authReq.ClientSecret != "natt_client_secret" {
		done <- fmt.Errorf("client secret=%s want natt_client_secret", authReq.ClientSecret)
		return
	}
	authResp, err := protocol.NewMessage(protocol.TypeAuthResponse, 42, 0, "", protocol.AuthResponse{
		Success:                  true,
		ClientID:                 42,
		ProtocolVersion:          protocol.Version,
		HeartbeatIntervalSeconds: 30,
	})
	if err != nil {
		done <- err
		return
	}
	authResp.RequestID = authReqMessage.RequestID
	if err := protocol.WriteMessage(conn, authResp); err != nil {
		done <- fmt.Errorf("write auth response: %w", err)
		return
	}
	dataOpen, err := protocol.NewMessage(protocol.TypeDataOpen, 42, 7, "conn-1", protocol.DataOpen{
		DataHost: dataHost,
		DataPort: dataPort,
	})
	if err != nil {
		done <- err
		return
	}
	if err := protocol.WriteMessage(conn, dataOpen); err != nil {
		done <- fmt.Errorf("write data open: %w", err)
		return
	}

	time.Sleep(200 * time.Millisecond)
	done <- nil
}

func runFakeDataServer(t *testing.T, listener net.Listener, done chan<- error) {
	t.Helper()
	conn, err := listener.Accept()
	if err != nil {
		done <- fmt.Errorf("accept data connection: %w", err)
		return
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(2 * time.Second))

	bind, err := protocol.ReadMessage(conn)
	if err != nil {
		done <- fmt.Errorf("read data bind: %w", err)
		return
	}
	if bind.Type != protocol.TypeDataBind || bind.ClientID != 42 || bind.TunnelID != 7 || bind.ConnectionID != "conn-1" {
		done <- fmt.Errorf("unexpected data bind: %+v", bind)
		return
	}
	bindPayload, err := protocol.DecodePayload[protocol.DataBind](bind)
	if err != nil {
		done <- err
		return
	}
	if bindPayload.ClientSecret != "natt_client_secret" {
		done <- fmt.Errorf("data bind secret=%s want natt_client_secret", bindPayload.ClientSecret)
		return
	}

	if _, err := conn.Write([]byte("ping local\n")); err != nil {
		done <- fmt.Errorf("write data payload: %w", err)
		return
	}
	got, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil {
		done <- fmt.Errorf("read echoed payload: %w", err)
		return
	}
	if got != "ping local\n" {
		done <- fmt.Errorf("echo response=%q want ping local", got)
		return
	}
	done <- nil
}

func runDataCloseControlServer(t *testing.T, listener net.Listener, dataHost string, dataPort int, done chan<- error) {
	t.Helper()
	defer close(done)

	conn, err := listener.Accept()
	if err != nil {
		done <- fmt.Errorf("accept control connection: %w", err)
		return
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(2 * time.Second))

	authReqMessage, err := protocol.ReadMessage(conn)
	if err != nil {
		done <- fmt.Errorf("read auth request: %w", err)
		return
	}
	authResp, err := protocol.NewMessage(protocol.TypeAuthResponse, 42, 0, "", protocol.AuthResponse{
		Success:                  true,
		ClientID:                 42,
		ProtocolVersion:          protocol.Version,
		HeartbeatIntervalSeconds: 30,
	})
	if err != nil {
		done <- err
		return
	}
	authResp.RequestID = authReqMessage.RequestID
	if err := protocol.WriteMessage(conn, authResp); err != nil {
		done <- fmt.Errorf("write auth response: %w", err)
		return
	}
	dataOpen, err := protocol.NewMessage(protocol.TypeDataOpen, 42, 7, "conn-unavailable", protocol.DataOpen{
		DataHost: dataHost,
		DataPort: dataPort,
	})
	if err != nil {
		done <- err
		return
	}
	if err := protocol.WriteMessage(conn, dataOpen); err != nil {
		done <- fmt.Errorf("write data open: %w", err)
		return
	}

	closeMessage, err := protocol.ReadMessage(conn)
	if err != nil {
		done <- fmt.Errorf("read data_close: %w", err)
		return
	}
	if closeMessage.Type != protocol.TypeDataClose || closeMessage.TunnelID != 7 || closeMessage.ConnectionID != "conn-unavailable" {
		done <- fmt.Errorf("unexpected data_close: %+v", closeMessage)
		return
	}
	payload, err := protocol.DecodePayload[protocol.DataClose](closeMessage)
	if err != nil {
		done <- err
		return
	}
	if payload.Code != protocol.CodeLocalServiceUnavailable || payload.Message == "" {
		done <- fmt.Errorf("unexpected data_close payload: %+v", payload)
		return
	}
	done <- nil
}

func expectDataBindOnly(t *testing.T, listener net.Listener, done chan<- error) {
	t.Helper()
	conn, err := listener.Accept()
	if err != nil {
		done <- fmt.Errorf("accept data connection: %w", err)
		return
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(2 * time.Second))

	bind, err := protocol.ReadMessage(conn)
	if err != nil {
		done <- fmt.Errorf("read data bind: %w", err)
		return
	}
	if bind.Type != protocol.TypeDataBind || bind.ConnectionID != "conn-unavailable" {
		done <- fmt.Errorf("unexpected data bind: %+v", bind)
		return
	}
	select {}
}

func runServerDataCloseControlServer(t *testing.T, listener net.Listener, dataHost string, dataPort int, dataBound <-chan struct{}, done chan<- error) {
	t.Helper()
	defer close(done)

	conn, err := listener.Accept()
	if err != nil {
		done <- fmt.Errorf("accept control connection: %w", err)
		return
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(2 * time.Second))

	authReqMessage, err := protocol.ReadMessage(conn)
	if err != nil {
		done <- fmt.Errorf("read auth request: %w", err)
		return
	}
	authResp, err := protocol.NewMessage(protocol.TypeAuthResponse, 42, 0, "", protocol.AuthResponse{
		Success:                  true,
		ClientID:                 42,
		ProtocolVersion:          protocol.Version,
		HeartbeatIntervalSeconds: 30,
	})
	if err != nil {
		done <- err
		return
	}
	authResp.RequestID = authReqMessage.RequestID
	if err := protocol.WriteMessage(conn, authResp); err != nil {
		done <- fmt.Errorf("write auth response: %w", err)
		return
	}
	dataOpen, err := protocol.NewMessage(protocol.TypeDataOpen, 42, 7, "conn-close", protocol.DataOpen{
		DataHost: dataHost,
		DataPort: dataPort,
	})
	if err != nil {
		done <- err
		return
	}
	if err := protocol.WriteMessage(conn, dataOpen); err != nil {
		done <- fmt.Errorf("write data open: %w", err)
		return
	}
	select {
	case <-dataBound:
	case <-time.After(2 * time.Second):
		done <- fmt.Errorf("timed out waiting for data bind")
		return
	}
	dataClose, err := protocol.NewMessage(protocol.TypeDataClose, 42, 7, "conn-close", protocol.DataClose{
		Code:    protocol.CodeOK,
		Message: "server requested close",
	})
	if err != nil {
		done <- err
		return
	}
	if err := protocol.WriteMessage(conn, dataClose); err != nil {
		done <- fmt.Errorf("write data close: %w", err)
		return
	}
	time.Sleep(100 * time.Millisecond)
	done <- nil
}

func expectDataConnectionClosedAfterBind(t *testing.T, listener net.Listener, connectionID string, dataBound chan<- struct{}, done chan<- error) {
	t.Helper()
	conn, err := listener.Accept()
	if err != nil {
		done <- fmt.Errorf("accept data connection: %w", err)
		return
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(2 * time.Second))

	bind, err := protocol.ReadMessage(conn)
	if err != nil {
		done <- fmt.Errorf("read data bind: %w", err)
		return
	}
	if bind.Type != protocol.TypeDataBind || bind.ConnectionID != connectionID {
		done <- fmt.Errorf("unexpected data bind: %+v", bind)
		return
	}
	close(dataBound)
	_, err = conn.Read(make([]byte, 1))
	if err == nil {
		done <- fmt.Errorf("data connection remained open")
		return
	}
	if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
		done <- fmt.Errorf("data connection was not closed before read deadline: %w", err)
		return
	}
	done <- nil
}

func startLocalEchoServer(t *testing.T) (addr string, stop func()) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen local echo server: %v", err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				reader := bufio.NewReader(c)
				for {
					line, err := reader.ReadString('\n')
					if err != nil {
						return
					}
					if _, err := c.Write([]byte(line)); err != nil {
						return
					}
				}
			}(conn)
		}
	}()
	return listener.Addr().String(), func() {
		_ = listener.Close()
		<-done
	}
}

func splitHostPort(t *testing.T, addr string) (string, int) {
	t.Helper()
	host, portText, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatalf("split addr %s: %v", addr, err)
	}
	var port int
	if _, err := fmt.Sscanf(portText, "%d", &port); err != nil {
		t.Fatalf("parse port %s: %v", portText, err)
	}
	return host, port
}

func waitForDataDone(t *testing.T, dataDone <-chan error, controlDone <-chan error) {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		select {
		case err := <-dataDone:
			if err != nil {
				t.Fatalf("fake data server failed: %v", err)
			}
			return
		case err := <-controlDone:
			if err != nil {
				t.Fatalf("fake control server failed before data close: %v", err)
			}
		case <-deadline:
			t.Fatal("timed out waiting for data connection close")
		}
	}
}

func createLocalTunnelBinding(t *testing.T, ctx context.Context, database *sql.DB, serverConnectionID int64, serverTunnelID int64, localHost string, localPort int, enabled bool) {
	t.Helper()
	if _, err := db.CreateLocalTunnel(ctx, database, db.CreateLocalTunnelParams{
		Name:               fmt.Sprintf("local-%d", serverTunnelID),
		ServerConnectionID: serverConnectionID,
		ServerTunnelID:     serverTunnelID,
		LocalHost:          localHost,
		LocalPort:          localPort,
		Enabled:            enabled,
	}); err != nil {
		t.Fatalf("create local tunnel binding: %v", err)
	}
}
