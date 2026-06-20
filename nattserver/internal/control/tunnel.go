// 本文件包含隧道管理的核心逻辑：启动/停止隧道、公网端口监听、
// 数据连接绑定和原始TCP代理转发。
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

// dataBindTimeout 数据绑定超时时间：客户端必须在10秒内完成数据通道绑定。
const dataBindTimeout = 10 * time.Second

// activeTunnel 活跃隧道封装，包含隧道信息、公网监听器和连接管理。
type activeTunnel struct {
	tunnel   model.Tunnel          // 隧道模型信息
	listener net.Listener          // 公网TCP监听器（接受外部用户连接）
	stopCh   chan struct{}         // 停止信号通道
	done     chan struct{}         // 完成信号通道（所有协程已退出）
	stopOnce sync.Once             // 确保停止操作只执行一次
	mu       sync.Mutex            // 保护conns map的互斥锁
	conns    map[string][]net.Conn // 活跃数据连接映射：connectionID -> [publicConn, dataConn]
}

// pendingDataConn 待绑定的数据连接，在外部用户连入后等待客户端的数据通道绑定。
type pendingDataConn struct {
	tunnelID     int64         // 关联的隧道ID
	connectionID string        // 连接唯一标识
	publicConn   net.Conn      // 外部用户的TCP连接
	dataConn     net.Conn      // 客户端的数据通道连接（绑定成功后填充）
	connCh       chan net.Conn // 数据连接传递通道（绑定成功后通过此通道传递dataConn）
	done         chan struct{} // 完成信号（超时或取消时关闭）
	doneOnce     sync.Once     // 确保done只关闭一次
	mu           sync.Mutex    // 保护dataConn的并发访问
}

// tunnelCommandPayload 隧道命令载荷，用于向客户端发送隧道启动/停止命令时携带的信息。
type tunnelCommandPayload struct {
	Name       string `json:"name"`        // 隧道名称
	Protocol   string `json:"protocol"`    // 传输协议（如tcp）
	RemoteHost string `json:"remote_host"` // 远程监听地址
	RemotePort int    `json:"remote_port"` // 远程监听端口
}

// copyResult 数据拷贝结果，记录单个方向的数据传输统计。
type copyResult struct {
	direction string // 传输方向（"in"=入站/"out"=出站）
	bytes     int64  // 传输的字节数
}

// StartTunnel 启动指定隧道，开始接受公网连接并转发数据。
// 执行流程：加载隧道配置→检查协议→抢占公网端口→发送启动命令给客户端→
// 注册活跃隧道→启动连接接受协程→更新数据库状态。
// 参数ctx：上下文。
// 参数id：隧道ID。
// 返回值：更新后的隧道模型和可能的错误。
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
	if !s.hasActiveClient(tunnel.ID) {
		return db.SetTunnelWaiting(ctx, s.database, tunnel.ID)
	}

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
		s.closeActiveClient(tunnel.ID)
		return db.SetTunnelWaiting(ctx, s.database, tunnel.ID)
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

// StopTunnel 停止指定隧道，关闭公网监听和所有活跃数据连接。
// 先发送data_close通知客户端关闭各数据连接，然后停止监听器，
// 最后发送tunnel_stop命令并更新数据库状态。
// 参数ctx：上下文。
// 参数id：隧道ID。
// 返回值：更新后的隧道模型和可能的错误。
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

// sendDataClose 向客户端发送数据连接关闭命令。
// 用于协作式关闭数据通道连接，确保双方都释放资源。
// 参数tunnelID：隧道ID。
// 参数connectionID：待关闭的连接ID。
// 参数code：关闭原因状态码。
// 参数message：关闭的详细说明。
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

// acceptTunnel 隧道公网连接接受循环。
// 持续接受外部用户的TCP连接，并为每个连接启动独立的goroutine处理。
// 当隧道停止时，Accept返回错误并退出循环。
// 参数active：活跃隧道封装。
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

// handlePublicConn 处理单个外部用户的公网连接。
// 执行流程：生成connectionID→创建pendingDataConn→发送data_open命令给客户端→
// 等待客户端的数据通道绑定（超时10秒）→绑定成功后启动原始TCP代理。
// 参数active：活跃隧道封装。
// 参数publicConn：外部用户的TCP连接。
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

