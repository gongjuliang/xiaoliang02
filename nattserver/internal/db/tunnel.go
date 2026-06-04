package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"nattserver/internal/model"
)

type CreateTunnelParams struct {
	Name       string
	ClientID   int64
	Protocol   model.TunnelProtocol
	LocalHost  string
	LocalPort  int
	RemoteHost string
	RemotePort int
	AutoStart  bool
	Remark     string
}

type UpdateTunnelParams struct {
	Name       string
	ClientID   int64
	Protocol   model.TunnelProtocol
	LocalHost  string
	LocalPort  int
	RemoteHost string
	RemotePort int
	AutoStart  bool
	Remark     string
}

func ListTunnels(ctx context.Context, database *sql.DB, clientID int64, limit int, offset int) ([]model.Tunnel, int64, error) {
	var total int64
	if err := database.QueryRowContext(ctx, "SELECT COUNT(1) FROM tunnels").Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count tunnels: %w", err)
	}

	rows, err := database.QueryContext(ctx, `
SELECT t.id, t.name, COALESCE(t.client_id, 0), t.protocol, t.remote_host, t.remote_port, t.status, t.auto_start, t.last_error,
       COALESCE(k.secret_plain, ''), COALESCE(k.secret_hint, ''),
       COALESCE(ts.bytes_in, 0), COALESCE(ts.bytes_out, 0),
       t.remark, t.created_at, t.updated_at
FROM tunnels t
LEFT JOIN tunnel_keys k ON k.tunnel_id = t.id
LEFT JOIN traffic_stats ts ON ts.tunnel_id = t.id
ORDER BY t.id DESC
LIMIT ? OFFSET ?;`, limit, offset)
	if err != nil {
		return nil, 0, fmt.Errorf("list tunnels: %w", err)
	}
	defer rows.Close()

	var tunnels []model.Tunnel
	for rows.Next() {
		tunnel, err := scanTunnelWithSecret(rows)
		if err != nil {
			return nil, 0, err
		}
		tunnels = append(tunnels, tunnel)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("iterate tunnels: %w", err)
	}
	return tunnels, total, nil
}

func GetTunnelByID(ctx context.Context, database *sql.DB, id int64) (model.Tunnel, error) {
	row := database.QueryRowContext(ctx, `
SELECT id, name, COALESCE(client_id, 0), protocol, remote_host, remote_port, status, auto_start, last_error, remark, created_at, updated_at
FROM tunnels
WHERE id = ?;`, id)
	tunnel, err := scanTunnel(row)
	if errors.Is(err, sql.ErrNoRows) {
		return model.Tunnel{}, ErrNotFound
	}
	if err != nil {
		return model.Tunnel{}, err
	}
	return tunnel, nil
}

func ListAutoStartOnlineTunnels(ctx context.Context, database *sql.DB) ([]model.Tunnel, error) {
	rows, err := database.QueryContext(ctx, `
SELECT t.id, t.name, COALESCE(t.client_id, 0), t.protocol, t.remote_host, t.remote_port, t.status, t.auto_start, t.last_error, t.remark, t.created_at, t.updated_at
FROM tunnels t
JOIN tunnel_keys k ON k.tunnel_id = t.id
WHERE t.auto_start = 1 AND k.status = 'enabled' AND k.online_status = 'online'
ORDER BY t.id ASC;`)
	if err != nil {
		return nil, fmt.Errorf("list auto-start online tunnels: %w", err)
	}
	defer rows.Close()

	var tunnels []model.Tunnel
	for rows.Next() {
		tunnel, err := scanTunnel(rows)
		if err != nil {
			return nil, err
		}
		tunnels = append(tunnels, tunnel)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate client auto-start tunnels: %w", err)
	}
	return tunnels, nil
}

func ListAutoStartTunnelsByClient(ctx context.Context, database *sql.DB, clientID int64) ([]model.Tunnel, error) {
	return ListAutoStartOnlineTunnels(ctx, database)
}

