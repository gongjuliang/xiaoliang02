// Package db 提供隧道流量统计相关的数据库操作。
// 使用SQL的原子增量更新（UPDATE ... SET col = col + ?）避免并发数据竞争，
// 支持连接数、活跃连接数和流量字节的精确累计统计。
package db

import (
	// context 提供上下文传递。
	"context"
	// database/sql 提供数据库操作接口。
	"database/sql"
	// fmt 提供错误信息格式化。
	"fmt"
)

// RecordTunnelConnectionOpen 记录隧道新连接的打开，原子递增连接计数和活跃连接数。
// 参数ctx：上下文。
// 参数database：数据库连接。
// 参数tunnelID：隧道ID。
// 返回值：可能的错误。
func RecordTunnelConnectionOpen(ctx context.Context, database *sql.DB, tunnelID int64) error {
	// 原子更新：连接数+1，活跃连接数+1
	result, err := database.ExecContext(ctx, `
UPDATE traffic_stats
SET connection_count = connection_count + 1,
	active_connections = active_connections + 1
WHERE tunnel_id = ?;`, tunnelID)
	if err != nil {
		return fmt.Errorf("record tunnel connection open: %w", err)
	}
	// 检查是否有行被更新（隧道不存在时返回ErrNotFound）
	return ensureRowsAffected(result, ErrNotFound)
}

// RecordTunnelConnectionClose 记录隧道连接的关闭，原子递减活跃连接数（不低于0）。
// 参数ctx：上下文。
// 参数database：数据库连接。
// 参数tunnelID：隧道ID。
// 返回值：可能的错误。
func RecordTunnelConnectionClose(ctx context.Context, database *sql.DB, tunnelID int64) error {
	// 原子更新：活跃连接数递减（CASE防止减到负数）
	result, err := database.ExecContext(ctx, `
UPDATE traffic_stats
SET active_connections = CASE
	WHEN active_connections > 0 THEN active_connections - 1
	ELSE 0
END
WHERE tunnel_id = ?;`, tunnelID)
	if err != nil {
		return fmt.Errorf("record tunnel connection close: %w", err)
	}
	return ensureRowsAffected(result, ErrNotFound)
}

// RecordTunnelTrafficDelta 记录隧道流量的增量数据（入站和出站字节数）。
// 参数ctx：上下文。
// 参数database：数据库连接。
// 参数tunnelID：隧道ID。
// 参数bytesIn：入站字节增量。
// 参数bytesOut：出站字节增量。
// 返回值：可能的错误。
func RecordTunnelTrafficDelta(ctx context.Context, database *sql.DB, tunnelID int64, bytesIn int64, bytesOut int64) error {
	// 原子更新：入站和出站流量累加
	result, err := database.ExecContext(ctx, `
UPDATE traffic_stats
SET bytes_in = bytes_in + ?,
	bytes_out = bytes_out + ?
WHERE tunnel_id = ?;`, bytesIn, bytesOut, tunnelID)
	if err != nil {
		return fmt.Errorf("record tunnel traffic delta: %w", err)
	}
	return ensureRowsAffected(result, ErrNotFound)
}

// ApplyTunnelTrafficDelta 批量应用隧道的多项流量增量数据。
// 在一次SQL更新中同时处理连接数、活跃连接数、入站和出站流量的变化。
// 参数ctx：上下文。
// 参数database：数据库连接。
// 参数tunnelID：隧道ID。
// 参数connectionCountDelta：连接数变化量。
// 参数activeConnectionsDelta：活跃连接数变化量。
// 参数bytesInDelta：入站流量变化量。
// 参数bytesOutDelta：出站流量变化量。
// 返回值：可能的错误。
func ApplyTunnelTrafficDelta(ctx context.Context, database *sql.DB, tunnelID int64, connectionCountDelta int64, activeConnectionsDelta int64, bytesInDelta int64, bytesOutDelta int64) error {
	// 批量原子更新：一次性应用所有流量统计变化
	result, err := database.ExecContext(ctx, `
UPDATE traffic_stats
SET connection_count = connection_count + ?,
	active_connections = CASE
		WHEN active_connections + ? > 0 THEN active_connections + ?
		ELSE 0
	END,
	bytes_in = bytes_in + ?,
	bytes_out = bytes_out + ?
WHERE tunnel_id = ?;`, connectionCountDelta, activeConnectionsDelta, activeConnectionsDelta, bytesInDelta, bytesOutDelta, tunnelID)
	if err != nil {
		return fmt.Errorf("apply tunnel traffic delta: %w", err)
	}
	return ensureRowsAffected(result, ErrNotFound)
}
