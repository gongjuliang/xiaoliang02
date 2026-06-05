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

	"nattserver/internal/auth"
	"nattserver/internal/config"
	"nattserver/internal/db"
	"nattserver/internal/model"
	"nattserver/internal/protocol"
)

func TestEndToEndTCPForwardingThroughProtocolClient(t *testing.T) {
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
		Name:       "echo-e2e",
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

	controlConn := authenticateFakeClient(t, controlPort, secret)
	defer controlConn.Close()
	if _, err := server.StartTunnel(ctx, tunnel.ID); err != nil {
		t.Fatalf("start tunnel: %v", err)
	}
	if startCommand, err := protocol.ReadMessage(controlConn); err != nil {
		t.Fatalf("read tunnel_start: %v", err)
	} else if startCommand.Type != protocol.TypeTunnelStart {
		t.Fatalf("start command type=%s want=%s", startCommand.Type, protocol.TypeTunnelStart)
	}

	publicConn := dialWithRetry(t, fmt.Sprintf("127.0.0.1:%d", remotePort))
	defer publicConn.Close()
	dataOpen, err := protocol.ReadMessage(controlConn)
	if err != nil {
		t.Fatalf("read data_open: %v", err)
	}
	clientDone := make(chan error, 1)
	go runProtocolDataClient(t, dataOpen, dataPort, secret, localAddr, clientDone)

	if _, err := publicConn.Write([]byte("natt e2e\n")); err != nil {
		t.Fatalf("write public connection: %v", err)
	}
	_ = publicConn.SetReadDeadline(time.Now().Add(2 * time.Second))
	got, err := bufio.NewReader(publicConn).ReadString('\n')
	if err != nil {
		t.Fatalf("read public echo: %v", err)
	}
	if got != "natt e2e\n" {
		t.Fatalf("echo response=%q want natt e2e", got)
	}
	_ = publicConn.Close()
	select {
	case err := <-clientDone:
		if err != nil {
			t.Fatalf("protocol data client failed: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for protocol data client")
	}
	waitForTrafficStat(t, database, tunnel.ID, 1, 0)

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

func TestEndToEndMultipleClientsAndTunnelsRunConcurrently(t *testing.T) {
	ctx := context.Background()
	database, err := db.Open(ctx, filepath.Join(t.TempDir(), "test.db"), nil)
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	defer database.Close()

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

	first := setupConcurrentTunnel(t, ctx, database, server, controlPort, "client-a", "secret-a", "alpha\n")
	defer first.stopEcho()
	defer first.controlConn.Close()
	second := setupConcurrentTunnel(t, ctx, database, server, controlPort, "client-b", "secret-b", "beta\n")
	defer second.stopEcho()
	defer second.controlConn.Close()

	firstFlow := runConcurrentPublicFlow(t, first, dataPort)
	secondFlow := runConcurrentPublicFlow(t, second, dataPort)
	waitForConcurrentPublicFlow(t, first, firstFlow)
	waitForConcurrentPublicFlow(t, second, secondFlow)

	waitForTrafficStat(t, database, first.tunnel.ID, 1, 0)
	waitForTrafficStat(t, database, second.tunnel.ID, 1, 0)

	if _, err := server.StopTunnel(ctx, first.tunnel.ID); err != nil {
		t.Fatalf("stop first tunnel: %v", err)
	}
	if _, err := server.StopTunnel(ctx, second.tunnel.ID); err != nil {
		t.Fatalf("stop second tunnel: %v", err)
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

type concurrentTunnelFixture struct {
	client      model.Client
	tunnel      model.Tunnel
	secret      string
	localAddr   string
	remotePort  int
	message     string
	controlConn net.Conn
	stopEcho    func()
}

func setupConcurrentTunnel(t *testing.T, ctx context.Context, database *sql.DB, server *Server, controlPort int, clientName string, secret string, message string) concurrentTunnelFixture {
	t.Helper()
	secretHash, err := auth.HashPassword(secret)
	if err != nil {
		t.Fatalf("hash client secret: %v", err)
	}
	client, err := db.CreateClient(ctx, database, db.CreateClientParams{
		Name:       clientName,
		SecretHash: secretHash,
		SecretHint: auth.SecretHint(secret),
	})
	if err != nil {
		t.Fatalf("create client %s: %v", clientName, err)
	}
	localAddr, stopEcho := startEchoServer(t)
	localHost, localPort := splitAddr(t, localAddr)
	remotePort := freeTCPPort(t)
	tunnel, err := db.CreateTunnel(ctx, database, db.CreateTunnelParams{
		Name:       clientName + "-echo",
		ClientID:   client.ID,
		Protocol:   model.TunnelProtocolTCP,
		LocalHost:  localHost,
		LocalPort:  localPort,
		RemoteHost: "127.0.0.1",
		RemotePort: remotePort,
	})
	if err != nil {
		t.Fatalf("create tunnel for %s: %v", clientName, err)
	}
	markTunnelConnectable(t, ctx, database, tunnel.ID)
	controlConn := authenticateFakeClient(t, controlPort, secret)
	if _, err := server.StartTunnel(ctx, tunnel.ID); err != nil {
		t.Fatalf("start tunnel for %s: %v", clientName, err)
	}
	if startCommand, err := protocol.ReadMessage(controlConn); err != nil {
		t.Fatalf("read tunnel_start for %s: %v", clientName, err)
	} else if startCommand.Type != protocol.TypeTunnelStart || startCommand.TunnelID != tunnel.ID {
		t.Fatalf("unexpected tunnel_start for %s: %+v", clientName, startCommand)
	}
	return concurrentTunnelFixture{
		client:      client,
		tunnel:      tunnel,
		secret:      secret,
		localAddr:   localAddr,
		remotePort:  remotePort,
		message:     message,
		controlConn: controlConn,
		stopEcho:    stopEcho,
	}
}

func runConcurrentPublicFlow(t *testing.T, fixture concurrentTunnelFixture, dataPort int) <-chan error {
	t.Helper()
	done := make(chan error, 1)
	go func() {
		publicConn := dialWithRetry(t, fmt.Sprintf("127.0.0.1:%d", fixture.remotePort))
		defer publicConn.Close()
		dataOpen, err := protocol.ReadMessage(fixture.controlConn)
		if err != nil {
			done <- fmt.Errorf("read data_open for client_id=%d: %w", fixture.client.ID, err)
			return
		}
		if dataOpen.ClientID != fixture.client.ID || dataOpen.TunnelID != fixture.tunnel.ID {
			done <- fmt.Errorf("unexpected data_open for client_id=%d: %+v", fixture.client.ID, dataOpen)
			return
		}
		clientDone := make(chan error, 1)
		go runProtocolDataClient(t, dataOpen, dataPort, fixture.secret, fixture.localAddr, clientDone)

		if _, err := publicConn.Write([]byte(fixture.message)); err != nil {
			done <- fmt.Errorf("write public connection for client_id=%d: %w", fixture.client.ID, err)
			return
		}
		_ = publicConn.SetReadDeadline(time.Now().Add(2 * time.Second))
		got, err := bufio.NewReader(publicConn).ReadString('\n')
		if err != nil {
			done <- fmt.Errorf("read public echo for client_id=%d: %w", fixture.client.ID, err)
			return
		}
		if got != fixture.message {
			done <- fmt.Errorf("echo response for client_id=%d = %q want %q", fixture.client.ID, got, fixture.message)
			return
		}
		_ = publicConn.Close()
		select {
		case err := <-clientDone:
			done <- err
		case <-time.After(2 * time.Second):
			done <- fmt.Errorf("timed out waiting for data client client_id=%d", fixture.client.ID)
		}
	}()
	return done
}

func waitForConcurrentPublicFlow(t *testing.T, fixture concurrentTunnelFixture, done <-chan error) {
	t.Helper()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatalf("timed out waiting for public flow client_id=%d", fixture.client.ID)
	}
}

func runProtocolDataClient(t *testing.T, dataOpen protocol.Message, dataPort int, secret string, localAddr string, done chan<- error) {
	t.Helper()
	dataConn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", dataPort), 2*time.Second)
	if err != nil {
		done <- fmt.Errorf("dial data server: %w", err)
		return
	}
	defer dataConn.Close()

	bind, err := protocol.NewMessage(protocol.TypeDataBind, dataOpen.ClientID, dataOpen.TunnelID, dataOpen.ConnectionID, protocol.DataBind{
		ClientSecret: secret,
	})
	if err != nil {
		done <- err
		return
	}
	if err := protocol.WriteMessage(dataConn, bind); err != nil {
		done <- fmt.Errorf("write data bind: %w", err)
		return
	}
	localConn, err := net.DialTimeout("tcp", localAddr, 2*time.Second)
	if err != nil {
		done <- fmt.Errorf("dial local echo: %w", err)
		return
	}
	defer localConn.Close()
	proxyRawTCP(localConn, dataConn)
	done <- nil
}

func startEchoServer(t *testing.T) (addr string, stop func()) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen echo server: %v", err)
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

func splitAddr(t *testing.T, addr string) (string, int) {
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

func waitForTrafficStat(t *testing.T, database *sql.DB, tunnelID int64, connectionCount int64, activeConnections int64) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		var gotConnectionCount int64
		var gotActiveConnections int64
		var bytesIn int64
		var bytesOut int64
		if err := database.QueryRowContext(context.Background(), `
SELECT connection_count, active_connections, bytes_in, bytes_out
FROM traffic_stats
WHERE tunnel_id = ?;`, tunnelID).Scan(&gotConnectionCount, &gotActiveConnections, &bytesIn, &bytesOut); err != nil {
			t.Fatalf("query traffic stats: %v", err)
		}
		if gotConnectionCount == connectionCount && gotActiveConnections == activeConnections && bytesIn > 0 && bytesOut > 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("traffic stats did not reach connection_count=%d active_connections=%d", connectionCount, activeConnections)
}