func CreateTunnel(ctx context.Context, database *sql.DB, params CreateTunnelParams) (model.Tunnel, error) {
	var legacyClient model.Client
	if params.ClientID > 0 {
		var err error
		legacyClient, err = GetClientByID(ctx, database, params.ClientID)
		if err != nil {
			return model.Tunnel{}, err
		}
	}

	tx, err := database.BeginTx(ctx, nil)
	if err != nil {
		return model.Tunnel{}, fmt.Errorf("begin create tunnel: %w", err)
	}
	defer tx.Rollback()

	result, err := tx.ExecContext(ctx, `
INSERT INTO tunnels(name, client_id, protocol, remote_host, remote_port, status, auto_start, remark)
VALUES(?, ?, ?, ?, ?, 'stopped', ?, ?);`,
		params.Name,
		params.ClientID,
		params.Protocol,
		params.RemoteHost,
		params.RemotePort,
		boolToInt(params.AutoStart),
		params.Remark,
	)
	if err != nil {
		return model.Tunnel{}, mapSQLiteError("create tunnel", err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		return model.Tunnel{}, fmt.Errorf("get created tunnel id: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO traffic_stats(tunnel_id, connection_count, active_connections, bytes_in, bytes_out)
VALUES(?, 0, 0, 0, 0);`, id); err != nil {
		return model.Tunnel{}, fmt.Errorf("create tunnel traffic stats: %w", err)
	}
	if params.ClientID > 0 {
		if _, err := tx.ExecContext(ctx, `
INSERT INTO tunnel_keys(tunnel_id, secret_hash, secret_hint, status, online_status)
VALUES(?, ?, ?, 'enabled', 'offline');`, id, legacyClient.SecretHash, legacyClient.SecretHint); err != nil {
			return model.Tunnel{}, mapSQLiteError("create legacy tunnel key", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return model.Tunnel{}, fmt.Errorf("commit create tunnel: %w", err)
	}
	return GetTunnelByID(ctx, database, id)
}

func UpdateTunnel(ctx context.Context, database *sql.DB, id int64, params UpdateTunnelParams) (model.Tunnel, error) {
	result, err := database.ExecContext(ctx, `
UPDATE tunnels
SET name = ?, protocol = ?, remote_host = ?, remote_port = ?, auto_start = ?, remark = ?, last_error = NULL
WHERE id = ?;`,
		params.Name,
		params.Protocol,
		params.RemoteHost,
		params.RemotePort,
		boolToInt(params.AutoStart),
		params.Remark,
		id,
	)
	if err != nil {
		return model.Tunnel{}, mapSQLiteError("update tunnel", err)
	}
	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return model.Tunnel{}, fmt.Errorf("get update tunnel rows affected: %w", err)
	}
	if rowsAffected == 0 {
		return model.Tunnel{}, ErrNotFound
	}
	return GetTunnelByID(ctx, database, id)
}

func DeleteTunnel(ctx context.Context, database *sql.DB, id int64) (model.Tunnel, error) {
	tunnel, err := GetTunnelByID(ctx, database, id)
	if err != nil {
		return model.Tunnel{}, err
	}
	if _, err := database.ExecContext(ctx, "DELETE FROM tunnels WHERE id = ?;", id); err != nil {
		return model.Tunnel{}, fmt.Errorf("delete tunnel: %w", err)
	}
	return tunnel, nil
}

func SetTunnelStatus(ctx context.Context, database *sql.DB, id int64, status model.TunnelStatus, lastError string) (model.Tunnel, error) {
	var result sql.Result
	var err error
	if lastError == "" {
		result, err = database.ExecContext(ctx, `
UPDATE tunnels
SET status = ?, last_error = NULL
WHERE id = ?;`, status, id)
	} else {
		result, err = database.ExecContext(ctx, `
UPDATE tunnels
SET status = ?, last_error = ?
WHERE id = ?;`, status, lastError, id)
	}
	if err != nil {
		return model.Tunnel{}, mapSQLiteError("set tunnel status", err)
	}
	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return model.Tunnel{}, fmt.Errorf("get tunnel status rows affected: %w", err)
	}
	if rowsAffected == 0 {
		return model.Tunnel{}, ErrNotFound
	}
	return GetTunnelByID(ctx, database, id)
}

type tunnelScanner interface {
	Scan(dest ...any) error
}

func scanTunnel(scanner tunnelScanner) (model.Tunnel, error) {
	var tunnel model.Tunnel
	var autoStart int
	var lastError sql.NullString
	var remark sql.NullString
	err := scanner.Scan(
		&tunnel.ID,
		&tunnel.Name,
		&tunnel.ClientID,
		&tunnel.Protocol,
		&tunnel.RemoteHost,
		&tunnel.RemotePort,
		&tunnel.Status,
		&autoStart,
		&lastError,
		&remark,
		&tunnel.CreatedAt,
		&tunnel.UpdatedAt,
	)
	if err != nil {
		return model.Tunnel{}, err
	}
	tunnel.AutoStart = autoStart == 1
	tunnel.LastError = nullableString(lastError)
	tunnel.Remark = nullableString(remark)
	return tunnel, nil
}

func scanTunnelWithSecret(scanner tunnelScanner) (model.Tunnel, error) {
	var tunnel model.Tunnel
	var autoStart int
	var lastError sql.NullString
	var secret sql.NullString
	var secretHint sql.NullString
	var remark sql.NullString
	err := scanner.Scan(
		&tunnel.ID,
		&tunnel.Name,
		&tunnel.ClientID,
		&tunnel.Protocol,
		&tunnel.RemoteHost,
		&tunnel.RemotePort,
		&tunnel.Status,
		&autoStart,
		&lastError,
		&secret,
		&secretHint,
		&tunnel.BytesIn,
		&tunnel.BytesOut,
		&remark,
		&tunnel.CreatedAt,
		&tunnel.UpdatedAt,
	)
	if err != nil {
		return model.Tunnel{}, err
	}
	tunnel.AutoStart = autoStart == 1
	tunnel.LastError = nullableString(lastError)
	tunnel.Secret = nullableString(secret)
	tunnel.SecretHint = nullableString(secretHint)
	tunnel.Remark = nullableString(remark)
	return tunnel, nil
}

func boolToInt(value bool) int {
	if value {
		return 1
	}
	return 0
}
