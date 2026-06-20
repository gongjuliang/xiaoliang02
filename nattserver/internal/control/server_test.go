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
	tunnel, err := db.CreateTunnel(ctx, database, db.CreateTunnelParams{
		Name:       "client-a-control",
		ClientID:   client.ID,
		Protocol:   model.TunnelProtocolTCP,
		RemoteHost: "127.0.0.1",
		RemotePort: freeTCPPort(t),
	})
	if err != nil {
		t.Fatalf("create tunnel: %v", err)
	}
	markTunnelConnectable(t, ctx, database, tunnel.ID)

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
	tunnel, err := db.CreateTunnel(ctx, database, db.CreateTunnelParams{
		Name:       "client-a-timeout",
		ClientID:   client.ID,
		Protocol:   model.TunnelProtocolTCP,
		RemoteHost: "127.0.0.1",
		RemotePort: freeTCPPort(t),
	})
	if err != nil {
		t.Fatalf("create tunnel: %v", err)
	}
	markTunnelConnectable(t, ctx, database, tunnel.ID)

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

func TestServerRecoversManuallyStartedTunnelWhenClientReconnects(t *testing.T) {
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
		Name:       "manual-echo",
		ClientID:   client.ID,
		Protocol:   model.TunnelProtocolTCP,
		RemoteHost: "127.0.0.1",
		RemotePort: freeTCPPort(t),
		AutoStart:  false,
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

	firstConn := authenticateFakeClient(t, controlPort, "natt_client_secret")
	if _, err := server.StartTunnel(ctx, tunnel.ID); err != nil {
		t.Fatalf("start manual tunnel: %v", err)
	}
	expectTunnelStartCommand(t, firstConn, tunnel.ID)
	waitForTunnelStatus(t, database, tunnel.ID, model.TunnelStatusRunning)
	_ = firstConn.Close()
	waitForClientStatus(t, database, client.ID, model.OnlineStatusOffline)
	waitForTunnelStatus(t, database, tunnel.ID, model.TunnelStatusError)
	assertTunnelLastError(t, database, tunnel.ID, controlOfflineMessage)

	secondConn := authenticateFakeClient(t, controlPort, "natt_client_secret")
	defer secondConn.Close()
	expectTunnelStartCommand(t, secondConn, tunnel.ID)
	waitForTunnelStatus(t, database, tunnel.ID, model.TunnelStatusRunning)
	assertTunnelLastError(t, database, tunnel.ID, "")

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

func TestServerHeartbeatRecoversOfflineControlError(t *testing.T) {
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
		Name:       "heartbeat-recover",
		ClientID:   client.ID,
		Protocol:   model.TunnelProtocolTCP,
		RemoteHost: "127.0.0.1",
		RemotePort: freeTCPPort(t),
		AutoStart:  false,
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

	conn := authenticateFakeClient(t, controlPort, "natt_client_secret")
	defer conn.Close()
	if _, err := db.SetTunnelStatus(ctx, database, tunnel.ID, model.TunnelStatusError, controlOfflineMessage); err != nil {
		t.Fatalf("set offline error: %v", err)
	}

	heartbeat, err := protocol.NewMessage(protocol.TypeHeartbeat, client.ID, tunnel.ID, "", protocol.Heartbeat{ClientTime: time.Now().Unix()})
	if err != nil {
		t.Fatalf("build heartbeat: %v", err)
	}
	if err := protocol.WriteMessage(conn, heartbeat); err != nil {
		t.Fatalf("write heartbeat: %v", err)
	}
	expectTunnelStartAndHeartbeatAck(t, conn, tunnel.ID, heartbeat.RequestID)
	waitForTunnelStatus(t, database, tunnel.ID, model.TunnelStatusRunning)
	assertTunnelLastError(t, database, tunnel.ID, "")

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

func TestServerStartTunnelWithoutOnlineClientMovesToWaiting(t *testing.T) {
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
		Name:       "offline-start",
		ClientID:   client.ID,
		Protocol:   model.TunnelProtocolTCP,
		RemoteHost: "127.0.0.1",
		RemotePort: remotePort,
		AutoStart:  false,
	})
	if err != nil {
		t.Fatalf("create tunnel: %v", err)
	}

	server := NewServer(config.ProtocolConfig{}, database, nil)
	started, err := server.StartTunnel(ctx, tunnel.ID)
	if err != nil {
		t.Fatalf("start offline tunnel: %v", err)
	}
	if started.Status != model.TunnelStatusWaiting || !started.AutoStart || started.LastError != "" {
		t.Fatalf("offline start returned %+v, want waiting auto_start with empty last_error", started)
	}
	stored, err := db.GetTunnelByID(ctx, database, tunnel.ID)
	if err != nil {
		t.Fatalf("get stored tunnel: %v", err)
	}
	if stored.Status != model.TunnelStatusWaiting || !stored.AutoStart || stored.LastError != "" {
		t.Fatalf("stored tunnel after offline start=%+v, want waiting auto_start with empty last_error", stored)
	}
	listener, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", remotePort))
	if err != nil {
		t.Fatalf("offline start should not bind public port %d: %v", remotePort, err)
	}
	_ = listener.Close()
}

