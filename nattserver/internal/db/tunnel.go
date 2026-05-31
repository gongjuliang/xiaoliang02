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
	where := ""
	args := []any{}
	if clientID > 0 {
		where = "WHERE client_id = ?"
		args = append(args, clientID)
	}

	var total int64
	countQuery := "SELECT COUNT(1) FROM tunnels " + where
	if err := database.QueryRowContext(ctx, countQuery, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count tunnels: %w", err)
	}

	queryArgs := append(args, limit, offset)
	rows, err := database.QueryContext(ctx, `
SELECT id, name, client_id, protocol, local_host, local_port, remote_host, remote_port, status, auto_start, last_error, remark, created_at, updated_at
FROM tunnels
`+where+`
ORDER BY id DESC
LIMIT ? OFFSET ?;`, queryArgs...)
	if err != nil {
		return nil, 0, fmt.Errorf("list tunnels: %w", err)
	}
	defer rows.Close()

	var tunnels []model.Tunnel
	for rows.Next() {
		tunnel, err := scanTunnel(rows)
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
SELECT id, name, client_id, protocol, local_host, local_port, remote_host, remote_port, status, auto_start, last_error, remark, created_at, updated_at
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

func ListAutoStartTunnelsByClient(ctx context.Context, database *sql.DB, clientID int64) ([]model.Tunnel, error) {
	rows, err := database.QueryContext(ctx, `
SELECT id, name, client_id, protocol, local_host, local_port, remote_host, remote_port, status, auto_start, last_error, remark, created_at, updated_at
FROM tunnels
WHERE client_id = ? AND auto_start = 1
ORDER BY id ASC;`, clientID)
	if err != nil {
		return nil, fmt.Errorf("list client auto-start tunnels: %w", err)
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

func CreateTunnel(ctx context.Context, database *sql.DB, params CreateTunnelParams) (model.Tunnel, error) {
	tx, err := database.BeginTx(ctx, nil)
	if err != nil {
		return model.Tunnel{}, fmt.Errorf("begin create tunnel: %w", err)
	}
	defer tx.Rollback()

	result, err := tx.ExecContext(ctx, `
INSERT INTO tunnels(name, client_id, protocol, local_host, local_port, remote_host, remote_port, status, auto_start, remark)
VALUES(?, ?, ?, ?, ?, ?, ?, 'stopped', ?, ?);`,
		params.Name,
		params.ClientID,
		params.Protocol,
		params.LocalHost,
		params.LocalPort,
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
	if err := tx.Commit(); err != nil {
		return model.Tunnel{}, fmt.Errorf("commit create tunnel: %w", err)
	}
	return GetTunnelByID(ctx, database, id)
}

func UpdateTunnel(ctx context.Context, database *sql.DB, id int64, params UpdateTunnelParams) (model.Tunnel, error) {
	result, err := database.ExecContext(ctx, `
UPDATE tunnels
SET name = ?, client_id = ?, protocol = ?, local_host = ?, local_port = ?, remote_host = ?, remote_port = ?, auto_start = ?, remark = ?, last_error = NULL
WHERE id = ?;`,
		params.Name,
		params.ClientID,
		params.Protocol,
		params.LocalHost,
		params.LocalPort,
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
		&tunnel.LocalHost,
		&tunnel.LocalPort,
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

func boolToInt(value bool) int {
	if value {
		return 1
	}
	return 0
}
