// Package control 提供NATT服务端的核心控制逻辑。
// 包含控制通道服务器（客户端认证、心跳、命令处理）、
// 隧道管理（启动/停止/数据转发）和流量统计等功能。
package control

import (
	"context"
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
	heartbeatInterval = 15 * time.Second // 心跳发送间隔：客户端每15秒发送一次心跳
	heartbeatTimeout  = 50 * time.Second // 心跳超时时间：超过50秒未收到心跳视为异常
	writeTimeout      = 10 * time.Second // 写入超时时间：单次协议帧写入超时
	authTimeout       = 10 * time.Second // 认证超时时间：控制连接建立后10秒内必须完成认证
)

// tunnelStoppedByServerMessage 服务端主动停止隧道时发送给客户端的提示信息。
const tunnelStoppedByServerMessage = "服务端暂停了隧道连接，请通知服务端人员启动隧道。"

// Server 控制通道服务器，管理服务端所有客户端连接、隧道和数据通道。
// 内部维护三个并发安全的Map：active（在线客户端）、tunnels（活跃隧道）、pending（待绑定数据连接）。
type Server struct {
	cfg       config.ProtocolConfig       // 协议通道配置
	database  *sql.DB                     // 数据库连接
	log       *logger.Logger              // 日志记录器
	options   ServerOptions               // 服务器选项
	traffic   *trafficRecorder            // 流量记录器
	mu        sync.Mutex                  // 保护active map的互斥锁
	active    map[int64]*clientConn       // 在线客户端连接映射：tunnelID -> clientConn
	tunnelMu  sync.Mutex                  // 保护tunnels map的互斥锁
	tunnels   map[int64]*activeTunnel     // 活跃隧道映射：tunnelID -> activeTunnel
	pendingMu sync.Mutex                  // 保护pending map的互斥锁
	pending   map[string]*pendingDataConn // 待绑定的数据连接映射：connectionID -> pendingDataConn
}

// ServerOptions 服务器可配置选项，允许调整心跳、流量统计等运行参数。
type ServerOptions struct {
	HeartbeatTimeout     time.Duration // 心跳超时时间（≤0时使用默认值50秒）
	HeartbeatMisses      int           // 允许连续心跳丢失次数（≤0时使用默认值3次）
	TrafficFlushInterval time.Duration // 流量数据刷盘间隔（≤0时使用默认值1秒）
}

// clientConn 客户端控制连接封装，包含连接对象和写操作互斥锁。
type clientConn struct {
	tunnelID int64      // 关联的隧道ID
	conn     net.Conn   // 底层TCP连接
	mu       sync.Mutex // 写操作互斥锁，保证同一连接的写入串行化
}

// NewServer 创建控制通道服务器实例（使用默认选项）。
// 参数cfg：协议通道配置。
// 参数database：数据库连接。
// 参数log：日志记录器。
// 返回值：初始化好的Server指针。
func NewServer(cfg config.ProtocolConfig, database *sql.DB, log *logger.Logger) *Server {
	return NewServerWithOptions(cfg, database, log, ServerOptions{})
}

// NewServerWithOptions 创建控制通道服务器实例（支持自定义选项）。
// 参数cfg：协议通道配置。
// 参数database：数据库连接。
// 参数log：日志记录器。
// 参数options：自定义服务器选项。
// 返回值：初始化好的Server指针。
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

// Run 启动控制通道和数据通道的监听服务。
// 分别启动控制端口和数据端口的TCP监听，并在各自的goroutine中处理连接。
// 当上下文被取消时，停止所有隧道并关闭监听器。
// 参数ctx：上下文（用于关闭信号）。
// 返回值：服务错误（ctx取消时返回nil）。
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
		s.log.Infof("内网穿透-服务端 control 服务 正在监听 %s", controlListener.Addr().String())
		s.log.Infof("内网穿透-服务端 data 服务 正在监听 %s", dataListener.Addr().String())
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

// serveControl 控制通道连接接受循环。
// 持续接受新的控制通道连接，并为每个连接启动独立的goroutine处理。
// 参数ctx：上下文。
// 参数listener：控制通道监听器。
// 返回值：服务错误。
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

