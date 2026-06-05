package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"nattuser/internal/model"
)

type CreateServerConnectionParams struct {
	Name         string
	ServerHost   string
	ServerPort   int
	DataPort     int
	RemotePort   int
	ClientSecret string
	LocalHost    string
	LocalPort    int
	AutoStart    bool
	Remark       string
}

type UpdateServerConnectionParams struct {
	Name         string
	ServerHost   string
	ServerPort   int
	DataPort     int
	RemotePort   int
	ClientSecret string
	LocalHost    string
	LocalPort    int
	AutoStart    bool
	Remark       string
}

func ListServerConnections(ctx context.Context, database *sql.DB, limit int, offset int) ([]model.ServerConnection, int64, error) {
	var total int64
	if err := database.QueryRowContext(ctx, "SELECT COUNT(1) FROM tunnel_connections").Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count server connections: %w", err)
	}

	rows, err := database.QueryContext(ctx, `
SELECT id, name, server_host, server_port, data_port, remote_port, use_tls, client_secret, local_host, local_port, status, auto_start, last_error, remark, created_at, updated_at
FROM tunnel_connections
ORDER BY id DESC
LIMIT ? OFFSET ?;`, limit, offset)
	if err != nil {
		return nil, 0, fmt.Errorf("list server connections: %w", err)
	}
	defer rows.Close()

	var connections []model.ServerConnection
	for rows.Next() {
		connection, err := scanServerConnection(rows)
		if err != nil {
			return nil, 0, err
		}
		connections = append(connections, connection)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("iterate server connections: %w", err)
	}
	return connections, total, nil
}

func ListControlServerConnections(ctx context.Context, database *sql.DB) ([]model.ServerConnection, error) {
	rows, err := database.QueryContext(ctx, `
SELECT id, name, server_host, server_port, data_port, remote_port, use_tls, client_secret, local_host, local_port, status, auto_start, last_error, remark, created_at, updated_at
FROM tunnel_connections
WHERE auto_start = 1 OR status IN ('connecting', 'connected', 'error')
ORDER BY id ASC;`)
	if err != nil {
		return nil, fmt.Errorf("list control server connections: %w", err)
	}
	defer rows.Close()

	var connections []model.ServerConnection
	for rows.Next() {
		connection, err := scanServerConnection(rows)
		if err != nil {
			return nil, err
		}
		connections = append(connections, connection)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate control server connections: %w", err)
	}
	return connections, nil
}

func GetServerConnectionByID(ctx context.Context, database *sql.DB, id int64) (model.ServerConnection, error) {
	row := database.QueryRowContext(ctx, `
SELECT id, name, server_host, server_port, data_port, remote_port, use_tls, client_secret, local_host, local_port, status, auto_start, last_error, remark, created_at, updated_at
FROM tunnel_connections
WHERE id = ?;`, id)
	connection, err := scanServerConnection(row)
	if errors.Is(err, sql.ErrNoRows) {
		return model.ServerConnection{}, ErrNotFound
	}
	if err != nil {
		return model.ServerConnection{}, err
	}
	return connection, nil
}

func CreateServerConnection(ctx context.Context, database *sql.DB, params CreateServerConnectionParams) (model.ServerConnection, error) {
	result, err := database.ExecContext(ctx, `
INSERT INTO tunnel_connections(name, server_host, server_port, data_port, remote_port, use_tls, client_secret, local_host, local_port, status, auto_start, remark)
VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, 'stopped', ?, ?);`,
		params.Name,
		params.ServerHost,
		params.ServerPort,
		params.DataPort,
		params.RemotePort,
		0,
		params.ClientSecret,
		params.LocalHost,
		params.LocalPort,
		boolToInt(params.AutoStart),
		params.Remark,
	)
	if err != nil {
		return model.ServerConnection{}, mapSQLiteError("create server connection", err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		return model.ServerConnection{}, fmt.Errorf("get created server connection id: %w", err)
	}
	return GetServerConnectionByID(ctx, database, id)
}

func UpdateServerConnection(ctx context.Context, database *sql.DB, id int64, params UpdateServerConnectionParams) (model.ServerConnection, error) {
	result, err := database.ExecContext(ctx, `
UPDATE tunnel_connections
SET name = ?, server_host = ?, server_port = ?, data_port = ?, remote_port = ?, use_tls = ?, client_secret = ?, local_host = ?, local_port = ?, auto_start = ?, remark = ?, last_error = NULL
WHERE id = ?;`,
		params.Name,
		params.ServerHost,
		params.ServerPort,
		params.DataPort,
		params.RemotePort,
		0,
		params.ClientSecret,
		params.LocalHost,
		params.LocalPort,
		boolToInt(params.AutoStart),
		params.Remark,
		id,
	)
	if err != nil {
		return model.ServerConnection{}, mapSQLiteError("update server connection", err)
	}
	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return model.ServerConnection{}, fmt.Errorf("get update rows affected: %w", err)
	}
	if rowsAffected == 0 {
		return model.ServerConnection{}, ErrNotFound
	}
	return GetServerConnectionByID(ctx, database, id)
}

