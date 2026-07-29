package control

import (
	"context"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"

	"nattserver/internal/db"
	"nattserver/internal/model"
	"nattserver/internal/protocol"
)

const maxUDPPacketSize = 65507

type activeUDPTunnel struct {
	tunnel    model.Tunnel
	listeners []*net.UDPConn
	stopCh    chan struct{}
	stopOnce  sync.Once

	mu          sync.Mutex
	sessions    map[string]*udpServerSession
	sessionIDs  map[string]*udpServerSession
	relay       *udpServerRelay
	maxSessions int
}

type udpServerSession struct {
	id       string
	key      string
	listener *net.UDPConn
	remote   *net.UDPAddr
	lastSeen time.Time
}

type udpServerRelay struct {
	conn      net.Conn
	writeMu   sync.Mutex
	closeOnce sync.Once
}

func (s *Server) startUDPTunnel(ctx context.Context, tunnel model.Tunnel) (model.Tunnel, error) {
	s.udpMu.Lock()
	if s.udpTunnels[tunnel.ID] != nil {
		s.udpMu.Unlock()
		return db.SetTunnelStatus(ctx, s.database, tunnel.ID, model.TunnelStatusRunning, "")
	}
	s.udpMu.Unlock()
	if !s.hasActiveClient(tunnel.ID) {
		return db.SetTunnelWaiting(ctx, s.database, tunnel.ID)
	}

	listeners, ipv6Err, err := listenUDPDual(tunnel.RemoteHost, s.tunnelIPv6Host, tunnel.RemotePort)
	if err != nil {
		return s.markTunnelError(ctx, tunnel.ID, fmt.Errorf("监听公网UDP端口失败: %w", err))
	}
	if ipv6Err != nil {
		s.logError("IPv6公网UDP监听不可用，已继续使用IPv4 tunnel_id=%d: %v", tunnel.ID, ipv6Err)
	}
	active := &activeUDPTunnel{
		tunnel:      tunnel,
		listeners:   listeners,
		stopCh:      make(chan struct{}),
		sessions:    make(map[string]*udpServerSession),
		sessionIDs:  make(map[string]*udpServerSession),
		maxSessions: s.udpMaxSessions,
	}

	s.udpMu.Lock()
	if s.udpTunnels[tunnel.ID] != nil {
		s.udpMu.Unlock()
		active.stop()
		return db.SetTunnelStatus(ctx, s.database, tunnel.ID, model.TunnelStatusRunning, "")
	}
	s.udpTunnels[tunnel.ID] = active
	s.udpMu.Unlock()

	startCommand, err := protocol.NewMessage(protocol.TypeTunnelStart, tunnel.ClientID, tunnel.ID, "", tunnelPayload(tunnel))
	if err != nil {
		s.removeActiveUDPTunnel(tunnel.ID)
		active.stop()
		return s.markTunnelError(ctx, tunnel.ID, err)
	}
	if err := s.SendToClient(tunnel.ID, startCommand); err != nil {
		s.removeActiveUDPTunnel(tunnel.ID)
		active.stop()
		s.closeActiveClient(tunnel.ID)
		return db.SetTunnelWaiting(ctx, s.database, tunnel.ID)
	}

	for _, listener := range listeners {
		go s.receiveUDPPackets(active, listener)
	}
	go s.expireUDPSessions(active)
	return db.SetTunnelStatus(ctx, s.database, tunnel.ID, model.TunnelStatusRunning, "")
}

func (s *Server) receiveUDPPackets(active *activeUDPTunnel, listener *net.UDPConn) {
	buffer := make([]byte, maxUDPPacketSize)
	for {
		n, remote, err := listener.ReadFromUDP(buffer)
		if err != nil {
			select {
			case <-active.stopCh:
				return
			default:
				s.logError("读取公网UDP数据失败 tunnel_id=%d: %v", active.tunnel.ID, err)
				continue
			}
		}
		if n == 0 {
			continue
		}
		payload := append([]byte(nil), buffer[:n]...)
		session, relay, available := active.touchSession(listener, remote)
		if !available {
			s.logError("UDP会话数达到上限 tunnel_id=%d", active.tunnel.ID)
			continue
		}
		if relay == nil {
			continue
		}
		message, err := protocol.NewMessage(protocol.TypeUDPPacket, 0, active.tunnel.ID, session.id, protocol.UDPPacket{Data: payload})
		if err != nil {
			s.logError("构建UDP数据帧失败 tunnel_id=%d: %v", active.tunnel.ID, err)
			continue
		}
		if err := relay.write(message); err != nil {
			active.clearRelay(relay)
			s.recordUDPRelayOffline(active.tunnel.ID)
			continue
		}
		s.traffic.recordTrafficDelta(active.tunnel.ID, int64(n), 0)
	}
}