// handleDataConn 处理客户端发起的数据通道连接。
// 执行流程：读取data_bind消息→密钥认证→校验connectionID匹配→
// 将数据连接传递给handlePublicConn中的pending等待。
// 参数ctx：上下文。
// 参数conn：客户端的数据通道TCP连接。
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

// addPending 将待绑定连接添加到pending映射中。
// 参数pending：待绑定的数据连接。
func (s *Server) addPending(pending *pendingDataConn) {
	s.pendingMu.Lock()
	defer s.pendingMu.Unlock()
	s.pending[pending.connectionID] = pending
}

// getPending 根据connectionID获取待绑定的数据连接。
// 参数connectionID：连接唯一标识。
// 返回值：待绑定的数据连接（不存在则返回nil）。
func (s *Server) getPending(connectionID string) *pendingDataConn {
	s.pendingMu.Lock()
	defer s.pendingMu.Unlock()
	return s.pending[connectionID]
}

// removePending 从pending映射中移除指定的待绑定连接。
// 参数connectionID：连接唯一标识。
func (s *Server) removePending(connectionID string) {
	s.pendingMu.Lock()
	defer s.pendingMu.Unlock()
	delete(s.pending, connectionID)
}

// closeDataConnection 关闭指定的数据连接。
// 同时检查pending和active两个映射，确保连接无论处于哪种状态都能被关闭。
// 参数connectionID：待关闭的连接ID。
// 返回值：是否成功找到并关闭了连接。
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

// removeActiveTunnel 从活跃隧道映射中移除指定隧道。
// 参数id：隧道ID。
// 返回值：被移除的活跃隧道封装（不存在则返回nil）。
func (s *Server) removeActiveTunnel(id int64) *activeTunnel {
	s.tunnelMu.Lock()
	defer s.tunnelMu.Unlock()
	active := s.tunnels[id]
	delete(s.tunnels, id)
	return active
}

// stopAllTunnels 停止所有活跃隧道。
// 原子地取出所有活跃隧道并清空映射，然后逐个停止。
// 在服务关闭时调用，确保所有资源被释放。
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

// stopTunnelRuntime 停止指定隧道的运行时资源。
// 从活跃隧道映射中查找并移除匹配的隧道，然后停止其监听器和连接。
// 参数tunnelID：待停止的隧道ID。
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

// markTunnelError 将隧道状态标记为异常，并记录错误信息到数据库。
// 参数ctx：上下文。
// 参数id：隧道ID。
// 参数err：导致异常的错误。
// 返回值：更新后的隧道模型和原始错误。
func (s *Server) markTunnelError(ctx context.Context, id int64, err error) (model.Tunnel, error) {
	tunnel, updateErr := db.SetTunnelStatus(ctx, s.database, id, model.TunnelStatusError, err.Error())
	if updateErr != nil {
		return model.Tunnel{}, updateErr
	}
	return tunnel, err
}

// recordTunnelRuntimeError 记录隧道运行时错误到数据库。
// 使用2秒超时的独立上下文，避免被父上下文取消影响。
// 参数tunnelID：隧道ID。
// 参数err：运行时错误（nil时跳过）。
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

// stop 停止活跃隧道的监听器和所有数据连接。
// 通过sync.Once保证只执行一次，关闭stopCh通知协程退出。
func (t *activeTunnel) stop() {
	t.stopOnce.Do(func() {
		close(t.stopCh)
		_ = t.listener.Close()
		t.closeConnections()
	})
}

// addConnection 将数据连接对添加到活跃连接映射中。
// 参数connectionID：连接ID。
// 参数publicConn：外部用户连接。
// 参数dataConn：客户端数据通道连接。
func (t *activeTunnel) addConnection(connectionID string, publicConn net.Conn, dataConn net.Conn) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.conns[connectionID] = []net.Conn{publicConn, dataConn}
}

// removeConnection 从活跃连接映射中移除指定连接。
// 参数connectionID：连接ID。
func (t *activeTunnel) removeConnection(connectionID string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.conns, connectionID)
}

// closeConnection 关闭指定连接ID的public和data两端连接。
// 参数connectionID：连接ID。
// 返回值：是否找到并关闭了连接。
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

// connectionIDs 获取所有活跃连接的ID列表。
// 返回值：连接ID字符串切片。
func (t *activeTunnel) connectionIDs() []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	ids := make([]string, 0, len(t.conns))
	for id := range t.conns {
		ids = append(ids, id)
	}
	return ids
}

