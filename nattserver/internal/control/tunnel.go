package control

import (
	"context"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"nattserver/internal/db"
	"nattserver/internal/model"
	"nattserver/internal/protocol"
)

const dataBindTimeout = 10 * time.Second

type activeTunnel struct {
	tunnel   model.Tunnel
	listener net.Listener
	stopCh   chan struct{}
	done     chan struct{}
	stopOnce sync.Once
	mu       sync.Mutex
	conns    map[string][]net.Conn
}

type pendingDataConn struct {
	tunnelID     int64
	connectionID string
	publicConn   net.Conn
	dataConn     net.Conn
	connCh       chan net.Conn
	done         chan struct{}
	doneOnce     sync.Once
	mu           sync.Mutex
}

type tunnelCommandPayload struct {
	Name       string `json:"name"`
	Protocol   string `json:"protocol"`
	RemoteHost string `json:"remote_host"`
	RemotePort int    `json:"remote_port"`
}

type copyResult struct {
	direction string
	bytes     int64
}

func (s *Server) StartTunnel(ctx context.Context, id int64) (model.Tunnel, error) {
	tunnel, err := db.GetTunnelByID(ctx, s.database, id)
	if err != nil {
		return model.Tunnel{}, err
	}
	if tunnel.Protocol != model.TunnelProtocolTCP {
		err := fmt.Errorf("unsupported tunnel protocol: %s", tunnel.Protocol)
		return s.markTunnelError(ctx, tunnel.ID, err)
	}

	// Starting a tunnel reserves the public TCP port first, then asks the
	// authenticated client to prepare local forwarding for future data_open.
	s.tunnelMu.Lock()
	if _, ok := s.tunnels[tunnel.ID]; ok {
		s.tunnelMu.Unlock()
		return db.SetTunnelStatus(ctx, s.database, tunnel.ID, model.TunnelStatusRunning, "")
	}
	s.tunnelMu.Unlock()

	listener, err := net.Listen("tcp", net.JoinHostPort(hostOrDefault(tunnel.RemoteHost), strconv.Itoa(tunnel.RemotePort)))
	if err != nil {
		return s.markTunnelError(ctx, tunnel.ID, fmt.Errorf("listen remote port: %w", err))
	}

	active := &activeTunnel{
		tunnel:   tunnel,
		listener: listener,
		stopCh:   make(chan struct{}),
		done:     make(chan struct{}),
		conns:    make(map[string][]net.Conn),
	}

	startCommand, err := protocol.NewMessage(protocol.TypeTunnelStart, tunnel.ClientID, tunnel.ID, "", tunnelPayload(tunnel))
	if err != nil {
		_ = listener.Close()
		return s.markTunnelError(ctx, tunnel.ID, err)
	}
	if err := s.SendToClient(tunnel.ID, startCommand); err != nil {
		_ = listener.Close()
		return s.markTunnelError(ctx, tunnel.ID, err)
	}

	s.tunnelMu.Lock()
	if old := s.tunnels[tunnel.ID]; old != nil {
		s.tunnelMu.Unlock()
		active.stop()
		return db.SetTunnelStatus(ctx, s.database, tunnel.ID, model.TunnelStatusRunning, "")
	}
	s.tunnels[tunnel.ID] = active
	s.tunnelMu.Unlock()

	go s.acceptTunnel(active)
	return db.SetTunnelStatus(ctx, s.database, tunnel.ID, model.TunnelStatusRunning, "")
}

func (s *Server) StopTunnel(ctx context.Context, id int64) (model.Tunnel, error) {
	tunnel, err := db.GetTunnelByID(ctx, s.database, id)
	if err != nil {
		return model.Tunnel{}, err
	}
	if active := s.removeActiveTunnel(id); active != nil {
		// Tell the client to close any per-connection data sockets before the
		// listener is stopped, so both halves release resources promptly.
		for _, connectionID := range active.connectionIDs() {
			s.sendDataClose(tunnel.ID, connectionID, protocol.CodeOK, "tunnel stopped")
		}
		active.stop()
	}

	stopCommand, err := protocol.NewMessage(protocol.TypeTunnelStop, tunnel.ClientID, tunnel.ID, "", tunnelPayload(tunnel))
	if err == nil {
		if err := s.SendToClient(tunnel.ID, stopCommand); err != nil {
			s.logError("send tunnel_stop failed tunnel_id=%d: %v", tunnel.ID, err)
		}
	}
	return db.SetTunnelStopped(ctx, s.database, id, "")
}

