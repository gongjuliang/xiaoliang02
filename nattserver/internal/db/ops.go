// Package db 提供运维管理相关的数据库查询功能。
// 包括仪表盘数据汇总、审计日志查询（文件存储）、
// 系统设置的增删改查操作（键值对存储）。
package db

import (
	// context 提供上下文传递。
	"context"
	// database/sql 提供数据库操作接口。
	"database/sql"
	// errors 提供错误判断。
	"errors"
	// fmt 提供错误信息格式化。
	"fmt"

	// nattserver/internal/model 项目数据模型。
	"nattserver/internal/model"
)

// DashboardSummary 仪表盘摘要数据结构，聚合系统的关键运行指标。
type DashboardSummary struct {
	// OnlineClients 当前在线的客户端数量。
	OnlineClients int64 `json:"online_clients"`
	// TotalClients 客户端总数量。
	TotalClients int64 `json:"total_clients"`
	// OnlineTunnels 当前在线的隧道密钥数量。
	OnlineTunnels int64 `json:"online_tunnels"`
	// RunningTunnels 当前正在运行的隧道数量。
	RunningTunnels int64 `json:"running_tunnels"`
	// TotalTunnels 隧道总数量。
	TotalTunnels int64 `json:"total_tunnels"`
	// ActiveConnections 当前活跃连接总数。
	ActiveConnections int64 `json:"active_connections"`
	// ConnectionCount 历史累计连接总数。
	ConnectionCount int64 `json:"connection_count"`
	// BytesIn 累计入站流量字节数。
	BytesIn int64 `json:"bytes_in"`
	// BytesOut 累计出站流量字节数。
	BytesOut int64 `json:"bytes_out"`
}

// GetDashboardSummary 获取仪表盘的汇总数据。
// 聚合在线隧道数量、隧道总数、运行中隧道数及流量统计数据。
// 参数ctx：上下文。
// 参数database：数据库连接。
// 返回值：DashboardSummary和可能的错误。
func GetDashboardSummary(ctx context.Context, database *sql.DB) (DashboardSummary, error) {
	// 声明汇总结构体
	var summary DashboardSummary
	// 查询在线隧道密钥数量（在线密钥数即在线隧道数）
	if err := database.QueryRowContext(ctx, "SELECT COUNT(1) FROM tunnel_keys WHERE online_status = 'online'").Scan(&summary.OnlineTunnels); err != nil {
		return DashboardSummary{}, fmt.Errorf("count online tunnels: %w", err)
	}
	// 在线客户端数与在线隧道数相同（每个隧道密钥对应一个客户端连接）
	summary.OnlineClients = summary.OnlineTunnels
	// 查询隧道总数
	if err := database.QueryRowContext(ctx, "SELECT COUNT(1) FROM tunnels").Scan(&summary.TotalTunnels); err != nil {
		return DashboardSummary{}, fmt.Errorf("count tunnels: %w", err)
	}
	// 客户端总数与隧道总数相同
	summary.TotalClients = summary.TotalTunnels
	// 查询正在运行的隧道数量
	if err := database.QueryRowContext(ctx, "SELECT COUNT(1) FROM tunnels WHERE status = 'running'").Scan(&summary.RunningTunnels); err != nil {
		return DashboardSummary{}, fmt.Errorf("count running tunnels: %w", err)
	}
	// 查询流量统计数据（使用COALESCE确保无数据时返回0而非NULL）
	if err := database.QueryRowContext(ctx, `
SELECT
	COALESCE(SUM(active_connections), 0),
	COALESCE(SUM(connection_count), 0),
	COALESCE(SUM(bytes_in), 0),
	COALESCE(SUM(bytes_out), 0)
FROM traffic_stats;`).Scan(
		&summary.ActiveConnections, // 累计活跃连接数
		&summary.ConnectionCount,   // 累计连接数
		&summary.BytesIn,           // 累计入站流量
		&summary.BytesOut,          // 累计出站流量
	); err != nil {
		return DashboardSummary{}, fmt.Errorf("sum traffic stats: %w", err)
	}
	return summary, nil
}

// ListAuditLogs 列出审计日志（从文件系统读取，参见audit.go）。
// 参数ctx：上下文。
// 参数database：数据库连接（当前未使用，保留接口兼容性）。
// 参数limit：返回的最大记录数。
// 参数offset：跳过的记录数。
// 返回值：审计日志列表、总数和可能的错误。
func ListAuditLogs(ctx context.Context, database *sql.DB, limit int, offset int) ([]model.AuditLog, int64, error) {
	// 委托给文件系统审计日志实现
	return listAuditLogsFromFiles(limit, offset)
}

// ListSettings 列出所有系统设置项，按key排序。
// 参数ctx：上下文。
// 参数database：数据库连接。
// 返回值：设置项列表和可能的错误。
func ListSettings(ctx context.Context, database *sql.DB) ([]model.Setting, error) {
	// 查询所有设置项，按key排序
	rows, err := database.QueryContext(ctx, `
SELECT key, value, updated_at
FROM settings
ORDER BY key;`)
	if err != nil {
		return nil, fmt.Errorf("list settings: %w", err)
	}
	defer rows.Close() // 确保结果集关闭

	// 遍历查询结果
	var settings []model.Setting
	for rows.Next() {
		var item model.Setting
		// 扫描当前行到结构体
		if err := rows.Scan(&item.Key, &item.Value, &item.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan setting: %w", err)
		}
		settings = append(settings, item)
	}
	// 检查遍历过程中是否有错误
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate settings: %w", err)
	}
	return settings, nil
}

// UpsertSetting 插入或更新系统设置项（键值对）。
// SQL使用ON CONFLICT语法实现：key不存在则插入，存在则更新value和updated_at。
// 参数ctx：上下文。
// 参数database：数据库连接。
// 参数key：设置键名。
// 参数value：设置值。
// 返回值：可能的错误。
func UpsertSetting(ctx context.Context, database *sql.DB, key string, value string) error {
	// 使用INSERT ... ON CONFLICT DO UPDATE实现upsert语义
	_, err := database.ExecContext(ctx, `
INSERT INTO settings(key, value)
VALUES(?, ?)
ON CONFLICT(key) DO UPDATE SET value = excluded.value;`, key, value)
	if err != nil {
		return fmt.Errorf("upsert setting %s: %w", key, err)
	}
	return nil
}

// GetSetting 根据键名获取单个系统设置的值。
// 参数ctx：上下文。
// 参数database：数据库连接。
// 参数key：设置键名。
// 返回值：设置值和可能的错误（未找到时返回ErrNotFound）。
func GetSetting(ctx context.Context, database *sql.DB, key string) (string, error) {
	var value string
	// 查询指定key的值
	err := database.QueryRowContext(ctx, "SELECT value FROM settings WHERE key = ?;", key).Scan(&value)
	// 未找到记录
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	// 其他数据库错误
	if err != nil {
		return "", fmt.Errorf("get setting %s: %w", key, err)
	}
	return value, nil
}
