package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"nattuser/internal/model"
)

type ClientStatusSummary struct {
	TotalServerConnections     int64 `json:"total_server_connections"`
	ConnectedServerConnections int64 `json:"connected_server_connections"`
	StoppedServerConnections   int64 `json:"stopped_server_connections"`
	ErrorServerConnections     int64 `json:"error_server_connections"`
}

func GetClientStatusSummary(ctx context.Context, database *sql.DB) (ClientStatusSummary, error) {
	var summary ClientStatusSummary
	if err := database.QueryRowContext(ctx, "SELECT COUNT(1) FROM tunnel_connections").Scan(&summary.TotalServerConnections); err != nil {
		return ClientStatusSummary{}, fmt.Errorf("count server connections: %w", err)
	}
	if err := database.QueryRowContext(ctx, "SELECT COUNT(1) FROM tunnel_connections WHERE status = 'connected'").Scan(&summary.ConnectedServerConnections); err != nil {
		return ClientStatusSummary{}, fmt.Errorf("count connected server connections: %w", err)
	}
	if err := database.QueryRowContext(ctx, "SELECT COUNT(1) FROM tunnel_connections WHERE status = 'stopped'").Scan(&summary.StoppedServerConnections); err != nil {
		return ClientStatusSummary{}, fmt.Errorf("count stopped server connections: %w", err)
	}
	if err := database.QueryRowContext(ctx, "SELECT COUNT(1) FROM tunnel_connections WHERE status = 'error'").Scan(&summary.ErrorServerConnections); err != nil {
		return ClientStatusSummary{}, fmt.Errorf("count error server connections: %w", err)
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