func DeleteServerConnection(ctx context.Context, database *sql.DB, id int64) (model.ServerConnection, error) {
	connection, err := GetServerConnectionByID(ctx, database, id)
	if err != nil {
		return model.ServerConnection{}, err
	}
	if _, err := database.ExecContext(ctx, "DELETE FROM tunnel_connections WHERE id = ?;", id); err != nil {
		return model.ServerConnection{}, fmt.Errorf("delete server connection: %w", err)
	}
	return connection, nil
}

func SetServerConnectionStatus(ctx context.Context, database *sql.DB, id int64, status model.ServerConnectionStatus, lastError string) (model.ServerConnection, error) {
	var result sql.Result
	var err error
	if lastError == "" {
		result, err = database.ExecContext(ctx, `
UPDATE tunnel_connections
SET status = ?, last_error = NULL
WHERE id = ?;`, status, id)
	} else {
		result, err = database.ExecContext(ctx, `
UPDATE tunnel_connections
SET status = ?, last_error = ?
WHERE id = ?;`, status, lastError, id)
	}
	if err != nil {
		return model.ServerConnection{}, mapSQLiteError("set server connection status", err)
	}
	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return model.ServerConnection{}, fmt.Errorf("get status rows affected: %w", err)
	}
	if rowsAffected == 0 {
		return model.ServerConnection{}, ErrNotFound
	}
	return GetServerConnectionByID(ctx, database, id)
}

func MarkServerConnectionConnecting(ctx context.Context, database *sql.DB, id int64) error {
	_, err := SetServerConnectionStatus(ctx, database, id, model.ServerConnectionStatusConnecting, "")
	return err
}

func MarkServerConnectionConnected(ctx context.Context, database *sql.DB, id int64) error {
	_, err := SetServerConnectionStatus(ctx, database, id, model.ServerConnectionStatusConnected, "")
	return err
}

func MarkServerConnectionConnectedWithRemotePort(ctx context.Context, database *sql.DB, id int64, remotePort int) error {
	result, err := database.ExecContext(ctx, `
UPDATE tunnel_connections
SET status = 'connected', last_error = NULL, remote_port = ?
WHERE id = ?;`, remotePort, id)
	if err != nil {
		return fmt.Errorf("mark server connection connected with remote port: %w", err)
	}
	return ensureRowsAffected(result, ErrNotFound)
}

func MarkServerConnectionHeartbeat(ctx context.Context, database *sql.DB, id int64) error {
	result, err := database.ExecContext(ctx, `
UPDATE tunnel_connections
SET status = 'connected', last_error = NULL
WHERE id = ?;`, id)
	if err != nil {
		return fmt.Errorf("mark server connection heartbeat: %w", err)
	}
	return ensureRowsAffected(result, ErrNotFound)
}

func MarkServerConnectionError(ctx context.Context, database *sql.DB, id int64, lastError string) error {
	if strings.TrimSpace(lastError) == "" {
		lastError = "control connection error"
	}
	_, err := SetServerConnectionStatus(ctx, database, id, model.ServerConnectionStatusError, lastError)
	return err
}

func MarkServerConnectionStopped(ctx context.Context, database *sql.DB, id int64) error {
	_, err := SetServerConnectionStatus(ctx, database, id, model.ServerConnectionStatusStopped, "")
	return err
}

type serverConnectionScanner interface {
	Scan(dest ...any) error
}

func scanServerConnection(scanner serverConnectionScanner) (model.ServerConnection, error) {
	var connection model.ServerConnection
	var ignoredUseTLS int
	var autoStart int
	var lastError sql.NullString
	var remark sql.NullString
	err := scanner.Scan(
		&connection.ID,
		&connection.Name,
		&connection.ServerHost,
		&connection.ServerPort,
		&connection.DataPort,
		&connection.RemotePort,
		&ignoredUseTLS,
		&connection.ClientSecret,
		&connection.LocalHost,
		&connection.LocalPort,
		&connection.Status,
		&autoStart,
		&lastError,
		&remark,
		&connection.CreatedAt,
		&connection.UpdatedAt,
	)
	if err != nil {
		return model.ServerConnection{}, err
	}
	connection.AutoStart = autoStart == 1
	connection.LastError = nullableString(lastError)
	connection.Remark = nullableString(remark)
	return connection, nil
}

func nullableString(value sql.NullString) string {
	if !value.Valid {
		return ""
	}
	return value.String
}

func boolToInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func mapSQLiteError(action string, err error) error {
	if strings.Contains(strings.ToLower(err.Error()), "unique constraint") {
		return fmt.Errorf("%s: %w", action, ErrConflict)
	}
	return fmt.Errorf("%s: %w", action, err)
}

func ensureRowsAffected(result sql.Result, notFound error) error {
	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("get rows affected: %w", err)
	}
	if rowsAffected == 0 {
		return notFound
	}
	return nil
}
