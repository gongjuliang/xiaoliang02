package control

import (
	"context"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"

	"nattuser/internal/db"
	"nattuser/internal/model"
	"nattuser/internal/protocol"
)

type copyResult struct {
	direction string
	bytes     int64
}

func (m *Manager) openDataConnection(ctx context.Context, connection model.ServerConnection, writer *controlWriter, command protocol.Message, dataOpen protocol.DataOpen) {
	if err := m.forwardDataConnection(ctx, connection, writer, command, dataOpen); err != nil {
		m.logError("data connection failed server_connection_id=%d tunnel_id=%d connection_id=%s: %v", connection.ID, command.TunnelID, command.ConnectionID, err)
	}
}

func (m *Manager) forwardDataConnection(ctx context.Context, connection model.ServerConnection, writer *controlWriter, command protocol.Message, dataOpen protocol.DataOpen) error {
	if strings.TrimSpace(command.ConnectionID) == "" || command.TunnelID <= 0 {
		return fmt.Errorf("invalid data_open connection metadata")
	}
	resolvedOpen, err := m.resolveLocalDataOpen(ctx, connection, command, dataOpen)
	if err != nil {
		_ = sendDataClose(writer, command, protocol.CodeLocalServiceUnavailable, err.Error())
		return err
	}

	dataConn, err := m.dialData(ctx, connection, resolvedOpen)
	if err != nil {
		return fmt.Errorf("dial data server: %w", err)
	}
	defer dataConn.Close()

	// The data connection is a separate TCP socket. It proves ownership with the
	// client secret and binds itself to the server's pending connection_id.
	bind, err := protocol.NewMessage(protocol.TypeDataBind, command.ClientID, command.TunnelID, command.ConnectionID, protocol.DataBind{
		ClientSecret: connection.ClientSecret,
	})
	if err != nil {
		return err
	}
	if err := writeWithDeadline(dataConn, bind); err != nil {
		return fmt.Errorf("send data bind: %w", err)
	}

	session := m.registerDataConnection(connection.ID, command.ConnectionID, dataConn)
	defer m.removeDataConnection(command.ConnectionID, session)
	defer session.close()

	localConn, err := m.dialLocal(ctx, resolvedOpen)
	if err != nil {
		_ = sendDataClose(writer, command, protocol.CodeLocalServiceUnavailable, err.Error())
		return fmt.Errorf("dial local service: %w", err)
	}
	defer localConn.Close()
	session.setLocalConn(localConn)

	// From this point on the tunnel is raw TCP proxying; protocol frames stay on
	// the control socket, while this data socket carries only application bytes.
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			_ = dataConn.Close()
			_ = localConn.Close()
		case <-done:
		}
	}()

	proxyRawTCP(dataConn, localConn)
	return nil
}

func (m *Manager) resolveLocalDataOpen(ctx context.Context, connection model.ServerConnection, command protocol.Message, dataOpen protocol.DataOpen) (protocol.DataOpen, error) {
	// The service side owns only the public listener. The client deliberately
	// resolves the private target from this tunnel connection so a server cannot
	// push an arbitrary LAN address through data_open.
	if binding, err := db.GetEnabledLocalTunnelByServerTunnel(ctx, m.database, connection.ID, command.TunnelID); err == nil {
		resolved := dataOpen
		resolved.LocalHost = strings.TrimSpace(binding.LocalHost)
		resolved.LocalPort = binding.LocalPort
		return resolved, nil
	} else if err != db.ErrNotFound {
		return protocol.DataOpen{}, fmt.Errorf("load local tunnel binding: %w", err)
	}
	if strings.TrimSpace(connection.LocalHost) == "" || connection.LocalPort <= 0 {
		return protocol.DataOpen{}, fmt.Errorf("local tunnel binding is missing or disabled")
	}
	resolved := dataOpen
	resolved.LocalHost = strings.TrimSpace(connection.LocalHost)
	resolved.LocalPort = connection.LocalPort
	return resolved, nil
}

func (m *Manager) registerDataConnection(serverConnectionID int64, connectionID string, dataConn net.Conn) *activeDataConnection {
	session := &activeDataConnection{
		connectionID:       connectionID,
		serverConnectionID: serverConnectionID,
		dataConn:           dataConn,
	}
	m.dataMu.Lock()
	m.data[connectionID] = session
	m.dataMu.Unlock()
	return session
}

