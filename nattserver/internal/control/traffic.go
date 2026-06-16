// Package control 提供隧道流量数据的内存缓冲和定期刷盘机制。
// 由于代理协程（proxy goroutine）频繁产生流量更新，为避免每次数据包都写SQLite，
// 采用内存中批量累积增量、按时间间隔（默认1秒）周期性刷盘的策略，
// 大幅减少数据库写入压力。线程安全。
package control

import (
	// context 提供上下文传递和取消信号。
	"context"
	// database/sql 提供数据库连接。
	"database/sql"
	// sync 提供互斥锁保证并发安全。
	"sync"
	// time 提供定时器和时间间隔。
	"time"

	// nattserver/internal/db 数据库操作包。
	"nattserver/internal/db"
	// nattserver/internal/logger 日志包。
	"nattserver/internal/logger"
)

// defaultTrafficFlushInterval 默认流量刷盘间隔：1秒。
const defaultTrafficFlushInterval = time.Second

// trafficRecorder 流量记录器，在内存中缓冲流量变化数据并定时批量写入数据库。
type trafficRecorder struct {
	// database 数据库连接，用于批量写入流量数据。
	database *sql.DB
	// log 日志记录器。
	log *logger.Logger
	// flushInterval 刷盘间隔时间。
	flushInterval time.Duration

	// mu 互斥锁，保护pending map的并发访问。
	mu sync.Mutex
	// pending 待刷盘的流量增量数据，key为隧道ID。
	pending map[int64]trafficDelta
}

// trafficDelta 流量增量数据结构，记录一个统计周期内的变化量。
type trafficDelta struct {
	// connectionCountDelta 连接数变化量（正数为新增，负数为减少）。
	connectionCountDelta int64
	// activeConnectionsDelta 活跃连接数变化量。
	activeConnectionsDelta int64
	// bytesInDelta 入站字节变化量。
	bytesInDelta int64
	// bytesOutDelta 出站字节变化量。
	bytesOutDelta int64
}

// newTrafficRecorder 创建流量记录器实例。
// 参数database：数据库连接。
// 参数log：日志记录器。
// 参数flushInterval：刷盘间隔（≤0时使用默认值1秒）。
// 返回值：初始化好的trafficRecorder指针。
func newTrafficRecorder(database *sql.DB, log *logger.Logger, flushInterval time.Duration) *trafficRecorder {
	// 刷盘间隔为空或非正数时使用默认值
	if flushInterval <= 0 {
		flushInterval = defaultTrafficFlushInterval
	}
	return &trafficRecorder{
		database:      database,
		log:           log,
		flushInterval: flushInterval,
		pending:       make(map[int64]trafficDelta), // 初始化空累计map
	}
}

// run 启动流量记录器的后台循环，定时刷盘并响应上下文取消信号。
// 当ctx被取消（服务关闭信号）时，使用新的background context确保最后的流量数据持久化。
// 参数ctx：上下文（当ctx.Done()时退出循环）。
func (r *trafficRecorder) run(ctx context.Context) {
	// 创建定时器，按指定间隔触发刷盘
	ticker := time.NewTicker(r.flushInterval)
	defer ticker.Stop() // 函数返回时停止定时器
	for {
		select {
		case <-ctx.Done():
			// 使用全新的background context刷新剩余数据，
			// 确保在调用方上下文已取消后仍能持久化计数器数据。
			r.flush(context.Background())
			return
		case <-ticker.C:
			// 定时触发刷盘
			r.flush(ctx)
		}
	}
}

// recordConnectionOpen 记录一次隧道连接的打开事件。
// 参数tunnelID：隧道ID。
func (r *trafficRecorder) recordConnectionOpen(tunnelID int64) {
	// 添加连接数+1、活跃连接数+1的增量
	r.add(tunnelID, trafficDelta{connectionCountDelta: 1, activeConnectionsDelta: 1})
}

// recordConnectionClose 记录一次隧道连接的关闭事件。
// 参数tunnelID：隧道ID。
func (r *trafficRecorder) recordConnectionClose(tunnelID int64) {
	// 添加活跃连接数-1的增量
	r.add(tunnelID, trafficDelta{activeConnectionsDelta: -1})
}

// recordTrafficDelta 记录隧道的数据传输流量增量。
// 参数tunnelID：隧道ID。
// 参数bytesIn：入站字节增量。
// 参数bytesOut：出站字节增量。
func (r *trafficRecorder) recordTrafficDelta(tunnelID int64, bytesIn int64, bytesOut int64) {
	// 入站和出站都为0则跳过（无实际数据传输）
	if bytesIn == 0 && bytesOut == 0 {
		return
	}
	// 添加流量增量
	r.add(tunnelID, trafficDelta{bytesInDelta: bytesIn, bytesOutDelta: bytesOut})
}

// add 将流量增量累加到指定隧道的pending缓冲区中。
// 流量在多个代理协程中更新，因此采用内存批量累加以避免每次数据包都写入SQLite。
// 参数tunnelID：隧道ID。
// 参数delta：要累加的增量数据。
func (r *trafficRecorder) add(tunnelID int64, delta trafficDelta) {
	// 无效隧道ID直接忽略
	if tunnelID <= 0 {
		return
	}
	// 流量数据从代理协程中更新，所以增量在内存中批量累积，
	// 然后周期性写入SQLite，而不是每包一写。
	// 加锁保护pending map
	r.mu.Lock()
	// 获取该隧道的当前累计值
	current := r.pending[tunnelID]
	// 累加各项增量
	current.connectionCountDelta += delta.connectionCountDelta
	current.activeConnectionsDelta += delta.activeConnectionsDelta
	current.bytesInDelta += delta.bytesInDelta
	current.bytesOutDelta += delta.bytesOutDelta
	// 更新回map
	r.pending[tunnelID] = current
	r.mu.Unlock()
}

// flush 将内存中累积的流量数据批量写入数据库。
// 使用takePending()原子地交换pending map，确保读取和清空是线程安全的。
// 参数ctx：上下文（用于数据库写入）。
func (r *trafficRecorder) flush(ctx context.Context) {
	// 原子取出所有待刷盘的数据并清空pending
	pending := r.takePending()
	// 无数据或数据库不可用则跳过
	if len(pending) == 0 || r.database == nil {
		return
	}
	// 逐隧道写入数据库
	for tunnelID, delta := range pending {
		// 调用db层的批量写入函数
		if err := db.ApplyTunnelTrafficDelta(ctx, r.database, tunnelID, delta.connectionCountDelta, delta.activeConnectionsDelta, delta.bytesInDelta, delta.bytesOutDelta); err != nil {
			// 刷盘失败记录错误日志但不中断（下一个周期会重试）
			r.logError("flush traffic stats failed tunnel_id=%d: %v", tunnelID, err)
		}
	}
}

// takePending 原子地获取所有pending数据并重置为空的map。
// 通过交换引用而非逐项复制来提高性能。
// 返回值：交换前的pending map副本。
func (r *trafficRecorder) takePending() map[int64]trafficDelta {
	// 加锁保护
	r.mu.Lock()
	defer r.mu.Unlock()
	// 保存当前pending引用
	pending := r.pending
	// 创建新的空map替换旧引用
	r.pending = make(map[int64]trafficDelta)
	return pending
}

// logError 辅助方法：安全地记录错误日志（log为nil时无操作）。
func (r *trafficRecorder) logError(format string, args ...any) {
	if r.log != nil {
		r.log.Errorf(format, args...)
	}
}
