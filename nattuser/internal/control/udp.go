package control

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"nattuser/internal/model"
	"nattuser/internal/protocol"
)

const (
	maxUDPPacketSize       = 65507
	udpSessionIdle         = 60 * time.Second
	udpMaxSessionsPerRelay = 1024
)

type activeUDPRelay struct {
	manager            *Manager
	serverConnectionID int64
	tunnelID           int64
	cancel             context.CancelFunc

	mu        sync.Mutex
	dataConn  net.Conn
	sessions  map[string]*udpClientSession
	writeMu   sync.Mutex
	closeOnce sync.Once
}

type udpClientSession struct {
	connectionID string
	conn         *net.UDPConn
	lastSeen     time.Time
}

type tunnelCommandPayload struct {
	Protocol string `json:"protocol"`
}

func (m *Manager) startUDPRelay(parent context.Context, connection model.ServerConnection, tunnelID int64) {
	if !strings.EqualFold(connection.Protocol, "udp") || tunnelID <= 0 {
		return
	}
	m.udpMu.Lock()
	if existing := m.udpRelays[connection.ID]; existing != nil && existing.tunnelID == tunnelID {
		m.udpMu.Unlock()
		return
	}
	if existing := m.udpRelays[connection.ID]; existing != nil {
		existing.close()
	}
	ctx, cancel := context.WithCancel(parent)
	relay := &activeUDPRelay{
		manager:            m,
		serverConnectionID: connection.ID,
		tunnelID:           tunnelID,
		cancel:             cancel,
		sessions:           make(map[string]*udpClientSession),
	}
	m.udpRelays[connection.ID] = relay
	m.workers.Add(1)
	m.udpMu.Unlock()
	go func() {
		defer m.workers.Done()
		defer m.removeUDPRelay(connection.ID, relay)
		m.runUDPRelay(ctx, connection, relay)
	}()
}

func (m *Manager) runUDPRelay(ctx context.Context, connection model.ServerConnection, relay *activeUDPRelay) {
	go relay.expireSessions(ctx)
	defer relay.close()
	for {
		if ctx.Err() != nil {
			return
		}
		dataConn, err := m.dialData(ctx, connection, protocol.DataOpen{})
		if err == nil {
			bind, bindErr := protocol.NewMessage(protocol.TypeUDPBind, 0, relay.tunnelID, "", protocol.UDPBind{ClientSecret: connection.ClientSecret})
			if bindErr == nil {
				bindErr = writeWithDeadline(dataConn, bind)
			}
			if bindErr == nil {
				relay.attachDataConn(dataConn)
				bindErr = waitForUDPBindAck(dataConn, relay.tunnelID)
			}
			if bindErr == nil {
				err = m.readUDPRelay(ctx, relay, connection, dataConn)
			}
			relay.detachDataConn(dataConn)
			_ = dataConn.Close()
			if bindErr != nil {
				err = bindErr
			}
		}
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			m.logError("UDP中继连接失败 server_connection_id=%d: %v", connection.ID, err)
		}
		if !sleepContext(ctx, m.options.ReconnectInterval) {
			return
		}
	}
}

func waitForUDPBindAck(conn net.Conn, tunnelID int64) error {
	_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	defer conn.SetReadDeadline(time.Time{})

	message, err := protocol.ReadMessage(conn)
	if err != nil {
		return fmt.Errorf("等待 UDP 中继就绪确认失败: %w", err)
	}
	if message.Type == protocol.TypeError {
		protocolErr, _ := protocol.DecodePayload[protocol.ProtocolError](message)
		return fmt.Errorf("%s", localizeProtocolError(protocolErr))
	}
	if message.Type != protocol.TypeUDPBindAck || message.TunnelID != tunnelID {
		return fmt.Errorf("UDP 中继就绪确认不正确")
	}
	return nil
}

func (m *Manager) readUDPRelay(ctx context.Context, relay *activeUDPRelay, connection model.ServerConnection, dataConn net.Conn) error {
	for {
		if ctx.Err() != nil {
			return nil
		}
		message, err := protocol.ReadMessage(dataConn)
		if err != nil {
			return err
		}
		if message.Type == protocol.TypeError {
			protocolErr, _ := protocol.DecodePayload[protocol.ProtocolError](message)
			return fmt.Errorf("%s", localizeProtocolError(protocolErr))
		}
		if message.Type != protocol.TypeUDPPacket || message.TunnelID != relay.tunnelID || strings.TrimSpace(message.ConnectionID) == "" {
			return fmt.Errorf("UDP中继数据帧不正确")
		}
		packet, err := protocol.DecodePayload[protocol.UDPPacket](message)
		if err != nil || len(packet.Data) == 0 || len(packet.Data) > maxUDPPacketSize {
			return fmt.Errorf("UDP数据包不正确")
		}
		if err := m.forwardUDPPacket(ctx, relay, connection, message.ConnectionID, packet.Data); err != nil {
			m.logError("转发本地UDP数据失败 server_connection_id=%d: %v", connection.ID, err)
		}
	}
}