func (m *Manager) removeDataConnection(connectionID string, session *activeDataConnection) {
	m.dataMu.Lock()
	defer m.dataMu.Unlock()
	if m.data[connectionID] == session {
		delete(m.data, connectionID)
	}
}

func (m *Manager) closeDataConnection(connectionID string) error {
	if strings.TrimSpace(connectionID) == "" {
		return fmt.Errorf("data_close connection_id is required")
	}
	// data_close is idempotent: the server may send it while the local dial or
	// proxy goroutines are also closing the same session.
	m.dataMu.Lock()
	session := m.data[connectionID]
	if session != nil {
		delete(m.data, connectionID)
	}
	m.dataMu.Unlock()
	if session != nil {
		session.close()
	}
	return nil
}

func (m *Manager) closeDataConnectionsForServer(serverConnectionID int64) {
	var sessions []*activeDataConnection
	m.dataMu.Lock()
	for connectionID, session := range m.data {
		if session.serverConnectionID == serverConnectionID {
			sessions = append(sessions, session)
			delete(m.data, connectionID)
		}
	}
	m.dataMu.Unlock()
	for _, session := range sessions {
		session.close()
	}
}

func (c *activeDataConnection) setLocalConn(conn net.Conn) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.localConn = conn
}

func (c *activeDataConnection) close() {
	c.closeOnce.Do(func() {
		c.mu.Lock()
		defer c.mu.Unlock()
		if c.dataConn != nil {
			_ = c.dataConn.Close()
		}
		if c.localConn != nil {
			_ = c.localConn.Close()
		}
	})
}

func sendDataClose(writer *controlWriter, command protocol.Message, code string, message string) error {
	if writer == nil {
		return nil
	}
	closeMessage, err := protocol.NewMessage(protocol.TypeDataClose, command.ClientID, command.TunnelID, command.ConnectionID, protocol.DataClose{
		Code:    code,
		Message: message,
	})
	if err != nil {
		return err
	}
	return writer.write(closeMessage)
}

func (m *Manager) dialData(ctx context.Context, connection model.ServerConnection, dataOpen protocol.DataOpen) (net.Conn, error) {
	host := dataHost(dataOpen.DataHost, connection.ServerHost)
	port := dataOpen.DataPort
	if port <= 0 {
		port = connection.DataPort
	}
	addr := net.JoinHostPort(host, strconv.Itoa(port))
	dialer := &net.Dialer{Timeout: m.options.DialTimeout}
	return dialer.DialContext(ctx, "tcp", addr)
}

func (m *Manager) dialLocal(ctx context.Context, dataOpen protocol.DataOpen) (net.Conn, error) {
	addr := net.JoinHostPort(dataOpen.LocalHost, strconv.Itoa(dataOpen.LocalPort))
	dialer := &net.Dialer{Timeout: m.options.DialTimeout}
	return dialer.DialContext(ctx, "tcp", addr)
}

func dataHost(payloadHost string, serverHost string) string {
	payloadHost = strings.TrimSpace(payloadHost)
	if payloadHost == "" || payloadHost == "0.0.0.0" || payloadHost == "::" {
		return serverHost
	}
	return payloadHost
}

func proxyRawTCP(left net.Conn, right net.Conn) {
	ch := make(chan copyResult, 2)
	go copyAndCloseWrite(right, left, "left_to_right", ch)
	go copyAndCloseWrite(left, right, "right_to_left", ch)

	// Closing both sides after the first copy exits releases the peer goroutine
	// on platforms where CloseWrite is unavailable or the peer stays idle.
	<-ch
	_ = left.Close()
	_ = right.Close()
	<-ch
}

func copyAndCloseWrite(dst net.Conn, src net.Conn, direction string, ch chan<- copyResult) {
	n, _ := io.Copy(dst, src)
	closeWrite(dst)
	ch <- copyResult{direction: direction, bytes: n}
}

func closeWrite(conn net.Conn) {
	if tcp, ok := conn.(*net.TCPConn); ok {
		_ = tcp.CloseWrite()
		return
	}
	_ = conn.Close()
}
