package control

import (
	"context"
	"crypto/tls"
	"database/sql"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"

	"nattserver/internal/config"
	"nattserver/internal/db"
	"nattserver/internal/logger"
	"nattserver/internal/protocol"
)

const (
	heartbeatInterval = 15 * time.Second
	heartbeatTimeout  = 50 * time.Second
	writeTimeout      = 10 * time.Second
	authTimeout       = 10 * time.Second
)

type Server struct {
	cfg       config.ProtocolConfig
	database  *sql.DB
	log       *logger.Logger
	options   ServerOptions
	traffic   *trafficRecorder
	mu        sync.Mutex
	active    map[int64]*clientConn
	tunnelMu  sync.Mutex
	tunnels   map[int64]*activeTunnel
	pendingMu sync.Mutex
	pending   map[string]*pendingDataConn
}

type ServerOptions struct {
	HeartbeatTimeout     time.Duration
	HeartbeatMisses      int
	TrafficFlushInterval time.Duration
}

type clientConn struct {
	tunnelID int64
	conn     net.Conn
	mu       sync.Mutex
}

func NewServer(cfg config.ProtocolConfig, database *sql.DB, log *logger.Logger) *Server {
	return NewServerWithOptions(cfg, database, log, ServerOptions{})
}

func NewServerWithOptions(cfg config.ProtocolConfig, database *sql.DB, log *logger.Logger, options ServerOptions) *Server {
	return &Server{
		cfg:      cfg,
		database: database,
		log:      log,
		options:  normalizeServerOptions(options),
		traffic:  newTrafficRecorder(database, log, normalizeServerOptions(options).TrafficFlushInterval),
		active:   make(map[int64]*clientConn),
		tunnels:  make(map[int64]*activeTunnel),
		pending:  make(map[string]*pendingDataConn),
	}
}

func (s *Server) Run(ctx context.Context) error {
	// Control and data listeners are deliberately separate: control carries
	// auth, heartbeats, and commands, while data sockets carry raw user traffic.
	controlListener, err := s.listenControl()
	if err != nil {
		return err
	}
	defer controlListener.Close()

	dataListener, err := s.listenData()
	if err != nil {
		return err
	}
	defer dataListener.Close()

	go func() {
		<-ctx.Done()
		_ = controlListener.Close()
		_ = dataListener.Close()
	}()

	if s.log != nil {
		s.log.Infof("control server listening on %s", controlListener.Addr().String())
		s.log.Infof("data server listening on %s", dataListener.Addr().String())
	}

	errCh := make(chan error, 2)
	go func() {
		errCh <- s.serveControl(ctx, controlListener)
	}()
	go func() {
		errCh <- s.serveData(ctx, dataListener)
	}()
	go s.traffic.run(ctx)

	select {
	case <-ctx.Done():
		s.stopAllTunnels()
		_ = controlListener.Close()
		_ = dataListener.Close()
		<-errCh
		<-errCh
		return nil
	case err := <-errCh:
		s.stopAllTunnels()
		_ = controlListener.Close()
		_ = dataListener.Close()
		<-errCh
		return err
	}
}

func (s *Server) serveControl(ctx context.Context, listener net.Listener) error {
	for {
		conn, err := listener.Accept()
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				return nil
			}
			if s.log != nil {
				s.log.Errorf("accept control connection failed: %v", err)
			}
			continue
		}
		go s.handleConn(ctx, conn)
	}
}

func (s *Server) serveData(ctx context.Context, listener net.Listener) error {
	for {
		conn, err := listener.Accept()
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				return nil
			}
			if s.log != nil {
				s.log.Errorf("accept data connection failed: %v", err)
			}
			continue
		}
		go s.handleDataConn(ctx, conn)
	}
}

func (s *Server) SendToClient(tunnelID int64, message protocol.Message) error {
	s.mu.Lock()
	client := s.active[tunnelID]
	s.mu.Unlock()
	if client == nil {
		return fmt.Errorf("tunnel %d is not online", tunnelID)
	}
	return client.write(message)
}

func (s *Server) DisconnectTunnel(tunnelID int64) {
	s.mu.Lock()
	client := s.active[tunnelID]
	if client != nil {
		delete(s.active, tunnelID)
	}
	s.mu.Unlock()
	if client != nil {
		_ = client.conn.Close()
	}
	s.stopTunnelRuntime(tunnelID)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := db.MarkTunnelKeyOffline(ctx, s.database, tunnelID); err != nil {
		s.logError("mark disconnected tunnel offline failed tunnel_id=%d: %v", tunnelID, err)
	}
	if err := db.MarkTunnelUnavailable(ctx, s.database, tunnelID, "tunnel disconnected"); err != nil {
		s.logError("mark disconnected tunnel unavailable failed tunnel_id=%d: %v", tunnelID, err)
	}
}

