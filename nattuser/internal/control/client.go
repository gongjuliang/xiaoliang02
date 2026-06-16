// Package control 提供NATT客户端的核心控制逻辑。
// 包含控制连接管理器（客户端认证、心跳、命令处理、自动重连）、
// 数据通道管理（公网→内网数据代理转发）等功能。
// Manager作为核心控制器，以数据库中的tunnel_connections表为期望状态，
// 周期性扫描并启动/停止对应的控制连接goroutine，实现自动回连和故障恢复。
package control

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"nattuser/internal/config"
	"nattuser/internal/db"
	"nattuser/internal/logger"
	"nattuser/internal/model"
	"nattuser/internal/protocol"
)

// 控制连接管理器的时间常量：定义扫描间隔、重连间隔、拨号超时、心跳间隔等。
const (
	// defaultScanInterval 默认数据库扫描间隔：每3秒检查一次期望连接状态。
	defaultScanInterval = 3 * time.Second
	// defaultReconnectInterval 默认重连间隔：断线后5秒尝试重连。
	defaultReconnectInterval = 5 * time.Second
	// defaultDialTimeout 默认拨号超时：TCP连接建立超时10秒。
	defaultDialTimeout = 10 * time.Second
	// defaultHeartbeatInterval 默认心跳间隔：每15秒发送一次心跳。
	defaultHeartbeatInterval = 15 * time.Second
	// authTimeout 认证超时时间：控制连接建立后10秒内必须完成认证。
	authTimeout = 10 * time.Second
	// writeTimeout 写入超时时间：单次协议帧写入超时10秒。
	writeTimeout = 10 * time.Second
	// serverTunnelStoppedError 服务端停止隧道时的客户端提示信息。
	serverTunnelStoppedError = "服务端暂停了隧道连接，请通知服务端人员启动隧道。"
)

// Options 控制连接管理器的可配置选项，允许调整扫描、重连和心跳等运行参数。
type Options struct {
	ScanInterval      time.Duration // 数据库扫描间隔（≤0使用默认3秒）
	ReconnectInterval time.Duration // 重连间隔（≤0使用默认5秒）
	DialTimeout       time.Duration // 拨号超时（≤0使用默认10秒）
	HeartbeatInterval time.Duration // 心跳间隔（≤0使用默认15秒）
}

// Manager 控制连接管理器，管理所有到服务端的控制连接和数据通道。
// 以数据库为期望状态源，周期性同步活跃连接。线程安全。
type Manager struct {
	cfg      *config.Config // 客户端配置
	database *sql.DB        // 数据库连接
	log      *logger.Logger // 日志记录器
	options  Options        // 运行时选项

	mu      sync.Mutex                       // 保护active map的互斥锁
	active  map[int64]*activeConnection      // 活跃控制连接映射：tunnel_connection ID → activeConnection
	dataMu  sync.Mutex                       // 保护data map的互斥锁
	data    map[string]*activeDataConnection // 活跃数据连接映射：connectionID → activeDataConnection
	workers sync.WaitGroup                   // 等待所有工作协程完成的计数器
}

// activeConnection 活跃控制连接封装，包含连接ID和取消函数。
type activeConnection struct {
	id     int64              // 隧道连接ID
	cancel context.CancelFunc // 取消该连接所有协程的函数
}

// NewManager 创建控制连接管理器实例（使用默认选项）。
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
		return fmt.Errorf("%s", localizeProtocolError(protocolErr))
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
		return fmt.Errorf("%s", localizeProtocolMessage(authResp.Message))
	}
	if err := db.MarkServerConnectionConnectedWithRemotePort(ctx, m.database, connection.ID, authResp.RemotePort); err != nil {
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
	return dialer.DialContext(ctx, "tcp", addr)
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
	case protocol.TypeTunnelStart:
		m.logInfo("received control command server_connection_id=%d type=%s request_id=%s", connectionID, message.Type, message.RequestID)
		return nil
	case protocol.TypeTunnelStop:
		m.logInfo("received control command server_connection_id=%d type=%s request_id=%s", connectionID, message.Type, message.RequestID)
		_ = db.MarkServerConnectionError(ctx, m.database, connectionID, serverTunnelStoppedError)
		m.closeDataConnectionsForServer(connectionID)
		return nil
	case protocol.TypeError:
		protocolErr, _ := protocol.DecodePayload[protocol.ProtocolError](message)
		return fmt.Errorf("%s", localizeProtocolError(protocolErr))
	default:
		m.logInfo("ignored unsupported control message server_connection_id=%d type=%s", connectionID, message.Type)
		return nil
	}
}

func localizeProtocolError(err protocol.ProtocolError) string {
	if strings.TrimSpace(err.Message) != "" {
		return localizeProtocolMessage(err.Message)
	}
	switch err.Code {
	case protocol.CodeUnauthorized:
		return "秘钥错误"
	case protocol.CodeConflict:
		return "该连接正在占用，不得连接"
	case protocol.CodeBadRequest:
		return "请求参数不正确"
	default:
		return "服务端返回错误"
	}
}

func localizeProtocolMessage(message string) string {
	trimmed := strings.TrimSpace(message)
	switch {
	case trimmed == "":
		return "服务端返回错误"
	case strings.Contains(strings.ToLower(trimmed), "unauthorized"):
		return "秘钥错误"
	case strings.Contains(trimmed, "秘钥错误"):
		return "秘钥错误"
	case strings.Contains(trimmed, "该连接正在占用"):
		return "该连接正在占用，不得连接"
	default:
		return trimmed
	}
}

type controlWriter struct {
	conn net.Conn
	mu   sync.Mutex
}

type activeDataConnection struct {
	connectionID       string
	serverConnectionID int64
	dataConn           net.Conn
	localConn          net.Conn
	mu                 sync.Mutex
	closeOnce          sync.Once
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
