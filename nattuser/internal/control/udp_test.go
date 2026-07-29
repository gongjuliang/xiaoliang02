package control

import (
	"net"
	"testing"

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