func (s *Server) listenControl() (net.Listener, error) {
	addr := fmt.Sprintf("%s:%d", hostOrDefault(s.cfg.ControlHost), s.cfg.ControlPort)
	if !s.cfg.TLS.Enabled {
		return net.Listen("tcp", addr)
	}
	cert, err := tls.LoadX509KeyPair(s.cfg.TLS.CertFile, s.cfg.TLS.KeyFile)
	if err != nil {
		return nil, fmt.Errorf("load control TLS certificate: %w", err)
	}
	return tls.Listen("tcp", addr, &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12})
}

func (s *Server) listenData() (net.Listener, error) {
	addr := fmt.Sprintf("%s:%d", hostOrDefault(s.cfg.DataHost), s.cfg.DataPort)
	if !s.cfg.TLS.Enabled {
		return net.Listen("tcp", addr)
	}
	cert, err := tls.LoadX509KeyPair(s.cfg.TLS.CertFile, s.cfg.TLS.KeyFile)
	if err != nil {
		return nil, fmt.Errorf("load data TLS certificate: %w", err)
	}
	return tls.Listen("tcp", addr, &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12})
}

func (s *Server) handleConn(ctx context.Context, conn net.Conn) {
	defer conn.Close()

	// A control connection is trusted only after the first frame authenticates
	// the client secret. Until then the deadline stays short to avoid idle dials.
	_ = conn.SetDeadline(time.Now().Add(authTimeout))
	first, err := protocol.ReadMessage(conn)
	if err != nil {
		s.logError("read auth request failed: %v", err)
		return
	}
	if first.Type != protocol.TypeAuthRequest {
		_ = writeWithDeadline(conn, protocol.NewErrorMessage(first.RequestID, protocol.CodeBadRequest, "first message must be auth_request"))
		return
	}

	authReq, err := protocol.DecodePayload[protocol.AuthRequest](first)
	if err != nil || authReq.ClientSecret == "" {
		_ = writeWithDeadline(conn, protocol.NewErrorMessage(first.RequestID, protocol.CodeBadRequest, "invalid auth_request payload"))
		return
	}
	key, err := db.AuthenticateTunnelSecret(ctx, s.database, authReq.ClientSecret)
	if err != nil {
		_ = writeWithDeadline(conn, protocol.NewErrorMessage(first.RequestID, protocol.CodeUnauthorized, "秘钥错误"))
		s.logError("tunnel auth failed from %s: %v", conn.RemoteAddr().String(), err)
		return
	}

	tunnel, tunnelErr := db.GetTunnelByID(ctx, s.database, key.TunnelID)
	remoteIP, _, _ := net.SplitHostPort(conn.RemoteAddr().String())
	if remoteIP == "" {
		remoteIP = conn.RemoteAddr().String()
	}
	clientID := key.TunnelID
	remotePort := 0
	if tunnelErr == nil {
		remotePort = tunnel.RemotePort
	}
	if tunnelErr == nil && tunnel.ClientID > 0 {
		clientID = tunnel.ClientID
	}
	active := &clientConn{tunnelID: key.TunnelID, conn: conn}
	if !s.register(active) {
		_ = writeWithDeadline(conn, protocol.NewErrorMessage(first.RequestID, protocol.CodeConflict, "该连接正在占用，不得连接"))
		return
	}
	defer s.unregister(key.TunnelID, active)
	if err := db.MarkTunnelKeyOnline(ctx, s.database, key.TunnelID, remoteIP); err != nil {
		s.logError("mark tunnel online failed tunnel_id=%d: %v", key.TunnelID, err)
	}
	_ = writeWithDeadline(conn, authResponse(first.RequestID, true, clientID, key.TunnelID, remotePort, "ok"))
	s.startTunnelIfAutoStart(ctx, key.TunnelID)

	_ = conn.SetDeadline(time.Time{})
	if s.log != nil {
		s.log.Infof("tunnel control connected tunnel_id=%d remote=%s", key.TunnelID, conn.RemoteAddr().String())
	}
	missedHeartbeats := 0
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		_ = conn.SetReadDeadline(time.Now().Add(s.options.HeartbeatTimeout))
		message, err := protocol.ReadMessage(conn)
		if err != nil {
			if isTimeout(err) {
				// One slow heartbeat may be a network blip; three consecutive
				// misses mark the control connection dead and trigger cleanup.
				missedHeartbeats++
				if missedHeartbeats < s.options.HeartbeatMisses {
					s.logError("control heartbeat timeout tunnel_id=%d missed=%d", key.TunnelID, missedHeartbeats)
					continue
				}
			}
			s.logError("control connection closed tunnel_id=%d: %v", key.TunnelID, err)
			return
		}
		missedHeartbeats = 0
		if err := s.handleMessage(ctx, active, message); err != nil {
			s.logError("handle control message failed tunnel_id=%d request_id=%s: %v", key.TunnelID, message.RequestID, err)
			_ = active.write(protocol.NewErrorMessage(message.RequestID, protocol.CodeBadRequest, err.Error()))
		}
	}
}

