package db

import (
	"context"
	"database/sql"
	"fmt"
)

func RecordTunnelConnectionOpen(ctx context.Context, database *sql.DB, tunnelID int64) error {
	result, err := database.ExecContext(ctx, `
UPDATE traffic_stats
SET connection_count = connection_count + 1,
	active_connections = active_connections + 1
WHERE tunnel_id = ?;`, tunnelID)
	if err != nil {
		return fmt.Errorf("record tunnel connection open: %w", err)
	}
	return ensureRowsAffected(result, ErrNotFound)
}

func RecordTunnelConnectionClose(ctx context.Context, database *sql.DB, tunnelID int64) error {
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

func RecordTunnelTrafficDelta(ctx context.Context, database *sql.DB, tunnelID int64, bytesIn int64, bytesOut int64) error {
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

func ApplyTunnelTrafficDelta(ctx context.Context, database *sql.DB, tunnelID int64, connectionCountDelta int64, activeConnectionsDelta int64, bytesInDelta int64, bytesOutDelta int64) error {
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
