package db

import (
	"context"
	"database/sql"
	"fmt"

	"nattserver/internal/model"
)

type DashboardSummary struct {
	OnlineClients     int64 `json:"online_clients"`
	TotalClients      int64 `json:"total_clients"`
	RunningTunnels    int64 `json:"running_tunnels"`
	TotalTunnels      int64 `json:"total_tunnels"`
	ActiveConnections int64 `json:"active_connections"`
	ConnectionCount   int64 `json:"connection_count"`
	BytesIn           int64 `json:"bytes_in"`
	BytesOut          int64 `json:"bytes_out"`
}

func GetDashboardSummary(ctx context.Context, database *sql.DB) (DashboardSummary, error) {
	var summary DashboardSummary
	if err := database.QueryRowContext(ctx, "SELECT COUNT(1) FROM clients").Scan(&summary.TotalClients); err != nil {
		return DashboardSummary{}, fmt.Errorf("count clients: %w", err)
	}
	if err := database.QueryRowContext(ctx, "SELECT COUNT(1) FROM clients WHERE online_status = 'online'").Scan(&summary.OnlineClients); err != nil {
		return DashboardSummary{}, fmt.Errorf("count online clients: %w", err)
	}
	if err := database.QueryRowContext(ctx, "SELECT COUNT(1) FROM tunnels").Scan(&summary.TotalTunnels); err != nil {
		return DashboardSummary{}, fmt.Errorf("count tunnels: %w", err)
	}
	if err := database.QueryRowContext(ctx, "SELECT COUNT(1) FROM tunnels WHERE status = 'running'").Scan(&summary.RunningTunnels); err != nil {
		return DashboardSummary{}, fmt.Errorf("count running tunnels: %w", err)
	}
	if err := database.QueryRowContext(ctx, `
SELECT
	COALESCE(SUM(active_connections), 0),
	COALESCE(SUM(connection_count), 0),
	COALESCE(SUM(bytes_in), 0),
	COALESCE(SUM(bytes_out), 0)
FROM traffic_stats;`).Scan(
		&summary.ActiveConnections,
		&summary.ConnectionCount,
		&summary.BytesIn,
		&summary.BytesOut,
	); err != nil {
		return DashboardSummary{}, fmt.Errorf("sum traffic stats: %w", err)
	}
	return summary, nil
}

func ListAuditLogs(ctx context.Context, database *sql.DB, limit int, offset int) ([]model.AuditLog, int64, error) {
	var total int64
	if err := database.QueryRowContext(ctx, "SELECT COUNT(1) FROM audit_logs").Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count audit logs: %w", err)
	}
	rows, err := database.QueryContext(ctx, `
SELECT id, actor, action, target_type, target_id, content, ip, created_at
FROM audit_logs
ORDER BY id DESC
LIMIT ? OFFSET ?;`, limit, offset)
	if err != nil {
		return nil, 0, fmt.Errorf("list audit logs: %w", err)
	}
	defer rows.Close()

	var logs []model.AuditLog
	for rows.Next() {
		var item model.AuditLog
		var targetType sql.NullString
		var targetID sql.NullString
		var ip sql.NullString
		if err := rows.Scan(&item.ID, &item.Actor, &item.Action, &targetType, &targetID, &item.Content, &ip, &item.CreatedAt); err != nil {
			return nil, 0, fmt.Errorf("scan audit log: %w", err)
		}
		item.TargetType = nullableString(targetType)
		item.TargetID = nullableString(targetID)
		item.IP = nullableString(ip)
		logs = append(logs, item)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("iterate audit logs: %w", err)
	}
	return logs, total, nil
}

func ListSettings(ctx context.Context, database *sql.DB) ([]model.Setting, error) {
	rows, err := database.QueryContext(ctx, `
SELECT key, value, updated_at
FROM settings
ORDER BY key;`)
	if err != nil {
		return nil, fmt.Errorf("list settings: %w", err)
	}
	defer rows.Close()

	var settings []model.Setting
	for rows.Next() {
		var item model.Setting
		if err := rows.Scan(&item.Key, &item.Value, &item.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan setting: %w", err)
		}
		settings = append(settings, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate settings: %w", err)
	}
	return settings, nil
}

func UpsertSetting(ctx context.Context, database *sql.DB, key string, value string) error {
	_, err := database.ExecContext(ctx, `
INSERT INTO settings(key, value)
VALUES(?, ?)
ON CONFLICT(key) DO UPDATE SET value = excluded.value;`, key, value)
	if err != nil {
		return fmt.Errorf("upsert setting %s: %w", key, err)
	}
	return nil
}