func (s *Server) handleMessage(ctx context.Context, client *clientConn, message protocol.Message) error {
	switch message.Type {
	case protocol.TypeHeartbeat:
		if err := db.MarkTunnelKeyHeartbeat(ctx, s.database, client.tunnelID); err != nil {
			return err
		}
		ack, err := protocol.NewMessage(protocol.TypeHeartbeatAck, 0, client.tunnelID, "", protocol.HeartbeatAck{ServerTime: time.Now().Unix()})
		if err != nil {
			return err
		}
		ack.RequestID = message.RequestID
		return client.write(ack)
	case protocol.TypeTunnelStatus:
		return nil
	case protocol.TypeDataClose:
		if strings.TrimSpace(message.ConnectionID) == "" {
			return fmt.Errorf("data_close connection_id is required")
		}
		if _, err := protocol.DecodePayload[protocol.DataClose](message); err != nil {
			return err
		}
		// data_close is the cooperative shutdown signal for the split data path;
		// closing by connection_id tears down either a pending bind or live proxy.
		s.closeDataConnection(message.ConnectionID)
		return nil
	default:
		return fmt.Errorf("unsupported message type: %s", message.Type)
	}
}

func (s *Server) register(client *clientConn) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.active[client.tunnelID] != nil {
		return false
	}
	s.active[client.tunnelID] = client
	return true
}

func (s *Server) unregister(tunnelID int64, client *clientConn) {
	s.mu.Lock()
	if s.active[tunnelID] == client {
		delete(s.active, tunnelID)
	}
	s.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := db.MarkTunnelKeyOffline(ctx, s.database, tunnelID); err != nil {
		s.logError("mark tunnel offline failed tunnel_id=%d: %v", tunnelID, err)
	}
	if err := db.MarkTunnelUnavailable(ctx, s.database, tunnelID, "tunnel control connection offline"); err != nil {
		s.logError("mark tunnel unavailable failed tunnel_id=%d: %v", tunnelID, err)
	}
	s.stopTunnelRuntime(tunnelID)
}

func (s *Server) startTunnelIfAutoStart(ctx context.Context, tunnelID int64) {
	tunnel, err := db.GetTunnelByID(ctx, s.database, tunnelID)
	if err != nil {
		s.logError("load auto-start tunnel failed tunnel_id=%d: %v", tunnelID, err)
		return
	}
	if tunnel.AutoStart {
		if _, err := s.StartTunnel(ctx, tunnel.ID); err != nil {
			s.logError("start auto-start tunnel failed tunnel_id=%d: %v", tunnel.ID, err)
		}
	}
}

func (c *clientConn) write(message protocol.Message) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return writeWithDeadline(c.conn, message)
}

func writeWithDeadline(conn net.Conn, message protocol.Message) error {
	_ = conn.SetWriteDeadline(time.Now().Add(writeTimeout))
	defer conn.SetWriteDeadline(time.Time{})
	return protocol.WriteMessage(conn, message)
}

func authResponse(requestID string, success bool, clientID int64, tunnelID int64, remotePort int, message string) protocol.Message {
	response, _ := protocol.NewMessage(protocol.TypeAuthResponse, 0, tunnelID, "", protocol.AuthResponse{
		Success:                  success,
		ClientID:                 clientID,
		TunnelID:                 tunnelID,
		RemotePort:               remotePort,
		ProtocolVersion:          protocol.Version,
		HeartbeatIntervalSeconds: int(heartbeatInterval.Seconds()),
		Message:                  message,
	})
	response.RequestID = requestID
	return response
}

func (s *Server) logError(format string, args ...any) {
	if s.log != nil {
		s.log.Errorf(format, args...)
	}
}

func normalizeServerOptions(options ServerOptions) ServerOptions {
	if options.HeartbeatTimeout <= 0 {
		options.HeartbeatTimeout = heartbeatTimeout
	}
	if options.HeartbeatMisses <= 0 {
		options.HeartbeatMisses = 3
	}
	if options.TrafficFlushInterval <= 0 {
		options.TrafficFlushInterval = defaultTrafficFlushInterval
	}
	return options
}

func isTimeout(err error) bool {
	var netErr net.Error
	return errors.As(err, &netErr) && netErr.Timeout()
}
