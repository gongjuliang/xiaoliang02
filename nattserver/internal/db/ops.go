package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"nattserver/internal/model"
)

type DashboardSummary struct {
	OnlineClients     int64 `json:"online_clients"`
	TotalClients      int64 `json:"total_clients"`
	OnlineTunnels     int64 `json:"online_tunnels"`
	RunningTunnels    int64 `json:"running_tunnels"`
	TotalTunnels      int64 `json:"total_tunnels"`
	ActiveConnections int64 `json:"active_connections"`
	ConnectionCount   int64 `json:"connection_count"`
	BytesIn           int64 `json:"bytes_in"`
	BytesOut          int64 `json:"bytes_out"`
}

func GetDashboardSummary(ctx context.Context, database *sql.DB) (DashboardSummary, error) {
	var summary DashboardSummary
	if err := database.QueryRowContext(ctx, "SELECT COUNT(1) FROM tunnel_keys WHERE online_status = 'online'").Scan(&summary.OnlineTunnels); err != nil {
		return DashboardSummary{}, fmt.Errorf("count online tunnels: %w", err)
	}
	summary.OnlineClients = summary.OnlineTunnels
	if err := database.QueryRowContext(ctx, "SELECT COUNT(1) FROM tunnels").Scan(&summary.TotalTunnels); err != nil {
		return DashboardSummary{}, fmt.Errorf("count tunnels: %w", err)
	}
	summary.TotalClients = summary.TotalTunnels
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
	return listAuditLogsFromFiles(limit, offset)
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

func GetSetting(ctx context.Context, database *sql.DB, key string) (string, error) {
	var value string
	err := database.QueryRowContext(ctx, "SELECT value FROM settings WHERE key = ?;", key).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", fmt.Errorf("get setting %s: %w", key, err)
	}
	return value, nil
}