func (s *Server) expireUDPSessions(active *activeUDPTunnel) {
	interval := s.udpSessionIdle / 2
	if interval < 5*time.Second {
		interval = 5 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-active.stopCh:
			return
		case now := <-ticker.C:
			active.expireSessions(now.Add(-s.udpSessionIdle))
		}
	}
}

func (s *Server) handleUDPRelay(ctx context.Context, conn net.Conn, first protocol.Message) {
	bind, err := protocol.DecodePayload[protocol.UDPBind](first)
	if err != nil || strings.TrimSpace(bind.ClientSecret) == "" {
		_ = writeWithDeadline(conn, protocol.NewErrorMessage(first.RequestID, protocol.CodeBadRequest, "udp_bind 参数不正确"))
		return
	}
	key, err := db.AuthenticateTunnelSecret(ctx, s.database, bind.ClientSecret)
	if err != nil {
		_ = writeWithDeadline(conn, protocol.NewErrorMessage(first.RequestID, protocol.CodeUnauthorized, "秘钥错误"))
		return
	}
	tunnel, err := db.GetTunnelByID(ctx, s.database, key.TunnelID)
	if err != nil || tunnel.Protocol != model.TunnelProtocolUDP || first.TunnelID != key.TunnelID {
		_ = writeWithDeadline(conn, protocol.NewErrorMessage(first.RequestID, protocol.CodeBadRequest, "隧道协议不匹配"))
		return
	}
	active := s.getActiveUDPTunnel(key.TunnelID)
	if active == nil {
		_ = writeWithDeadline(conn, protocol.NewErrorMessage(first.RequestID, protocol.CodeConflict, "UDP隧道未启动"))
		return
	}
	relay := &udpServerRelay{conn: conn}
	previous := active.setRelay(relay)
	if previous != nil {
		previous.close()
	}
	ack, err := protocol.NewMessage(protocol.TypeUDPBindAck, 0, tunnel.ID, "", struct{}{})
	if err != nil {
		active.clearRelay(relay)
		relay.close()
		return
	}
	if err := relay.write(ack); err != nil {
		active.clearRelay(relay)
		relay.close()
		return
	}
	_, _ = db.SetTunnelStatus(ctx, s.database, tunnel.ID, model.TunnelStatusRunning, "")
	defer func() {
		if active.clearRelay(relay) {
			s.recordUDPRelayOffline(tunnel.ID)
		}
		relay.close()
	}()

	for {
		message, readErr := protocol.ReadMessage(conn)
		if readErr != nil {
			return
		}
		if message.Type != protocol.TypeUDPPacket || message.TunnelID != tunnel.ID || strings.TrimSpace(message.ConnectionID) == "" {
			_ = writeWithDeadline(conn, protocol.NewErrorMessage(message.RequestID, protocol.CodeBadRequest, "UDP数据帧不正确"))
			return
		}
		packet, decodeErr := protocol.DecodePayload[protocol.UDPPacket](message)
		if decodeErr != nil || len(packet.Data) == 0 || len(packet.Data) > maxUDPPacketSize {
			_ = writeWithDeadline(conn, protocol.NewErrorMessage(message.RequestID, protocol.CodeBadRequest, "UDP数据包不正确"))
			continue
		}
		if err := active.reply(message.ConnectionID, packet.Data); err != nil {
			continue
		}
		s.traffic.recordTrafficDelta(tunnel.ID, 0, int64(len(packet.Data)))
	}
}