func (s *Server) sendDataClose(tunnelID int64, connectionID string, code string, message string) {
	dataClose, err := protocol.NewMessage(protocol.TypeDataClose, 0, tunnelID, connectionID, protocol.DataClose{
		Code:    code,
		Message: message,
	})
	if err != nil {
		s.logError("build data_close failed tunnel_id=%d connection_id=%s: %v", tunnelID, connectionID, err)
		return
	}
	if err := s.SendToClient(tunnelID, dataClose); err != nil {
		s.logError("send data_close failed tunnel_id=%d connection_id=%s: %v", tunnelID, connectionID, err)
	}
}

func (s *Server) acceptTunnel(active *activeTunnel) {
	defer close(active.done)
	for {
		conn, err := active.listener.Accept()
		if err != nil {
			select {
			case <-active.stopCh:
				return
			default:
			}
			if s.log != nil {
				s.log.Errorf("accept public tunnel connection failed tunnel_id=%d: %v", active.tunnel.ID, err)
			}
			continue
		}
		go s.handlePublicConn(active, conn)
	}
}

func (s *Server) handlePublicConn(active *activeTunnel, publicConn net.Conn) {
	connectionID := protocol.NewRequestID()
	pending := &pendingDataConn{
		tunnelID:     active.tunnel.ID,
		connectionID: connectionID,
		publicConn:   publicConn,
		connCh:       make(chan net.Conn),
		done:         make(chan struct{}),
	}
	s.addPending(pending)
	defer func() {
		s.removePending(connectionID)
		pending.close()
	}()

	// Each external TCP connection gets a fresh connection_id. The client opens
	// a matching data socket and binds it back with data_bind before proxying.
	dataOpen, err := protocol.NewMessage(protocol.TypeDataOpen, active.tunnel.ClientID, active.tunnel.ID, connectionID, protocol.DataOpen{
		DataHost: advertisedDataHost(s.cfg.DataHost),
		DataPort: s.cfg.DataPort,
	})
	if err != nil {
		_ = publicConn.Close()
		s.recordTunnelRuntimeError(active.tunnel.ID, err)
		return
	}
	if err := s.SendToClient(active.tunnel.ID, dataOpen); err != nil {
		_ = publicConn.Close()
		s.recordTunnelRuntimeError(active.tunnel.ID, err)
		return
	}

	timer := time.NewTimer(dataBindTimeout)
	defer timer.Stop()
	var dataConn net.Conn
	select {
	case dataConn = <-pending.connCh:
	case <-timer.C:
		_ = publicConn.Close()
		s.recordTunnelRuntimeError(active.tunnel.ID, fmt.Errorf("data connection bind timeout"))
		return
	case <-active.stopCh:
		_ = publicConn.Close()
		return
	case <-pending.done:
		_ = publicConn.Close()
		return
	}

	active.addConnection(connectionID, publicConn, dataConn)
	s.traffic.recordConnectionOpen(active.tunnel.ID)
	proxyRawTCPWithRecorder(publicConn, dataConn, s.traffic, active.tunnel.ID)
	active.removeConnection(connectionID)
	s.traffic.recordConnectionClose(active.tunnel.ID)
}

