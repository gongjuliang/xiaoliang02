package control

import (
	"context"
	"crypto/tls"
	"database/sql"
	"errors"
	"fmt"
	"net"
	"runtime"
	"strconv"
	"sync"
	"time"

	"nattuser/internal/config"
	"nattuser/internal/db"
	"nattuser/internal/logger"
	"nattuser/internal/model"
	"nattuser/internal/protocol"
)

const (
	defaultScanInterval      = 3 * time.Second
	defaultReconnectInterval = 5 * time.Second
	defaultDialTimeout       = 10 * time.Second
	defaultHeartbeatInterval = 15 * time.Second
	authTimeout              = 10 * time.Second
	writeTimeout             = 10 * time.Second
)

type Options struct {
	ScanInterval      time.Duration
	ReconnectInterval time.Duration
	DialTimeout       time.Duration
	HeartbeatInterval time.Duration
}

type Manager struct {
	cfg      *config.Config
	database *sql.DB
	log      *logger.Logger
	options  Options

	mu      sync.Mutex
	active  map[int64]*activeConnection
	dataMu  sync.Mutex
	data    map[string]*activeDataConnection
	workers sync.WaitGroup
}

type activeConnection struct {
	id     int64
	cancel context.CancelFunc
}

func NewManager(cfg *config.Config, database *sql.DB, log *logger.Logger) *Manager {
	return NewManagerWithOptions(cfg, database, log, Options{})
}

func NewManagerWithOptions(cfg *config.Config, database *sql.DB, log *logger.Logger, options Options) *Manager {
	if cfg == nil {
		cfg = config.Default()
	}
	return &Manager{
		cfg:      cfg,
		database: database,
		log:      log,
		options:  normalizeOptions(options),
		active:   make(map[int64]*activeConnection),
		data:     make(map[string]*activeDataConnection),
	}
}

func (m *Manager) Run(ctx context.Context) error {
	if err := m.syncConnections(ctx); err != nil {
		m.logError("initial control connection sync failed: %v", err)
	}

	// The manager treats the database as desired state: every scan starts or
	// stops control workers to match saved server connection records.
	ticker := time.NewTicker(m.options.ScanInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			m.stopAll()
			return nil
		case <-ticker.C:
			if err := m.syncConnections(ctx); err != nil {
				m.logError("control connection sync failed: %v", err)
			}
		}
	}
}

func (m *Manager) syncConnections(ctx context.Context) error {
	connections, err := db.ListControlServerConnections(ctx, m.database)
	if err != nil {
		return err
	}

	desired := make(map[int64]model.ServerConnection, len(connections))
	for _, connection := range connections {
		desired[connection.ID] = connection
		m.startConnection(ctx, connection)
	}

	m.mu.Lock()
	for id, active := range m.active {
		if _, ok := desired[id]; !ok {
			active.cancel()
			delete(m.active, id)
		}
	}
	m.mu.Unlock()
	return nil
}

func (m *Manager) startConnection(parent context.Context, connection model.ServerConnection) {
	m.mu.Lock()
	if _, ok := m.active[connection.ID]; ok {
		m.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(parent)
	active := &activeConnection{id: connection.ID, cancel: cancel}
	m.active[connection.ID] = active
	m.workers.Add(1)
	m.mu.Unlock()

	go func() {
		defer m.workers.Done()
		defer m.removeActive(connection.ID, active)
		m.runConnection(ctx, connection)
	}()
}

func (m *Manager) removeActive(id int64, active *activeConnection) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.active[id] == active {
		delete(m.active, id)
	}
}

func (m *Manager) stopAll() {
	m.mu.Lock()
	active := make([]*activeConnection, 0, len(m.active))
	for _, connection := range m.active {
		active = append(active, connection)
	}
	m.active = make(map[int64]*activeConnection)
	m.mu.Unlock()

	for _, connection := range active {
		connection.cancel()
	}
	m.workers.Wait()
}

func (m *Manager) runConnection(ctx context.Context, connection model.ServerConnection) {
	for {
		if ctx.Err() != nil {
			_ = db.MarkServerConnectionStopped(context.Background(), m.database, connection.ID)
			return
		}
		if !m.shouldRun(ctx, connection.ID) {
			return
		}

		if err := m.connectAndServe(ctx, connection); err != nil {
			if ctx.Err() != nil {
				_ = db.MarkServerConnectionStopped(context.Background(), m.database, connection.ID)
				return
			}
			m.logError("control connection failed server_connection_id=%d: %v", connection.ID, err)
			_ = db.MarkServerConnectionError(context.Background(), m.database, connection.ID, err.Error())
		}

		// A failed control connection loops until the saved record is stopped or
		// deleted, giving auto-start connections process-level reconnects.
		if !sleepContext(ctx, m.options.ReconnectInterval) {
			_ = db.MarkServerConnectionStopped(context.Background(), m.database, connection.ID)
			return
		}
		refreshed, err := db.GetServerConnectionByID(ctx, m.database, connection.ID)
		if errors.Is(err, db.ErrNotFound) {
			return
		}
		if err != nil {
			m.logError("refresh server connection failed id=%d: %v", connection.ID, err)
			continue
		}
		connection = refreshed
	}
}