// serveData 数据通道连接接受循环。
// 持续接受新的数据通道连接（外部用户连入或客户端数据绑定），并为每个连接启动独立goroutine。
// 参数ctx：上下文。
// 参数listener：数据通道监听器。
// 返回值：服务错误。
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

// SendToClient 向指定隧道的客户端发送协议消息。
// 通过tunnelID查找对应的客户端连接，如果客户端不在线则返回错误。
// 参数tunnelID：目标隧道ID。
// 参数message：待发送的协议消息。
// 返回值：发送错误。
func (s *Server) SendToClient(tunnelID int64, message protocol.Message) error {
	s.mu.Lock()
	client := s.active[tunnelID]
	s.mu.Unlock()
	if client == nil {
		return fmt.Errorf("隧道 %d 未在线", tunnelID)
	}
	return client.write(message)
}

// DisconnectTunnel 断开指定隧道的客户端控制连接。
// 从活跃连接表中移除客户端、关闭TCP连接、停止隧道运行时、
// 并更新数据库中隧道密钥的离线状态和隧道不可用状态。
// 参数tunnelID：待断开的隧道ID。
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
	if err := db.MarkTunnelUnavailable(ctx, s.database, tunnelID, "隧道连接已断开"); err != nil {
		s.logError("mark disconnected tunnel unavailable failed tunnel_id=%d: %v", tunnelID, err)
	}
}

// listenControl 创建控制通道的TCP监听器。
// 使用配置中的ControlHost和ControlPort。
// 返回值：TCP监听器和可能的错误。
func (s *Server) listenControl() (net.Listener, error) {
	addr := fmt.Sprintf("%s:%d", hostOrDefault(s.cfg.ControlHost), s.cfg.ControlPort)
	return net.Listen("tcp", addr)
}

// listenData 创建数据通道的TCP监听器。
// 使用配置中的DataHost和DataPort。
// 返回值：TCP监听器和可能的错误。
func (s *Server) listenData() (net.Listener, error) {
	addr := fmt.Sprintf("%s:%d", hostOrDefault(s.cfg.DataHost), s.cfg.DataPort)
	return net.Listen("tcp", addr)
}

// handleConn 处理单个客户端控制连接的完整生命周期。
// 执行流程：设置认证超时→读取并验证auth_request→数据库密钥认证→
// 检查隧道状态→注册客户端连接→发送认证响应→进入心跳消息循环。
// 参数ctx：上下文。
// 参数conn：客户端TCP连接。
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
		_ = writeWithDeadline(conn, protocol.NewErrorMessage(first.RequestID, protocol.CodeBadRequest, "首条消息必须是 auth_request"))
		return
	}

	authReq, err := protocol.DecodePayload[protocol.AuthRequest](first)
	if err != nil || authReq.ClientSecret == "" {
		_ = writeWithDeadline(conn, protocol.NewErrorMessage(first.RequestID, protocol.CodeBadRequest, "auth_request 参数不正确"))
		return
	}
	key, err := db.AuthenticateTunnelSecret(ctx, s.database, authReq.ClientSecret)
	if err != nil {
		_ = writeWithDeadline(conn, protocol.NewErrorMessage(first.RequestID, protocol.CodeUnauthorized, "秘钥错误"))
		s.logError("tunnel auth failed from %s: %v", conn.RemoteAddr().String(), err)
		return
	}

	tunnel, tunnelErr := db.GetTunnelByID(ctx, s.database, key.TunnelID)
	if tunnelErr == nil && tunnel.Status == "stopped" && !tunnel.AutoStart {
		_ = writeWithDeadline(conn, protocol.NewErrorMessage(first.RequestID, protocol.CodeConflict, tunnelStoppedByServerMessage))
		return
	}
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