func (s *Server) recordUDPRelayOffline(tunnelID int64) {
	if s.getActiveUDPTunnel(tunnelID) == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, err := db.SetTunnelStatus(ctx, s.database, tunnelID, model.TunnelStatusError, "UDP中继连接已离线"); err != nil {
		s.logError("记录UDP中继离线失败 tunnel_id=%d: %v", tunnelID, err)
	}
}

func (s *Server) getActiveUDPTunnel(id int64) *activeUDPTunnel {
	s.udpMu.Lock()
	defer s.udpMu.Unlock()
	return s.udpTunnels[id]
}

func (s *Server) removeActiveUDPTunnel(id int64) *activeUDPTunnel {
	s.udpMu.Lock()
	defer s.udpMu.Unlock()
	active := s.udpTunnels[id]
	delete(s.udpTunnels, id)
	return active
}

func (s *Server) stopAllUDPTunnels() {
	s.udpMu.Lock()
	active := make([]*activeUDPTunnel, 0, len(s.udpTunnels))
	for _, tunnel := range s.udpTunnels {
		active = append(active, tunnel)
	}
	s.udpTunnels = make(map[int64]*activeUDPTunnel)
	s.udpMu.Unlock()
	for _, tunnel := range active {
		tunnel.stop()
	}
}

func (t *activeUDPTunnel) touchSession(listener *net.UDPConn, remote *net.UDPAddr) (*udpServerSession, *udpServerRelay, bool) {
	key := listener.LocalAddr().Network() + "|" + remote.String()
	now := time.Now()
	t.mu.Lock()
	defer t.mu.Unlock()
	if session := t.sessions[key]; session != nil {
		session.lastSeen = now
		return session, t.relay, true
	}
	if len(t.sessions) >= t.maxSessions {
		return nil, nil, false
	}
	session := &udpServerSession{
		id:       protocol.NewRequestID(),
		key:      key,
		listener: listener,
		remote:   cloneUDPAddr(remote),
		lastSeen: now,
	}
	t.sessions[key] = session
	t.sessionIDs[session.id] = session
	return session, t.relay, true
}

func (t *activeUDPTunnel) expireSessions(before time.Time) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for key, session := range t.sessions {
		if session.lastSeen.Before(before) {
			delete(t.sessions, key)
			delete(t.sessionIDs, session.id)
		}
	}
}

func (t *activeUDPTunnel) setRelay(relay *udpServerRelay) *udpServerRelay {
	t.mu.Lock()
	defer t.mu.Unlock()
	previous := t.relay
	t.relay = relay
	return previous
}

func (t *activeUDPTunnel) clearRelay(relay *udpServerRelay) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.relay != relay {
		return false
	}
	t.relay = nil
	return true
}

func (t *activeUDPTunnel) reply(sessionID string, payload []byte) error {
	t.mu.Lock()
	session := t.sessionIDs[sessionID]
	if session != nil {
		session.lastSeen = time.Now()
	}
	t.mu.Unlock()
	if session == nil {
		return fmt.Errorf("UDP会话不存在")
	}
	_, err := session.listener.WriteToUDP(payload, session.remote)
	return err
}

func (t *activeUDPTunnel) stop() {
	t.stopOnce.Do(func() {
		close(t.stopCh)
		closeUDPListeners(t.listeners)
		t.mu.Lock()
		relay := t.relay
		t.relay = nil
		t.sessions = make(map[string]*udpServerSession)
		t.sessionIDs = make(map[string]*udpServerSession)
		t.mu.Unlock()
		if relay != nil {
			relay.close()
		}
	})
}

func (r *udpServerRelay) write(message protocol.Message) error {
	r.writeMu.Lock()
	defer r.writeMu.Unlock()
	return writeWithDeadline(r.conn, message)
}

func (r *udpServerRelay) close() {
	r.closeOnce.Do(func() {
		_ = r.conn.Close()
	})
}

func cloneUDPAddr(value *net.UDPAddr) *net.UDPAddr {
	if value == nil {
		return nil
	}
	return &net.UDPAddr{IP: append(net.IP(nil), value.IP...), Port: value.Port, Zone: value.Zone}
}