func (m *Manager) shouldRun(ctx context.Context, id int64) bool {
	connection, err := db.GetServerConnectionByID(ctx, m.database, id)
	if errors.Is(err, db.ErrNotFound) {
		return false
	}
	if err != nil {
		m.logError("load server connection failed id=%d: %v", id, err)
		return true
	}
	return connection.AutoStart ||
		connection.Status == model.ServerConnectionStatusConnecting ||
		connection.Status == model.ServerConnectionStatusConnected ||
		connection.Status == model.ServerConnectionStatusError
}

func (m *Manager) connectAndServe(ctx context.Context, connection model.ServerConnection) error {
	if err := db.MarkServerConnectionConnecting(ctx, m.database, connection.ID); err != nil {
		return err
	}

	conn, err := m.dial(ctx, connection)
	if err != nil {
		return err
	}
	defer conn.Close()
	go func() {
		<-ctx.Done()
		_ = conn.Close()
	}()

	// Control authentication happens before any tunnel command is accepted; the
	// server returns heartbeat settings and the stable server-side client id.
	authReq, err := protocol.NewMessage(protocol.TypeAuthRequest, 0, 0, "", protocol.AuthRequest{
		ClientSecret:    connection.ClientSecret,
		ClientName:      connection.Name,
		ClientVersion:   m.cfg.App.Version,
		ProtocolVersion: protocol.Version,
		SystemInfo: map[string]any{
			"goos":   runtime.GOOS,
			"goarch": runtime.GOARCH,
		},
	})
	if err != nil {
		return err
	}
	if err := writeWithDeadline(conn, authReq); err != nil {
		return fmt.Errorf("send auth request: %w", err)
	}

	_ = conn.SetReadDeadline(time.Now().Add(authTimeout))
	authRespMessage, err := protocol.ReadMessage(conn)
	if err != nil {
		return fmt.Errorf("read auth response: %w", err)
	}
	_ = conn.SetReadDeadline(time.Time{})
	if authRespMessage.Type == protocol.TypeError {
		protocolErr, _ := protocol.DecodePayload[protocol.ProtocolError](authRespMessage)
		return fmt.Errorf("auth rejected: %s %s", protocolErr.Code, protocolErr.Message)
	}
	if authRespMessage.Type != protocol.TypeAuthResponse {
		return fmt.Errorf("unexpected auth response type: %s", authRespMessage.Type)
	}
	authResp, err := protocol.DecodePayload[protocol.AuthResponse](authRespMessage)
	if err != nil {
		return err
	}
	if !authResp.Success {
		if authResp.Message == "" {
			authResp.Message = "authentication failed"
		}
		return fmt.Errorf("auth rejected: %s", authResp.Message)
	}
	if err := db.MarkServerConnectionConnected(ctx, m.database, connection.ID); err != nil {
		return err
	}
	m.logInfo("control connected server_connection_id=%d remote=%s", connection.ID, conn.RemoteAddr().String())

	heartbeatInterval := m.options.HeartbeatInterval
	if authResp.HeartbeatIntervalSeconds > 0 {
		heartbeatInterval = time.Duration(authResp.HeartbeatIntervalSeconds) * time.Second
	}
	return m.serveControlMessages(ctx, connection.ID, conn, heartbeatInterval)
}

func (m *Manager) dial(ctx context.Context, connection model.ServerConnection) (net.Conn, error) {
	addr := net.JoinHostPort(connection.ServerHost, strconv.Itoa(connection.ServerPort))
	dialer := &net.Dialer{Timeout: m.options.DialTimeout}
	if !connection.UseTLS {
		return dialer.DialContext(ctx, "tcp", addr)
	}

	rawConn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, err
	}
	tlsConn := tls.Client(rawConn, &tls.Config{
		ServerName:         connection.ServerHost,
		MinVersion:         tls.VersionTLS12,
		InsecureSkipVerify: true,
	})
	handshakeCtx, cancel := context.WithTimeout(ctx, m.options.DialTimeout)
	defer cancel()
	if err := tlsConn.HandshakeContext(handshakeCtx); err != nil {
		_ = rawConn.Close()
		return nil, err
	}
	return tlsConn, nil
}

