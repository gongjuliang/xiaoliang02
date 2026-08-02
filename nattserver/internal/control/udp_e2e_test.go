package control

import (
	"context"
	"database/sql"
	"fmt"
	"net"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"nattserver/internal/auth"
	"nattserver/internal/config"
	"nattserver/internal/db"
	"nattserver/internal/model"
	"nattserver/internal/protocol"
)

func TestEndToEndUDPPublicForwardingThroughRelay(t *testing.T) {
	ctx := context.Background()
	database, err := db.Open(ctx, filepath.Join(t.TempDir(), "udp-e2e.db"), nil)
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	defer database.Close()

	const secret = "xiaoliang_udp_e2e_secret"
	secretHash, err := auth.HashPassword(secret)
	if err != nil {
		t.Fatalf("hash secret: %v", err)
	}
	remotePort := freeUDPPort(t)
	tunnel, err := db.CreateTunnel(ctx, database, db.CreateTunnelParams{
		Name:       "udp-e2e",
		Protocol:   model.TunnelProtocolUDP,
		RemoteHost: "127.0.0.1",
		RemotePort: remotePort,
		AutoStart:  false,
	})
	if err != nil {
		t.Fatalf("create UDP tunnel: %v", err)
	}
	if _, err := db.CreateTunnelKey(ctx, database, db.CreateTunnelKeyParams{
		TunnelID: tunnel.ID, SecretHash: secretHash, SecretHint: auth.SecretHint(secret), SecretPlain: secret,
	}); err != nil {
		t.Fatalf("create tunnel key: %v", err)
	}
	markTunnelConnectable(t, ctx, database, tunnel.ID)

	controlPort := freeTCPPort(t)
	dataPort := freeTCPPort(t)
	server := NewServer(config.ProtocolConfig{
		ControlHost: "127.0.0.1", ControlPort: controlPort,
		DataHost: "127.0.0.1", DataPort: dataPort,
	}, database, nil)
	runCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runDone := make(chan error, 1)
	go func() { runDone <- server.Run(runCtx) }()

	controlConn := authenticateFakeClient(t, controlPort, secret)
	defer controlConn.Close()
	if _, err := server.StartTunnel(ctx, tunnel.ID); err != nil {
		t.Fatalf("start UDP tunnel: %v", err)
	}
	startCommand, err := protocol.ReadMessage(controlConn)
	if err != nil {
		t.Fatalf("read tunnel_start: %v", err)
	}
	if startCommand.Type != protocol.TypeTunnelStart || startCommand.TunnelID != tunnel.ID {
		t.Fatalf("unexpected start command: %+v", startCommand)
	}

	localAddr, stopEcho := startUDPEchoServer(t)
	defer stopEcho()
	stopRelay := startProtocolUDPRelay(t, dataPort, tunnel.ID, secret, localAddr)
	defer stopRelay()

	publicAddr, err := net.ResolveUDPAddr("udp4", fmt.Sprintf("127.0.0.1:%d", remotePort))
	if err != nil {
		t.Fatalf("resolve public UDP address: %v", err)
	}
	publicConn, err := net.DialUDP("udp4", nil, publicAddr)
	if err != nil {
		t.Fatalf("dial public UDP address: %v", err)
	}
	defer publicConn.Close()
	message := []byte("udp relay payload")
	if _, err := publicConn.Write(message); err != nil {
		t.Fatalf("write public UDP packet: %v", err)
	}
	_ = publicConn.SetReadDeadline(time.Now().Add(3 * time.Second))
	buffer := make([]byte, 1024)
	n, err := publicConn.Read(buffer)
	if err != nil {
		t.Fatalf("read public UDP reply: %v", err)
	}
	if got := string(buffer[:n]); got != string(message) {
		t.Fatalf("UDP reply=%q want=%q", got, message)
	}

	if _, err := server.StopTunnel(ctx, tunnel.ID); err != nil {
		t.Fatalf("stop UDP tunnel: %v", err)
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

func TestConcurrentStartUDPTunnelDoesNotConflict(t *testing.T) {
	ctx := context.Background()
	database, err := db.Open(ctx, filepath.Join(t.TempDir(), "udp-start.db"), nil)
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	defer database.Close()

	const secret = "xiaoliang_udp_start_secret"
	secretHash, err := auth.HashPassword(secret)
	if err != nil {
		t.Fatalf("hash secret: %v", err)
	}
	tunnel, err := db.CreateTunnel(ctx, database, db.CreateTunnelParams{
		Name:       "udp-concurrent-start",
		Protocol:   model.TunnelProtocolUDP,
		RemoteHost: "127.0.0.1",
		RemotePort: freeUDPPort(t),
		AutoStart:  false,
	})
	if err != nil {
		t.Fatalf("create UDP tunnel: %v", err)
	}
	if _, err := db.CreateTunnelKey(ctx, database, db.CreateTunnelKeyParams{
		TunnelID: tunnel.ID, SecretHash: secretHash, SecretHint: auth.SecretHint(secret), SecretPlain: secret,
	}); err != nil {
		t.Fatalf("create tunnel key: %v", err)
	}
	markTunnelConnectable(t, ctx, database, tunnel.ID)

	controlPort := freeTCPPort(t)
	dataPort := freeTCPPort(t)
	server := NewServer(config.ProtocolConfig{
		ControlHost: "127.0.0.1", ControlPort: controlPort,
		DataHost: "127.0.0.1", DataPort: dataPort,
	}, database, nil)
	runCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runDone := make(chan error, 1)
	go func() { runDone <- server.Run(runCtx) }()

	controlConn := authenticateFakeClient(t, controlPort, secret)
	defer controlConn.Close()

	const starters = 4
	ready := make(chan struct{})
	errs := make(chan error, starters)
	var workers sync.WaitGroup
	for range starters {
		workers.Add(1)
		go func() {
			defer workers.Done()
			<-ready
			_, err := server.StartTunnel(ctx, tunnel.ID)
			errs <- err
		}()
	}
	close(ready)
	workers.Wait()
	close(errs)
	for startErr := range errs {
		if startErr != nil {
			t.Fatalf("concurrent start tunnel: %v", startErr)
		}
	}
	if _, err := protocol.ReadMessage(controlConn); err != nil {
		t.Fatalf("read tunnel_start: %v", err)
	}
	if active := server.getActiveUDPTunnel(tunnel.ID); active == nil {
		t.Fatal("UDP tunnel was not active after concurrent starts")
	}

	_ = controlConn.Close()
	waitForTunnelKeyOnlineStatus(t, database, tunnel.ID, model.OnlineStatusOffline)
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

func waitForTunnelKeyOnlineStatus(t *testing.T, database *sql.DB, tunnelID int64, want model.OnlineStatus) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		key, err := db.GetTunnelKeyByTunnelID(context.Background(), database, tunnelID)
		if err != nil {
			t.Fatalf("load tunnel key: %v", err)
		}
		if key.OnlineStatus == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	key, err := db.GetTunnelKeyByTunnelID(context.Background(), database, tunnelID)
	if err != nil {
		t.Fatalf("load tunnel key: %v", err)
	}
	t.Fatalf("tunnel key online status=%s want=%s", key.OnlineStatus, want)
}

func freeUDPPort(t *testing.T) int {
	t.Helper()
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 0})
	if err != nil {
		t.Fatalf("listen free UDP port: %v", err)
	}
	defer conn.Close()
	return conn.LocalAddr().(*net.UDPAddr).Port
}