func (s *Server) handleDataConn(ctx context.Context, conn net.Conn) {
	handedOff := false
	defer func() {
		if !handedOff {
			_ = conn.Close()
		}
	}()

	_ = conn.SetReadDeadline(time.Now().Add(authTimeout))
	message, err := protocol.ReadMessage(conn)
	if err != nil {
		s.logError("read data bind failed: %v", err)
		return
	}
	_ = conn.SetReadDeadline(time.Time{})
	if message.Type != protocol.TypeDataBind {
		_ = writeWithDeadline(conn, protocol.NewErrorMessage(message.RequestID, protocol.CodeBadRequest, "首条数据消息必须是 data_bind"))
		return
	}
	// A data socket is accepted only when it authenticates and matches a pending
	// connection_id/tunnel_id that was created by handlePublicConn.
	bind, err := protocol.DecodePayload[protocol.DataBind](message)
	if err != nil || strings.TrimSpace(bind.ClientSecret) == "" {
		_ = writeWithDeadline(conn, protocol.NewErrorMessage(message.RequestID, protocol.CodeBadRequest, "data_bind 参数不正确"))
		return
	}
	key, err := db.AuthenticateTunnelSecret(ctx, s.database, bind.ClientSecret)
	if err != nil {
		_ = writeWithDeadline(conn, protocol.NewErrorMessage(message.RequestID, protocol.CodeUnauthorized, "秘钥错误"))
		return
	}
	pending := s.getPending(message.ConnectionID)
	if pending == nil || pending.tunnelID != key.TunnelID || pending.tunnelID != message.TunnelID {
		_ = writeWithDeadline(conn, protocol.NewErrorMessage(message.RequestID, protocol.CodeBadRequest, "数据连接不在预期范围内"))
		return
	}

	pending.setDataConn(conn)
	select {
	case pending.connCh <- conn:
		handedOff = true
	case <-pending.done:
	}
}

func (s *Server) addPending(pending *pendingDataConn) {
	s.pendingMu.Lock()
	defer s.pendingMu.Unlock()
	s.pending[pending.connectionID] = pending
}

func (s *Server) getPending(connectionID string) *pendingDataConn {
	s.pendingMu.Lock()
	defer s.pendingMu.Unlock()
	return s.pending[connectionID]
}

func (s *Server) removePending(connectionID string) {
	s.pendingMu.Lock()
	defer s.pendingMu.Unlock()
	delete(s.pending, connectionID)
}

func (s *Server) closeDataConnection(connectionID string) bool {
	closed := false
	if pending := s.getPending(connectionID); pending != nil {
		pending.close()
		closed = true
	}

	s.tunnelMu.Lock()
	active := make([]*activeTunnel, 0, len(s.tunnels))
	for _, tunnel := range s.tunnels {
		active = append(active, tunnel)
	}
	s.tunnelMu.Unlock()
	for _, tunnel := range active {
		if tunnel.closeConnection(connectionID) {
			closed = true
		}
	}
	return closed
}

func (s *Server) removeActiveTunnel(id int64) *activeTunnel {
	s.tunnelMu.Lock()
	defer s.tunnelMu.Unlock()
	active := s.tunnels[id]
	delete(s.tunnels, id)
	return active
}

func (s *Server) stopAllTunnels() {
	s.tunnelMu.Lock()
	active := make([]*activeTunnel, 0, len(s.tunnels))
	for _, tunnel := range s.tunnels {
		active = append(active, tunnel)
	}
	s.tunnels = make(map[int64]*activeTunnel)
	s.tunnelMu.Unlock()

	for _, tunnel := range active {
		tunnel.stop()
	}
}

func (s *Server) stopTunnelRuntime(tunnelID int64) {
	s.tunnelMu.Lock()
	active := make([]*activeTunnel, 0)
	for id, tunnel := range s.tunnels {
		if tunnel.tunnel.ID == tunnelID {
			active = append(active, tunnel)
			delete(s.tunnels, id)
		}
	}
	s.tunnelMu.Unlock()

	for _, tunnel := range active {
		tunnel.stop()
	}
}

func (s *Server) markTunnelError(ctx context.Context, id int64, err error) (model.Tunnel, error) {
	tunnel, updateErr := db.SetTunnelStatus(ctx, s.database, id, model.TunnelStatusError, err.Error())
	if updateErr != nil {
		return model.Tunnel{}, updateErr
	}
	return tunnel, err
}

func (s *Server) recordTunnelRuntimeError(tunnelID int64, err error) {
	if err == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, updateErr := db.SetTunnelStatus(ctx, s.database, tunnelID, model.TunnelStatusError, err.Error()); updateErr != nil {
		s.logError("record tunnel runtime error failed tunnel_id=%d: %v", tunnelID, updateErr)
	}
}

func (t *activeTunnel) stop() {
	t.stopOnce.Do(func() {
		close(t.stopCh)
		_ = t.listener.Close()
		t.closeConnections()
	})
}

func (t *activeTunnel) addConnection(connectionID string, publicConn net.Conn, dataConn net.Conn) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.conns[connectionID] = []net.Conn{publicConn, dataConn}
}