// handleMessage 处理来自客户端的单条控制消息。
// 支持的消息类型：heartbeat（心跳）、tunnel_status（隧道状态）、data_close（关闭数据连接）。
// 参数ctx：上下文。
// 参数client：客户端连接封装。
// 参数message：接收到的协议消息。
// 返回值：处理错误。
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
			return fmt.Errorf("data_close 缺少 connection_id")
		}
		if _, err := protocol.DecodePayload[protocol.DataClose](message); err != nil {
			return err
		}
		// data_close is the cooperative shutdown signal for the split data path;
		// closing by connection_id tears down either a pending bind or live proxy.
		s.closeDataConnection(message.ConnectionID)
		return nil
	default:
		return fmt.Errorf("不支持的消息类型：%s", message.Type)
	}
}

// register 将客户端连接注册到活跃连接表中。
// 如果该tunnelID已有活跃连接则返回false，防止重复连接。
// 参数client：待注册的客户端连接。
// 返回值：注册是否成功。
func (s *Server) register(client *clientConn) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.active[client.tunnelID] != nil {
		return false
	}
	s.active[client.tunnelID] = client
	return true
}

// unregister 从活跃连接表中注销客户端连接，并更新数据库状态。
// 仅在当前注册的就是该client实例时才执行注销（防止误删新连接）。
// 参数tunnelID：隧道ID。
// 参数client：待注销的客户端连接。
func (s *Server) unregister(tunnelID int64, client *clientConn) {
	s.mu.Lock()
	removed := false
	if s.active[tunnelID] == client {
		delete(s.active, tunnelID)
		removed = true
	}
	s.mu.Unlock()
	if !removed {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := db.MarkTunnelKeyOffline(ctx, s.database, tunnelID); err != nil {
		s.logError("mark tunnel offline failed tunnel_id=%d: %v", tunnelID, err)
	}
	if err := db.MarkTunnelUnavailable(ctx, s.database, tunnelID, "隧道控制连接已离线"); err != nil {
		s.logError("mark tunnel unavailable failed tunnel_id=%d: %v", tunnelID, err)
	}
	s.stopTunnelRuntime(tunnelID)
}

// startTunnelIfAutoStart 检查隧道是否配置了自动启动，如果是则立即启动隧道。
// 在客户端认证成功后调用，确保auto_start隧道自动恢复运行。
// 参数ctx：上下文。
// 参数tunnelID：待检查的隧道ID。
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

// write 向客户端控制连接发送协议消息（线程安全）。
// 通过互斥锁保证同一连接的写入操作串行化。
// 参数message：待发送的协议消息。
// 返回值：写入错误。
func (c *clientConn) write(message protocol.Message) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return writeWithDeadline(c.conn, message)
}

// writeWithDeadline 带写入超时的协议消息发送。
// 设置10秒写入超时，完成后重置截止时间。
// 参数conn：TCP连接。
// 参数message：待发送的协议消息。
// 返回值：写入错误。
func writeWithDeadline(conn net.Conn, message protocol.Message) error {
	_ = conn.SetWriteDeadline(time.Now().Add(writeTimeout))
	defer conn.SetWriteDeadline(time.Time{})
	return protocol.WriteMessage(conn, message)
}

// authResponse 构建认证响应消息。
// 参数requestID：关联的原始请求ID。
// 参数success：认证是否成功。
// 参数clientID：客户端ID。
// 参数tunnelID：隧道ID。
// 参数remotePort：分配的远程端口。
// 参数message：附加说明信息。
// 返回值：构建好的认证响应协议消息。
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

// logError 辅助方法：安全地记录错误日志（当log为nil时无操作）。
// 参数format：日志格式字符串。
// 参数args：格式化参数。
func (s *Server) logError(format string, args ...any) {
	if s.log != nil {
		s.log.Errorf(format, args...)
	}
}

// normalizeServerOptions 规范化服务器选项，将未设置或无效的值填充为默认值。
// 参数options：原始服务器选项。
// 返回值：规范化后的服务器选项。
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

// isTimeout 判断错误是否为网络超时错误。
// 通过类型断言检查error是否实现了net.Error接口且Timeout()为true。
// 参数err：待检查的错误。
// 返回值：是否为超时错误。
func isTimeout(err error) bool {
	var netErr net.Error
	return errors.As(err, &netErr) && netErr.Timeout()
}