func (m *Manager) serveControlMessages(ctx context.Context, connectionID int64, conn net.Conn, heartbeatInterval time.Duration) error {
	errCh := make(chan error, 2)
	writer := &controlWriter{conn: conn}
	// Heartbeats and reads run independently so server commands are handled even
	// while the next heartbeat tick is still waiting.
	go m.heartbeatLoop(ctx, connectionID, writer, heartbeatInterval, errCh)
	go m.readLoop(ctx, connectionID, conn, writer, heartbeatInterval, errCh)

	select {
	case <-ctx.Done():
		return nil
	case err := <-errCh:
		return err
	}
}

func (m *Manager) heartbeatLoop(ctx context.Context, connectionID int64, writer *controlWriter, interval time.Duration, errCh chan<- error) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			message, err := protocol.NewMessage(protocol.TypeHeartbeat, 0, 0, "", protocol.Heartbeat{ClientTime: time.Now().Unix()})
			if err != nil {
				errCh <- err
				return
			}
			if err := writer.write(message); err != nil {
				errCh <- fmt.Errorf("send heartbeat server_connection_id=%d: %w", connectionID, err)
				return
			}
		}
	}
}

func (m *Manager) readLoop(ctx context.Context, connectionID int64, conn net.Conn, writer *controlWriter, heartbeatInterval time.Duration, errCh chan<- error) {
	readTimeout := heartbeatInterval * 4
	if readTimeout < 30*time.Second {
		readTimeout = 30 * time.Second
	}

	for {
		if ctx.Err() != nil {
			return
		}
		_ = conn.SetReadDeadline(time.Now().Add(readTimeout))
		message, err := protocol.ReadMessage(conn)
		if err != nil {
			errCh <- fmt.Errorf("read control message server_connection_id=%d: %w", connectionID, err)
			return
		}
		if err := m.handleMessage(ctx, connectionID, writer, message); err != nil {
			errCh <- err
			return
		}
	}
}

func (m *Manager) handleMessage(ctx context.Context, connectionID int64, writer *controlWriter, message protocol.Message) error {
	switch message.Type {
	case protocol.TypeHeartbeatAck:
		return db.MarkServerConnectionHeartbeat(ctx, m.database, connectionID)
	case protocol.TypeDataOpen:
		dataOpen, err := protocol.DecodePayload[protocol.DataOpen](message)
		if err != nil {
			return err
		}
		connection, err := db.GetServerConnectionByID(ctx, m.database, connectionID)
		if err != nil {
			return err
		}
		go m.openDataConnection(ctx, connection, writer, message, dataOpen)
		return nil
	case protocol.TypeDataClose:
		// The server owns public listeners, so it can ask the client to close a
		// specific data connection when the remote side or tunnel is gone.
		if err := m.closeDataConnection(message.ConnectionID); err != nil {
			return err
		}
		return nil
	case protocol.TypeTunnelStart, protocol.TypeTunnelStop:
		m.logInfo("received control command server_connection_id=%d type=%s request_id=%s", connectionID, message.Type, message.RequestID)
		return nil
	case protocol.TypeError:
		protocolErr, _ := protocol.DecodePayload[protocol.ProtocolError](message)
		return fmt.Errorf("control error: %s %s", protocolErr.Code, protocolErr.Message)
	default:
		m.logInfo("ignored unsupported control message server_connection_id=%d type=%s", connectionID, message.Type)
		return nil
	}
}

type controlWriter struct {
	conn net.Conn
	mu   sync.Mutex
}

type activeDataConnection struct {
	connectionID string
	dataConn     net.Conn
	localConn    net.Conn
	mu           sync.Mutex
	closeOnce    sync.Once
}

func (w *controlWriter) write(message protocol.Message) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return writeWithDeadline(w.conn, message)
}

func writeWithDeadline(conn net.Conn, message protocol.Message) error {
	_ = conn.SetWriteDeadline(time.Now().Add(writeTimeout))
	defer conn.SetWriteDeadline(time.Time{})
	return protocol.WriteMessage(conn, message)
}

func sleepContext(ctx context.Context, duration time.Duration) bool {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func normalizeOptions(options Options) Options {
	if options.ScanInterval <= 0 {
		options.ScanInterval = defaultScanInterval
	}
	if options.ReconnectInterval <= 0 {
		options.ReconnectInterval = defaultReconnectInterval
	}
	if options.DialTimeout <= 0 {
		options.DialTimeout = defaultDialTimeout
	}
	if options.HeartbeatInterval <= 0 {
		options.HeartbeatInterval = defaultHeartbeatInterval
	}
	return options
}

func (m *Manager) logInfo(format string, args ...any) {
	if m.log != nil {
		m.log.Infof(format, args...)
	}
}

func (m *Manager) logError(format string, args ...any) {
	if m.log != nil {
		m.log.Errorf(format, args...)
	}
}