func (t *activeTunnel) removeConnection(connectionID string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.conns, connectionID)
}

func (t *activeTunnel) closeConnection(connectionID string) bool {
	t.mu.Lock()
	conns := t.conns[connectionID]
	delete(t.conns, connectionID)
	t.mu.Unlock()

	if len(conns) == 0 {
		return false
	}
	for _, conn := range conns {
		_ = conn.Close()
	}
	return true
}

func (t *activeTunnel) connectionIDs() []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	ids := make([]string, 0, len(t.conns))
	for id := range t.conns {
		ids = append(ids, id)
	}
	return ids
}

func (t *activeTunnel) closeConnections() {
	t.mu.Lock()
	defer t.mu.Unlock()
	for id, conns := range t.conns {
		for _, conn := range conns {
			_ = conn.Close()
		}
		delete(t.conns, id)
	}
}

func (p *pendingDataConn) close() {
	p.doneOnce.Do(func() {
		close(p.done)
		p.closeConns()
	})
}

func (p *pendingDataConn) setDataConn(conn net.Conn) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.dataConn = conn
}

func (p *pendingDataConn) closeConns() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.publicConn != nil {
		_ = p.publicConn.Close()
	}
	if p.dataConn != nil {
		_ = p.dataConn.Close()
	}
}

func proxyRawTCP(publicConn net.Conn, dataConn net.Conn) (bytesIn int64, bytesOut int64) {
	return proxyRawTCPWithRecorder(publicConn, dataConn, nil, 0)
}

func proxyRawTCPWithRecorder(publicConn net.Conn, dataConn net.Conn, recorder *trafficRecorder, tunnelID int64) (bytesIn int64, bytesOut int64) {
	ch := make(chan copyResult, 2)
	go copyAndCloseWrite(dataConn, publicConn, "in", recorder, tunnelID, ch)
	go copyAndCloseWrite(publicConn, dataConn, "out", recorder, tunnelID, ch)

	// When either direction ends, close both sockets to unblock the opposite
	// copy; this keeps half-closed TCP sessions from leaking goroutines.
	first := <-ch
	_ = publicConn.Close()
	_ = dataConn.Close()
	second := <-ch

	for _, result := range []copyResult{first, second} {
		if result.direction == "in" {
			bytesIn = result.bytes
		} else {
			bytesOut = result.bytes
		}
	}
	return bytesIn, bytesOut
}

func copyAndCloseWrite(dst net.Conn, src net.Conn, direction string, recorder *trafficRecorder, tunnelID int64, ch chan<- copyResult) {
	writer := io.Writer(dst)
	if recorder != nil && tunnelID > 0 {
		writer = trafficCountingWriter{
			writer:    dst,
			recorder:  recorder,
			tunnelID:  tunnelID,
			direction: direction,
		}
	}
	n, _ := io.Copy(writer, src)
	closeWrite(dst)
	ch <- copyResult{direction: direction, bytes: n}
}

type trafficCountingWriter struct {
	writer    io.Writer
	recorder  *trafficRecorder
	tunnelID  int64
	direction string
}

func (w trafficCountingWriter) Write(p []byte) (int, error) {
	n, err := w.writer.Write(p)
	if n > 0 {
		if w.direction == "in" {
			w.recorder.recordTrafficDelta(w.tunnelID, int64(n), 0)
		} else {
			w.recorder.recordTrafficDelta(w.tunnelID, 0, int64(n))
		}
	}
	return n, err
}

func closeWrite(conn net.Conn) {
	if tcp, ok := conn.(*net.TCPConn); ok {
		_ = tcp.CloseWrite()
		return
	}
	_ = conn.Close()
}

func tunnelPayload(tunnel model.Tunnel) tunnelCommandPayload {
	return tunnelCommandPayload{
		Name:       tunnel.Name,
		Protocol:   string(tunnel.Protocol),
		RemoteHost: tunnel.RemoteHost,
		RemotePort: tunnel.RemotePort,
	}
}

func advertisedDataHost(host string) string {
	host = strings.TrimSpace(host)
	if host == "" || host == "0.0.0.0" || host == "::" {
		return ""
	}
	return host
}

func hostOrDefault(host string) string {
	host = strings.TrimSpace(host)
	if host == "" {
		return "0.0.0.0"
	}
	return host
}