func TestServerHeartbeatAckIncludesCurrentTunnelStatus(t *testing.T) {
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
		Name:       "heartbeat-state",
		ClientID:   client.ID,
		Protocol:   model.TunnelProtocolTCP,
		RemoteHost: "127.0.0.1",
		RemotePort: remotePort,
		AutoStart:  false,
	})
	if err != nil {
		t.Fatalf("create tunnel: %v", err)
	}
	if _, err := db.SetTunnelStatus(ctx, database, tunnel.ID, model.TunnelStatusError, "公网端口被占用"); err != nil {
		t.Fatalf("set tunnel error: %v", err)
	}

	controlPort := freeTCPPort(t)
	server := NewServer(config.ProtocolConfig{
		ControlHost: "127.0.0.1",
		ControlPort: controlPort,
	}, database, nil)
	runCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runDone := make(chan error, 1)
	go func() {
		runDone <- server.Run(runCtx)
	}()

	conn := authenticateFakeClient(t, controlPort, "natt_client_secret")
	defer conn.Close()
	heartbeat, err := protocol.NewMessage(protocol.TypeHeartbeat, client.ID, tunnel.ID, "", protocol.Heartbeat{ClientTime: time.Now().Unix()})
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
	ack, err := protocol.DecodePayload[protocol.HeartbeatAck](ackMsg)
	if err != nil {
		t.Fatalf("decode heartbeat ack: %v", err)
	}
	if ack.TunnelStatus != string(model.TunnelStatusError) || ack.LastError != "公网端口被占用" || ack.RemotePort != remotePort {
		t.Fatalf("heartbeat ack payload=%+v, want error with last_error and remote_port=%d", ack, remotePort)
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

func TestServerDoesNotRecoverNonOfflineTunnelErrorOnReconnect(t *testing.T) {
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
		Name:       "port-error",
		ClientID:   client.ID,
		Protocol:   model.TunnelProtocolTCP,
		RemoteHost: "127.0.0.1",
		RemotePort: freeTCPPort(t),
		AutoStart:  true,
	})
	if err != nil {
		t.Fatalf("create tunnel: %v", err)
	}
	const portError = "listen remote port: bind failed"
	if _, err := db.SetTunnelStatus(ctx, database, tunnel.ID, model.TunnelStatusError, portError); err != nil {
		t.Fatalf("set non-offline error: %v", err)
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

	conn := authenticateFakeClient(t, controlPort, "natt_client_secret")
	defer conn.Close()
	assertNoControlMessage(t, conn)
	waitForTunnelStatus(t, database, tunnel.ID, model.TunnelStatusError)
	assertTunnelLastError(t, database, tunnel.ID, portError)

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

func TestServerAllowsWaitingAutoStartTunnelOnFirstConnection(t *testing.T) {
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
		Name:       "waiting-auto",
		ClientID:   client.ID,
		Protocol:   model.TunnelProtocolTCP,
		RemoteHost: "127.0.0.1",
		RemotePort: freeTCPPort(t),
		AutoStart:  true,
	})
	if err != nil {
		t.Fatalf("create tunnel: %v", err)
	}
	if tunnel.Status != model.TunnelStatusWaiting {
		t.Fatalf("created tunnel status=%s want waiting", tunnel.Status)
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

	conn := authenticateFakeClient(t, controlPort, "natt_client_secret")
	defer conn.Close()
	expectTunnelStartCommand(t, conn, tunnel.ID)
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
	if protocolErr.Message != "秘钥错误" {
		t.Fatalf("error message=%q want 秘钥错误", protocolErr.Message)
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

func TestServerRejectsSecondControlConnectionWhileTunnelIsOccupied(t *testing.T) {
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
		Name:       "occupied",
		ClientID:   client.ID,
		Protocol:   model.TunnelProtocolTCP,
		RemoteHost: "127.0.0.1",
		RemotePort: freeTCPPort(t),
	})
	if err != nil {
		t.Fatalf("create tunnel: %v", err)
	}
	markTunnelConnectable(t, ctx, database, tunnel.ID)

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

	firstConn := authenticateFakeClient(t, port, "natt_client_secret")
	defer firstConn.Close()

	secondConn := dialWithRetry(t, fmt.Sprintf("127.0.0.1:%d", port))
	defer secondConn.Close()
	authReq, err := protocol.NewMessage(protocol.TypeAuthRequest, 0, 0, "", protocol.AuthRequest{
		ClientSecret:    "natt_client_secret",
		ClientName:      "client-b",
		ClientVersion:   "test-version",
		ProtocolVersion: protocol.Version,
	})
	if err != nil {
		t.Fatalf("build auth request: %v", err)
	}
	if err := protocol.WriteMessage(secondConn, authReq); err != nil {
		t.Fatalf("write auth request: %v", err)
	}
	errorMsg, err := protocol.ReadMessage(secondConn)
	if err != nil {
		t.Fatalf("read occupied auth error: %v", err)
	}
	protocolErr, err := protocol.DecodePayload[protocol.ProtocolError](errorMsg)
	if err != nil {
		t.Fatalf("decode occupied auth error: %v", err)
	}
	if errorMsg.Type != protocol.TypeError || protocolErr.Code != protocol.CodeConflict || protocolErr.Message != "该连接正在占用，不得连接" {
		t.Fatalf("unexpected occupied error message=%+v payload=%+v", errorMsg, protocolErr)
	}

	_ = firstConn.Close()
	waitForClientStatus(t, database, client.ID, model.OnlineStatusOffline)
	thirdConn := authenticateFakeClient(t, port, "natt_client_secret")
	_ = thirdConn.Close()
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

func TestServerRejectsControlConnectionWhenTunnelIsStopped(t *testing.T) {
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
	if _, err := db.CreateTunnel(ctx, database, db.CreateTunnelParams{
		Name:       "stopped-tunnel",
		ClientID:   client.ID,
		Protocol:   model.TunnelProtocolTCP,
		RemoteHost: "127.0.0.1",
		RemotePort: freeTCPPort(t),
		AutoStart:  false,
	}); err != nil {
		t.Fatalf("create tunnel: %v", err)
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
	errorMsg, err := protocol.ReadMessage(conn)
	if err != nil {
		t.Fatalf("read stopped auth error: %v", err)
	}
	protocolErr, err := protocol.DecodePayload[protocol.ProtocolError](errorMsg)
	if err != nil {
		t.Fatalf("decode stopped auth error: %v", err)
	}
	if errorMsg.Type != protocol.TypeError || protocolErr.Code != protocol.CodeConflict || protocolErr.Message != tunnelStoppedByServerMessage {
		t.Fatalf("unexpected stopped error message=%+v payload=%+v", errorMsg, protocolErr)
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

func TestServerDisconnectTunnelClosesControlAndDeletedSecretCannotReconnect(t *testing.T) {
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
		Name:       "delete-disconnect",
		ClientID:   client.ID,
		Protocol:   model.TunnelProtocolTCP,
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

	conn := authenticateFakeClient(t, controlPort, "natt_client_secret")
	expectTunnelStartCommand(t, conn, tunnel.ID)
	waitForTunnelStatus(t, database, tunnel.ID, model.TunnelStatusRunning)

	if _, err := db.SetTunnelKeyStatus(ctx, database, tunnel.ID, model.TunnelKeyStatusDisabled); err != nil {
		t.Fatalf("disable tunnel key: %v", err)
	}
	if _, err := server.StopTunnel(ctx, tunnel.ID); err != nil {
		t.Fatalf("stop tunnel before delete: %v", err)
	}
	stopCommand, err := protocol.ReadMessage(conn)
	if err != nil {
		t.Fatalf("read tunnel_stop command: %v", err)
	}
	if stopCommand.Type != protocol.TypeTunnelStop || stopCommand.TunnelID != tunnel.ID {
		t.Fatalf("unexpected tunnel_stop command: %+v", stopCommand)
	}
	server.DisconnectTunnel(tunnel.ID)
	assertConnClosed(t, conn, "control connection")
	if _, err := db.DeleteTunnel(ctx, database, tunnel.ID); err != nil {
		t.Fatalf("delete tunnel: %v", err)
	}

	reconnect := dialWithRetry(t, fmt.Sprintf("127.0.0.1:%d", controlPort))
	defer reconnect.Close()
	authReq, err := protocol.NewMessage(protocol.TypeAuthRequest, 0, 0, "", protocol.AuthRequest{
		ClientSecret:    "natt_client_secret",
		ClientName:      "client-a",
		ClientVersion:   "test-version",
		ProtocolVersion: protocol.Version,
	})
	if err != nil {
		t.Fatalf("build reconnect auth request: %v", err)
	}
	if err := protocol.WriteMessage(reconnect, authReq); err != nil {
		t.Fatalf("write reconnect auth request: %v", err)
	}
	errorMsg, err := protocol.ReadMessage(reconnect)
	if err != nil {
		t.Fatalf("read reconnect auth error: %v", err)
	}
	protocolErr, err := protocol.DecodePayload[protocol.ProtocolError](errorMsg)
	if err != nil {
		t.Fatalf("decode reconnect auth error: %v", err)
	}
	if errorMsg.Type != protocol.TypeError || protocolErr.Code != protocol.CodeUnauthorized {
		t.Fatalf("unexpected reconnect auth error=%+v payload=%+v", errorMsg, protocolErr)
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

func assertTunnelLastError(t *testing.T, database *sql.DB, id int64, want string) {
	t.Helper()
	var got sql.NullString
	if err := database.QueryRowContext(context.Background(), "SELECT last_error FROM tunnels WHERE id = ?", id).Scan(&got); err != nil {
		t.Fatalf("query tunnel last_error: %v", err)
	}
	if value := nullableTestString(got); value != want {
		t.Fatalf("tunnel last_error=%q want=%q", value, want)
	}
}

func nullableTestString(value sql.NullString) string {
	if !value.Valid {
		return ""
	}
	return value.String
}

func markTunnelConnectable(t *testing.T, ctx context.Context, database *sql.DB, id int64) {
	t.Helper()
	if _, err := db.SetTunnelStatus(ctx, database, id, model.TunnelStatusRunning, ""); err != nil {
		t.Fatalf("mark tunnel connectable: %v", err)
	}
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

func expectTunnelStartAndHeartbeatAck(t *testing.T, conn net.Conn, tunnelID int64, heartbeatRequestID string) {
	t.Helper()
	_ = conn.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
	defer conn.SetReadDeadline(time.Time{})
	seenStart := false
	seenAck := false
	for !seenStart || !seenAck {
		message, err := protocol.ReadMessage(conn)
		if err != nil {
			t.Fatalf("read recovery messages: %v seen_start=%t seen_ack=%t", err, seenStart, seenAck)
		}
		switch message.Type {
		case protocol.TypeTunnelStart:
			if message.TunnelID != tunnelID {
				t.Fatalf("unexpected tunnel_start command: %+v", message)
			}
			seenStart = true
		case protocol.TypeHeartbeatAck:
			if message.RequestID != heartbeatRequestID {
				t.Fatalf("unexpected heartbeat ack: %+v", message)
			}
			seenAck = true
		default:
			t.Fatalf("unexpected recovery message: %+v", message)
		}
	}
}

func assertNoControlMessage(t *testing.T, conn net.Conn) {
	t.Helper()
	_ = conn.SetReadDeadline(time.Now().Add(150 * time.Millisecond))
	defer conn.SetReadDeadline(time.Time{})
	message, err := protocol.ReadMessage(conn)
	if err == nil {
		t.Fatalf("unexpected control message: %+v", message)
	}
	if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
		return
	}
	t.Fatalf("read control message failed with non-timeout error: %v", err)
}
