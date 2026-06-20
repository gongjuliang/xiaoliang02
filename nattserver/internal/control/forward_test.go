package control

import (
	"bufio"
	"context"
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

func TestServerTunnelForwardsPublicTCPThroughBoundDataConnection(t *testing.T) {
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
	remotePort := freeTCPPort(t)
	tunnel, err := db.CreateTunnel(ctx, database, db.CreateTunnelParams{
		Name:       "echo",
		ClientID:   client.ID,
		Protocol:   model.TunnelProtocolTCP,
		RemoteHost: "127.0.0.1",
		RemotePort: remotePort,
	})
	if err != nil {
		t.Fatalf("create tunnel: %v", err)
	}
	markTunnelConnectable(t, ctx, database, tunnel.ID)

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

	controlConn := authenticateFakeClient(t, controlPort, "natt_client_secret")
	defer controlConn.Close()

	started, err := server.StartTunnel(ctx, tunnel.ID)
	if err != nil {
		t.Fatalf("start tunnel: %v", err)
	}
	if started.Status != model.TunnelStatusRunning {
		t.Fatalf("started tunnel status=%s want=%s", started.Status, model.TunnelStatusRunning)
	}
	startCommand, err := protocol.ReadMessage(controlConn)
	if err != nil {
		t.Fatalf("read tunnel_start command: %v", err)
	}
	if startCommand.Type != protocol.TypeTunnelStart || startCommand.TunnelID != tunnel.ID {
		t.Fatalf("unexpected start command: %+v", startCommand)
	}

	publicConn := dialWithRetry(t, fmt.Sprintf("127.0.0.1:%d", remotePort))
	defer publicConn.Close()

	dataOpen, err := protocol.ReadMessage(controlConn)
	if err != nil {
		t.Fatalf("read data_open command: %v", err)
	}
	if dataOpen.Type != protocol.TypeDataOpen || dataOpen.TunnelID != tunnel.ID || dataOpen.ConnectionID == "" {
		t.Fatalf("unexpected data_open command: %+v", dataOpen)
	}
	openPayload, err := protocol.DecodePayload[protocol.DataOpen](dataOpen)
	if err != nil {
		t.Fatalf("decode data_open payload: %v", err)
	}
	if openPayload.LocalHost != "" || openPayload.LocalPort != 0 || openPayload.DataPort != dataPort {
		t.Fatalf("unexpected data_open payload: %+v", openPayload)
	}

	dataConn := dialWithRetry(t, fmt.Sprintf("127.0.0.1:%d", dataPort))
	defer dataConn.Close()
	bind, err := protocol.NewMessage(protocol.TypeDataBind, client.ID, tunnel.ID, dataOpen.ConnectionID, protocol.DataBind{
		ClientSecret: "natt_client_secret",
	})
	if err != nil {
		t.Fatalf("build data bind: %v", err)
	}
	if err := protocol.WriteMessage(dataConn, bind); err != nil {
		t.Fatalf("write data bind: %v", err)
	}

	echoDone := make(chan error, 1)
	go func() {
		reader := bufio.NewReader(dataConn)
		line, err := reader.ReadString('\n')
		if err != nil {
			echoDone <- fmt.Errorf("read forwarded data: %w", err)
			return
		}
		_, err = dataConn.Write([]byte(line))
		echoDone <- err
	}()

	if _, err := publicConn.Write([]byte("hello through tunnel\n")); err != nil {
		t.Fatalf("write public connection: %v", err)
	}
	_ = publicConn.SetReadDeadline(time.Now().Add(2 * time.Second))
	got, err := bufio.NewReader(publicConn).ReadString('\n')
	if err != nil {
		t.Fatalf("read public response: %v", err)
	}
	if got != "hello through tunnel\n" {
		t.Fatalf("public response=%q want echo", got)
	}
	if err := <-echoDone; err != nil {
		t.Fatalf("fake data peer failed: %v", err)
	}

	stopped, err := server.StopTunnel(ctx, tunnel.ID)
	if err != nil {
		t.Fatalf("stop tunnel: %v", err)
	}
	if stopped.Status != model.TunnelStatusStopped {
		t.Fatalf("stopped tunnel status=%s want=%s", stopped.Status, model.TunnelStatusStopped)
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

func TestServerClosesPublicConnectionWhenClientSendsDataClose(t *testing.T) {
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
	remotePort := freeTCPPort(t)
	tunnel, err := db.CreateTunnel(ctx, database, db.CreateTunnelParams{
		Name:       "echo",
		ClientID:   client.ID,
		Protocol:   model.TunnelProtocolTCP,
		RemoteHost: "127.0.0.1",
		RemotePort: remotePort,
	})
	if err != nil {
		t.Fatalf("create tunnel: %v", err)
	}
	markTunnelConnectable(t, ctx, database, tunnel.ID)

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

	controlConn := authenticateFakeClient(t, controlPort, "natt_client_secret")
	defer controlConn.Close()
	if _, err := server.StartTunnel(ctx, tunnel.ID); err != nil {
		t.Fatalf("start tunnel: %v", err)
	}
	if _, err := protocol.ReadMessage(controlConn); err != nil {
		t.Fatalf("read tunnel_start command: %v", err)
	}

	publicConn := dialWithRetry(t, fmt.Sprintf("127.0.0.1:%d", remotePort))
	defer publicConn.Close()
	dataOpen, err := protocol.ReadMessage(controlConn)
	if err != nil {
		t.Fatalf("read data_open command: %v", err)
	}

	dataConn := dialWithRetry(t, fmt.Sprintf("127.0.0.1:%d", dataPort))
	defer dataConn.Close()
	bind, err := protocol.NewMessage(protocol.TypeDataBind, client.ID, tunnel.ID, dataOpen.ConnectionID, protocol.DataBind{
		ClientSecret: "natt_client_secret",
	})
	if err != nil {
		t.Fatalf("build data bind: %v", err)
	}
	if err := protocol.WriteMessage(dataConn, bind); err != nil {
		t.Fatalf("write data bind: %v", err)
	}
	closeMessage, err := protocol.NewMessage(protocol.TypeDataClose, client.ID, tunnel.ID, dataOpen.ConnectionID, protocol.DataClose{
		Code:    protocol.CodeLocalServiceUnavailable,
		Message: "local service unavailable",
	})
	if err != nil {
		t.Fatalf("build data close: %v", err)
	}
	if err := protocol.WriteMessage(controlConn, closeMessage); err != nil {
		t.Fatalf("write data close: %v", err)
	}

	_ = publicConn.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
	_, err = publicConn.Read(make([]byte, 1))
	if err == nil {
		t.Fatal("public connection remained open after data_close")
	}
	if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
		t.Fatalf("public connection was not closed before read deadline: %v", err)
	}

	_ = controlConn.Close()
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

func TestServerStopTunnelSendsDataCloseForActiveConnection(t *testing.T) {
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
	remotePort := freeTCPPort(t)
	tunnel, err := db.CreateTunnel(ctx, database, db.CreateTunnelParams{
		Name:       "echo",
		ClientID:   client.ID,
		Protocol:   model.TunnelProtocolTCP,
		RemoteHost: "127.0.0.1",
		RemotePort: remotePort,
	})
	if err != nil {
		t.Fatalf("create tunnel: %v", err)
	}
	markTunnelConnectable(t, ctx, database, tunnel.ID)

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

	controlConn := authenticateFakeClient(t, controlPort, "natt_client_secret")
	defer controlConn.Close()
	if _, err := server.StartTunnel(ctx, tunnel.ID); err != nil {
		t.Fatalf("start tunnel: %v", err)
	}
	if _, err := protocol.ReadMessage(controlConn); err != nil {
		t.Fatalf("read tunnel_start command: %v", err)
	}

	publicConn := dialWithRetry(t, fmt.Sprintf("127.0.0.1:%d", remotePort))
	defer publicConn.Close()
	dataOpen, err := protocol.ReadMessage(controlConn)
	if err != nil {
		t.Fatalf("read data_open command: %v", err)
	}
	dataConn := dialWithRetry(t, fmt.Sprintf("127.0.0.1:%d", dataPort))
	defer dataConn.Close()
	bind, err := protocol.NewMessage(protocol.TypeDataBind, client.ID, tunnel.ID, dataOpen.ConnectionID, protocol.DataBind{
		ClientSecret: "natt_client_secret",
	})
	if err != nil {
		t.Fatalf("build data bind: %v", err)
	}
	if err := protocol.WriteMessage(dataConn, bind); err != nil {
		t.Fatalf("write data bind: %v", err)
	}
	if _, err := publicConn.Write([]byte("x")); err != nil {
		t.Fatalf("write public probe: %v", err)
	}
	_ = dataConn.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := dataConn.Read(make([]byte, 1)); err != nil {
		t.Fatalf("read data probe: %v", err)
	}
	_ = dataConn.SetReadDeadline(time.Time{})

	if _, err := server.StopTunnel(ctx, tunnel.ID); err != nil {
		t.Fatalf("stop tunnel: %v", err)
	}
	closeCommand, err := protocol.ReadMessage(controlConn)
	if err != nil {
		t.Fatalf("read data_close command: %v", err)
	}
	if closeCommand.Type != protocol.TypeDataClose || closeCommand.TunnelID != tunnel.ID || closeCommand.ConnectionID != dataOpen.ConnectionID {
		t.Fatalf("unexpected data_close command: %+v", closeCommand)
	}
	closePayload, err := protocol.DecodePayload[protocol.DataClose](closeCommand)
	if err != nil {
		t.Fatalf("decode data_close payload: %v", err)
	}
	if closePayload.Code != protocol.CodeOK || closePayload.Message == "" {
		t.Fatalf("unexpected data_close payload: %+v", closePayload)
	}
	stopCommand, err := protocol.ReadMessage(controlConn)
	if err != nil {
		t.Fatalf("read tunnel_stop command: %v", err)
	}
	if stopCommand.Type != protocol.TypeTunnelStop || stopCommand.TunnelID != tunnel.ID {
		t.Fatalf("unexpected tunnel_stop command: %+v", stopCommand)
	}
	assertConnClosed(t, dataConn, "data connection")
	assertConnClosed(t, publicConn, "public connection")

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

func assertConnClosed(t *testing.T, conn net.Conn, name string) {
	t.Helper()
	_ = conn.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
	_, err := conn.Read(make([]byte, 1))
	if err == nil {
		t.Fatalf("%s remained open", name)
	}
	if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
		t.Fatalf("%s was not closed before read deadline: %v", name, err)
	}
}

func authenticateFakeClient(t *testing.T, controlPort int, secret string) net.Conn {
	t.Helper()
	conn := dialWithRetry(t, fmt.Sprintf("127.0.0.1:%d", controlPort))
	authReq, err := protocol.NewMessage(protocol.TypeAuthRequest, 0, 0, "", protocol.AuthRequest{
		ClientSecret:    secret,
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
	if authRespMsg.Type != protocol.TypeAuthResponse {
		t.Fatalf("auth response type=%s want=%s", authRespMsg.Type, protocol.TypeAuthResponse)
	}
	return conn
}