func (m *Manager) forwardUDPPacket(ctx context.Context, relay *activeUDPRelay, connection model.ServerConnection, sessionID string, payload []byte) error {
	session, err := relay.getOrCreateSession(ctx, connection, sessionID)
	if err != nil {
		return err
	}
	_, err = session.conn.Write(payload)
	if err == nil {
		relay.touchSession(sessionID)
	}
	return err
}

func (r *activeUDPRelay) getOrCreateSession(ctx context.Context, connection model.ServerConnection, sessionID string) (*udpClientSession, error) {
	r.mu.Lock()
	if session := r.sessions[sessionID]; session != nil {
		session.lastSeen = time.Now()
		r.mu.Unlock()
		return session, nil
	}
	if len(r.sessions) >= udpMaxSessionsPerRelay {
		r.mu.Unlock()
		return nil, fmt.Errorf("UDP会话数达到上限")
	}
	r.mu.Unlock()

	dataOpen, err := r.manager.resolveLocalDataOpen(ctx, connection, protocol.Message{TunnelID: r.tunnelID}, protocol.DataOpen{})
	if err != nil {
		return nil, err
	}
	target, err := net.ResolveUDPAddr("udp", net.JoinHostPort(dataOpen.LocalHost, strconv.Itoa(dataOpen.LocalPort)))
	if err != nil {
		return nil, fmt.Errorf("解析本地UDP服务地址失败: %w", err)
	}
	conn, err := net.DialUDP("udp", nil, target)
	if err != nil {
		return nil, fmt.Errorf("连接本地UDP服务失败: %w", err)
	}
	session := &udpClientSession{connectionID: sessionID, conn: conn, lastSeen: time.Now()}
	r.mu.Lock()
	if existing := r.sessions[sessionID]; existing != nil {
		r.mu.Unlock()
		_ = conn.Close()
		return existing, nil
	}
	r.sessions[sessionID] = session
	r.mu.Unlock()
	go r.readLocalUDP(session)
	return session, nil
}

func (r *activeUDPRelay) readLocalUDP(session *udpClientSession) {
	buffer := make([]byte, maxUDPPacketSize)
	for {
		n, err := session.conn.Read(buffer)
		if err != nil {
			return
		}
		if n == 0 {
			continue
		}
		r.touchSession(session.connectionID)
		message, err := protocol.NewMessage(protocol.TypeUDPPacket, 0, r.tunnelID, session.connectionID, protocol.UDPPacket{Data: append([]byte(nil), buffer[:n]...)})
		if err != nil {
			continue
		}
		_ = r.write(message)
	}
}

func (r *activeUDPRelay) expireSessions(ctx context.Context) {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			r.mu.Lock()
			for id, session := range r.sessions {
				if session.lastSeen.Before(now.Add(-udpSessionIdle)) {
					delete(r.sessions, id)
					_ = session.conn.Close()
				}
			}
			r.mu.Unlock()
		}
	}
}

func (r *activeUDPRelay) attachDataConn(conn net.Conn) {
	r.mu.Lock()
	r.dataConn = conn
	r.mu.Unlock()
}

func (r *activeUDPRelay) detachDataConn(conn net.Conn) {
	r.mu.Lock()
	if r.dataConn == conn {
		r.dataConn = nil
	}
	r.mu.Unlock()
}

func (r *activeUDPRelay) write(message protocol.Message) error {
	r.writeMu.Lock()
	defer r.writeMu.Unlock()
	r.mu.Lock()
	conn := r.dataConn
	r.mu.Unlock()
	if conn == nil {
		return fmt.Errorf("UDP中继未连接")
	}
	return writeWithDeadline(conn, message)
}

func (r *activeUDPRelay) touchSession(id string) {
	r.mu.Lock()
	if session := r.sessions[id]; session != nil {
		session.lastSeen = time.Now()
	}
	r.mu.Unlock()
}

func (r *activeUDPRelay) close() {
	r.closeOnce.Do(func() {
		r.cancel()
		r.mu.Lock()
		if r.dataConn != nil {
			_ = r.dataConn.Close()
			r.dataConn = nil
		}
		for id, session := range r.sessions {
			_ = session.conn.Close()
			delete(r.sessions, id)
		}
		r.mu.Unlock()
	})
}

func (m *Manager) stopUDPRelay(serverConnectionID int64) {
	m.udpMu.Lock()
	relay := m.udpRelays[serverConnectionID]
	delete(m.udpRelays, serverConnectionID)
	m.udpMu.Unlock()
	if relay != nil {
		relay.close()
	}
}

func (m *Manager) removeUDPRelay(serverConnectionID int64, expected *activeUDPRelay) {
	m.udpMu.Lock()
	if m.udpRelays[serverConnectionID] == expected {
		delete(m.udpRelays, serverConnectionID)
	}
	m.udpMu.Unlock()
}

func (m *Manager) stopAllUDPRelays() {
	m.udpMu.Lock()
	relays := make([]*activeUDPRelay, 0, len(m.udpRelays))
	for _, relay := range m.udpRelays {
		relays = append(relays, relay)
	}
	m.udpRelays = make(map[int64]*activeUDPRelay)
	m.udpMu.Unlock()
	for _, relay := range relays {
		relay.close()
	}
}