func startUDPEchoServer(t *testing.T) (string, func()) {
	t.Helper()
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 0})
	if err != nil {
		t.Fatalf("listen UDP echo: %v", err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		buffer := make([]byte, maxUDPPacketSize)
		for {
			n, remote, err := conn.ReadFromUDP(buffer)
			if err != nil {
				return
			}
			_, _ = conn.WriteToUDP(buffer[:n], remote)
		}
	}()
	return conn.LocalAddr().String(), func() {
		_ = conn.Close()
		<-done
	}
}

func startProtocolUDPRelay(t *testing.T, dataPort int, tunnelID int64, secret string, localAddr string) func() {
	t.Helper()
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", dataPort), 2*time.Second)
	if err != nil {
		t.Fatalf("dial UDP relay data channel: %v", err)
	}
	bind, err := protocol.NewMessage(protocol.TypeUDPBind, 0, tunnelID, "", protocol.UDPBind{ClientSecret: secret})
	if err != nil {
		_ = conn.Close()
		t.Fatalf("build UDP bind: %v", err)
	}
	if err := protocol.WriteMessage(conn, bind); err != nil {
		_ = conn.Close()
		t.Fatalf("send UDP bind: %v", err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(1 * time.Second))
	bindAck, err := protocol.ReadMessage(conn)
	_ = conn.SetReadDeadline(time.Time{})
	if err != nil {
		_ = conn.Close()
		t.Fatalf("read UDP bind acknowledgement: %v", err)
	}
	if bindAck.Type != "udp_bind_ack" || bindAck.TunnelID != tunnelID {
		_ = conn.Close()
		t.Fatalf("unexpected UDP bind acknowledgement: %+v", bindAck)
	}
	localConn, err := net.Dial("udp4", localAddr)
	if err != nil {
		_ = conn.Close()
		t.Fatalf("dial local UDP echo: %v", err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer localConn.Close()
		buffer := make([]byte, maxUDPPacketSize)
		for {
			message, err := protocol.ReadMessage(conn)
			if err != nil {
				return
			}
			if message.Type != protocol.TypeUDPPacket {
				return
			}
			packet, err := protocol.DecodePayload[protocol.UDPPacket](message)
			if err != nil {
				return
			}
			if _, err := localConn.Write(packet.Data); err != nil {
				return
			}
			_ = localConn.SetReadDeadline(time.Now().Add(2 * time.Second))
			n, err := localConn.Read(buffer)
			if err != nil {
				return
			}
			reply, err := protocol.NewMessage(protocol.TypeUDPPacket, 0, tunnelID, message.ConnectionID, protocol.UDPPacket{Data: append([]byte(nil), buffer[:n]...)})
			if err != nil || protocol.WriteMessage(conn, reply) != nil {
				return
			}
		}
	}()
	return func() {
		_ = conn.Close()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatal("timed out waiting for UDP relay shutdown")
		}
	}
}