// closeConnections 关闭隧道的所有活跃数据连接。
// 遍历并关闭每个连接对，然后清空连接映射。
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

// close 关闭待绑定数据连接，释放所有资源。
// 通过sync.Once保证只执行一次。
func (p *pendingDataConn) close() {
	p.doneOnce.Do(func() {
		close(p.done)
		p.closeConns()
	})
}

// setDataConn 设置客户端的数据通道连接（线程安全）。
// 参数conn：客户端的数据通道TCP连接。
func (p *pendingDataConn) setDataConn(conn net.Conn) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.dataConn = conn
}

// closeConns 关闭待绑定连接的public和data两端TCP连接。
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

// proxyRawTCP 在两个TCP连接之间建立双向原始数据代理（不带流量统计）。
// 参数publicConn：外部用户连接。
// 参数dataConn：客户端数据通道连接。
// 返回值：入站和出站字节数。
func proxyRawTCP(publicConn net.Conn, dataConn net.Conn) (bytesIn int64, bytesOut int64) {
	return proxyRawTCPWithRecorder(publicConn, dataConn, nil, 0)
}

// proxyRawTCPWithRecorder 在两个TCP连接之间建立双向原始数据代理，并记录流量统计。
// 任意一个方向的数据传输结束时，关闭两端连接以释放资源。
// 参数publicConn：外部用户连接。
// 参数dataConn：客户端数据通道连接。
// 参数recorder：流量记录器（可为nil，表示不统计）。
// 参数tunnelID：隧道ID（用于流量统计关联）。
// 返回值：入站和出站字节数。
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

// copyAndCloseWrite 单向数据拷贝并在完成后关闭写入端。
// 将src的数据拷贝到dst，支持流量统计包装。
// 参数dst：目标连接。
// 参数src：源连接。
// 参数direction：传输方向（"in"/"out"）。
// 参数recorder：流量记录器。
// 参数tunnelID：隧道ID。
// 参数ch：拷贝结果通道。
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

// trafficCountingWriter 流量计数写入器，包装io.Writer并在写入时记录流量统计。
// 实现io.Writer接口，透明地将流量数据传递给trafficRecorder。
type trafficCountingWriter struct {
	writer    io.Writer        // 底层写入器
	recorder  *trafficRecorder // 流量记录器
	tunnelID  int64            // 关联的隧道ID
	direction string           // 传输方向（"in"/"out"）
}

// Write 实现io.Writer接口，将数据写入底层写入器并记录流量统计。
// 参数p：待写入的数据字节切片。
// 返回值：写入的字节数和可能的错误。
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

// closeWrite 关闭TCP连接的写入端（发送FIN包）。
// 对于TCP连接调用CloseWrite()实现半关闭，其他类型连接则完全关闭。
// 参数conn：待关闭写入端的网络连接。
func closeWrite(conn net.Conn) {
	if tcp, ok := conn.(*net.TCPConn); ok {
		_ = tcp.CloseWrite()
		return
	}
	_ = conn.Close()
}

// tunnelPayload 将隧道模型转换为命令载荷结构体。
// 用于构建发送给客户端的隧道启动/停止命令。
// 参数tunnel：隧道模型。
// 返回值：隧道命令载荷。
func tunnelPayload(tunnel model.Tunnel) tunnelCommandPayload {
	return tunnelCommandPayload{
		Name:       tunnel.Name,
		Protocol:   string(tunnel.Protocol),
		RemoteHost: tunnel.RemoteHost,
		RemotePort: tunnel.RemotePort,
	}
}

// advertisedDataHost 获取对外公布的数据通道主机地址。
// 当配置为0.0.0.0或空时返回空字符串（客户端将使用服务端IP）。
// 参数host：配置的数据通道主机地址。
// 返回值：对外公布的地址。
func advertisedDataHost(host string) string {
	host = strings.TrimSpace(host)
	if host == "" || host == "0.0.0.0" || host == "::" {
		return ""
	}
	return host
}

// hostOrDefault 将空的主机地址替换为默认值"0.0.0.0"。
// 参数host：原始主机地址。
// 返回值：处理后的主机地址。
func hostOrDefault(host string) string {
	host = strings.TrimSpace(host)
	if host == "" {
		return "0.0.0.0"
	}
	return host
}
