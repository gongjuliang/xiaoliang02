package control

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"testing"
	"time"

	"nattuser/internal/config"
	"nattuser/internal/model"
	"nattuser/internal/protocol"
)

func TestWaitForUDPBindAckAcceptsMatchingTunnel(t *testing.T) {
	serverConn, clientConn := net.Pipe()
	defer serverConn.Close()
	defer clientConn.Close()

	go func() {
		message, err := protocol.NewMessage("udp_bind_ack", 0, 42, "", struct{}{})
		if err == nil {
			_ = protocol.WriteMessage(serverConn, message)
		}
	}()

	if err := waitForUDPBindAck(clientConn, 42); err != nil {
		t.Fatalf("wait for UDP bind acknowledgement: %v", err)
	}
}

func TestUDPRelayIsIdempotentAndReconnectsDataChannel(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen fake data server: %v", err)
	}
	defer listener.Close()
	port := listener.Addr().(*net.TCPAddr).Port

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	binds := make(chan struct{}, 2)
	serverDone := make(chan error, 1)
	go serveUDPRelayReconnects(ctx, listener, 42, binds, serverDone)

	manager := NewManagerWithOptions(config.Default(), nil, nil, Options{
		ReconnectInterval: 10 * time.Millisecond,
		DialTimeout:       200 * time.Millisecond,
	})
	connection := model.ServerConnection{
		ID:           7,
		Protocol:     "udp",
		ServerHost:   "127.0.0.1",
		DataPort:     port,
		ClientSecret: "xiaoliang_udp_reconnect_secret",
	}
	manager.startUDPRelay(ctx, connection, 42)
	manager.startUDPRelay(ctx, connection, 42)

	for attempt := 0; attempt < 2; attempt++ {
		select {
		case <-binds:
		case <-time.After(2 * time.Second):
			t.Fatalf("timed out waiting for UDP bind attempt %d", attempt+1)
		}
	}
	manager.udpMu.Lock()
	relayCount := len(manager.udpRelays)
	manager.udpMu.Unlock()
	if relayCount != 1 {
		t.Fatalf("UDP relay count=%d want 1", relayCount)
	}

	cancel()
	manager.stopAllUDPRelays()
	manager.workers.Wait()
	select {
	case err := <-serverDone:
		if err != nil {
			t.Fatalf("fake UDP relay server: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for fake UDP relay server")
	}
}

func serveUDPRelayReconnects(ctx context.Context, listener net.Listener, tunnelID int64, binds chan<- struct{}, done chan<- error) {
	defer close(done)
	for attempt := 0; attempt < 2; attempt++ {
		conn, err := listener.Accept()
		if err != nil {
			done <- err
			return
		}
		message, err := protocol.ReadMessage(conn)
		if err != nil {
			_ = conn.Close()
			done <- fmt.Errorf("read UDP bind attempt %d: %w", attempt+1, err)
			return
		}
		if message.Type != protocol.TypeUDPBind || message.TunnelID != tunnelID {
			_ = conn.Close()
			done <- fmt.Errorf("unexpected UDP bind attempt %d: %+v", attempt+1, message)
			return
		}
		bind, err := protocol.DecodePayload[protocol.UDPBind](message)
		if err != nil || bind.ClientSecret != "xiaoliang_udp_reconnect_secret" {
			_ = conn.Close()
			done <- fmt.Errorf("invalid UDP bind attempt %d", attempt+1)
			return
		}
		ack, err := protocol.NewMessage(protocol.TypeUDPBindAck, 0, tunnelID, strconv.Itoa(attempt), struct{}{})
		if err != nil || protocol.WriteMessage(conn, ack) != nil {
			_ = conn.Close()
			done <- fmt.Errorf("write UDP bind ack attempt %d", attempt+1)
			return
		}
		binds <- struct{}{}
		if attempt == 0 {
			_ = conn.Close()
			continue
		}
		<-ctx.Done()
		_ = conn.Close()
	}
}
