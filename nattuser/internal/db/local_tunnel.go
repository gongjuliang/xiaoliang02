package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"nattuser/internal/model"
)

type CreateLocalTunnelParams struct {
	Name               string
	ServerConnectionID int64
	ServerTunnelID     int64
	LocalHost          string
	LocalPort          int
	Enabled            bool
	Remark             string
}

type UpdateLocalTunnelParams struct {
	Name               string
	ServerConnectionID int64
	ServerTunnelID     int64
	LocalHost          string
	LocalPort          int
	Enabled            bool
	Remark             string
}

func ListLocalTunnels(ctx context.Context, database *sql.DB, limit int, offset int) ([]model.LocalTunnel, int64, error) {
	var total int64
	if err := database.QueryRowContext(ctx, "SELECT COUNT(1) FROM local_tunnels").Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count local tunnels: %w", err)
	}
	rows, err := database.QueryContext(ctx, `
SELECT id, name, server_connection_id, server_tunnel_id, local_host, local_port, enabled, last_error, remark, created_at, updated_at
FROM local_tunnels
ORDER BY id DESC
LIMIT ? OFFSET ?;`, limit, offset)
	if err != nil {
		return nil, 0, fmt.Errorf("list local tunnels: %w", err)
	}
	defer rows.Close()

	var tunnels []model.LocalTunnel
	for rows.Next() {
		tunnel, err := scanLocalTunnel(rows)
		if err != nil {
			return nil, 0, err
		}
		tunnels = append(tunnels, tunnel)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("iterate local tunnels: %w", err)
	}
	return tunnels, total, nil
}

func GetLocalTunnelByID(ctx context.Context, database *sql.DB, id int64) (model.LocalTunnel, error) {
	row := database.QueryRowContext(ctx, `
SELECT id, name, server_connection_id, server_tunnel_id, local_host, local_port, enabled, last_error, remark, created_at, updated_at
FROM local_tunnels
WHERE id = ?;`, id)
	tunnel, err := scanLocalTunnel(row)
	if errors.Is(err, sql.ErrNoRows) {
		return model.LocalTunnel{}, ErrNotFound
	}
	if err != nil {
		return model.LocalTunnel{}, err
	}
	return tunnel, nil
}

func GetEnabledLocalTunnelByServerTunnel(ctx context.Context, database *sql.DB, serverConnectionID int64, serverTunnelID int64) (model.LocalTunnel, error) {
	row := database.QueryRowContext(ctx, `
SELECT id, name, server_connection_id, server_tunnel_id, local_host, local_port, enabled, last_error, remark, created_at, updated_at
FROM local_tunnels
WHERE server_connection_id = ? AND server_tunnel_id = ? AND enabled = 1;`, serverConnectionID, serverTunnelID)
	tunnel, err := scanLocalTunnel(row)
	if errors.Is(err, sql.ErrNoRows) {
		return model.LocalTunnel{}, ErrNotFound
	}
	if err != nil {
		return model.LocalTunnel{}, err
	}
	return tunnel, nil
}

func CreateLocalTunnel(ctx context.Context, database *sql.DB, params CreateLocalTunnelParams) (model.LocalTunnel, error) {
	result, err := database.ExecContext(ctx, `
INSERT INTO local_tunnels(name, server_connection_id, server_tunnel_id, local_host, local_port, enabled, remark)
VALUES(?, ?, ?, ?, ?, ?, ?);`,
		params.Name,
		params.ServerConnectionID,
		params.ServerTunnelID,
		params.LocalHost,
		params.LocalPort,
		boolToInt(params.Enabled),
		params.Remark,
	)
	if err != nil {
		return model.LocalTunnel{}, mapSQLiteError("create local tunnel", err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		return model.LocalTunnel{}, fmt.Errorf("get created local tunnel id: %w", err)
	}
	return GetLocalTunnelByID(ctx, database, id)
}

func UpdateLocalTunnel(ctx context.Context, database *sql.DB, id int64, params UpdateLocalTunnelParams) (model.LocalTunnel, error) {
	result, err := database.ExecContext(ctx, `
UPDATE local_tunnels
SET name = ?, server_connection_id = ?, server_tunnel_id = ?, local_host = ?, local_port = ?, enabled = ?, remark = ?, last_error = NULL
WHERE id = ?;`,
		params.Name,
		params.ServerConnectionID,
		params.ServerTunnelID,
		params.LocalHost,
		params.LocalPort,
		boolToInt(params.Enabled),
		params.Remark,
		id,
	)
	if err != nil {
		return model.LocalTunnel{}, mapSQLiteError("update local tunnel", err)
	}
	if err := ensureRowsAffected(result, ErrNotFound); err != nil {
		return model.LocalTunnel{}, err
	}
	return GetLocalTunnelByID(ctx, database, id)
}

func DeleteLocalTunnel(ctx context.Context, database *sql.DB, id int64) (model.LocalTunnel, error) {
	tunnel, err := GetLocalTunnelByID(ctx, database, id)
	if err != nil {
		return model.LocalTunnel{}, err
	}
	if _, err := database.ExecContext(ctx, "DELETE FROM local_tunnels WHERE id = ?;", id); err != nil {
		return model.LocalTunnel{}, fmt.Errorf("delete local tunnel: %w", err)
	}
	return tunnel, nil
}

func SetLocalTunnelError(ctx context.Context, database *sql.DB, id int64, lastError string) error {
	result, err := database.ExecContext(ctx, `
UPDATE local_tunnels
SET last_error = ?
WHERE id = ?;`, strings.TrimSpace(lastError), id)
	if err != nil {
		return fmt.Errorf("set local tunnel error: %w", err)
	}
	return ensureRowsAffected(result, ErrNotFound)
}

type localTunnelScanner interface {
	Scan(dest ...any) error
}

func scanLocalTunnel(scanner localTunnelScanner) (model.LocalTunnel, error) {
	var tunnel model.LocalTunnel
	var enabled int
	var lastError sql.NullString
	var remark sql.NullString
	err := scanner.Scan(
		&tunnel.ID,
		&tunnel.Name,
		&tunnel.ServerConnectionID,
		&tunnel.ServerTunnelID,
		&tunnel.LocalHost,
		&tunnel.LocalPort,
		&enabled,
		&lastError,
		&remark,
		&tunnel.CreatedAt,
		&tunnel.UpdatedAt,
	)
	if err != nil {
		return model.LocalTunnel{}, err
	}
	tunnel.Enabled = enabled == 1
	tunnel.LastError = nullableString(lastError)
	tunnel.Remark = nullableString(remark)
	return tunnel, nil
}
