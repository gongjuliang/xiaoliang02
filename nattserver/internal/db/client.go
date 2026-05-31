package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"nattserver/internal/auth"
	"nattserver/internal/model"
)

type CreateClientParams struct {
	Name       string
	SecretHash string
	SecretHint string
	Remark     string
}

type UpdateClientParams struct {
	Name   string
	Remark string
}

func ListClients(ctx context.Context, database *sql.DB, limit int, offset int) ([]model.Client, int64, error) {
	var total int64
	if err := database.QueryRowContext(ctx, "SELECT COUNT(1) FROM clients").Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count clients: %w", err)
	}

	rows, err := database.QueryContext(ctx, `
SELECT id, name, secret_hash, secret_hint, status, online_status, last_ip, last_seen_at, remark, created_at, updated_at
FROM clients
ORDER BY id DESC
LIMIT ? OFFSET ?;`, limit, offset)
	if err != nil {
		return nil, 0, fmt.Errorf("list clients: %w", err)
	}
	defer rows.Close()

	var clients []model.Client
	for rows.Next() {
		client, err := scanClient(rows)
		if err != nil {
			return nil, 0, err
		}
		clients = append(clients, client)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("iterate clients: %w", err)
	}
	return clients, total, nil
}

func GetClientByID(ctx context.Context, database *sql.DB, id int64) (model.Client, error) {
	row := database.QueryRowContext(ctx, `
SELECT id, name, secret_hash, secret_hint, status, online_status, last_ip, last_seen_at, remark, created_at, updated_at
FROM clients
WHERE id = ?;`, id)
	client, err := scanClient(row)
	if errors.Is(err, sql.ErrNoRows) {
		return model.Client{}, ErrNotFound
	}
	if err != nil {
		return model.Client{}, err
	}
	return client, nil
}

func AuthenticateClientSecret(ctx context.Context, database *sql.DB, secret string) (model.Client, error) {
	rows, err := database.QueryContext(ctx, `
SELECT id, name, secret_hash, secret_hint, status, online_status, last_ip, last_seen_at, remark, created_at, updated_at
FROM clients
WHERE status = 'enabled';`)
	if err != nil {
		return model.Client{}, fmt.Errorf("query enabled clients: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		client, err := scanClient(rows)
		if err != nil {
			return model.Client{}, err
		}
		if auth.CheckPassword(secret, client.SecretHash) {
			return client, nil
		}
	}
	if err := rows.Err(); err != nil {
		return model.Client{}, fmt.Errorf("iterate enabled clients: %w", err)
	}
	return model.Client{}, ErrNotFound
}

func CreateClient(ctx context.Context, database *sql.DB, params CreateClientParams) (model.Client, error) {
	result, err := database.ExecContext(ctx, `
INSERT INTO clients(name, secret_hash, secret_hint, status, online_status, remark)
VALUES(?, ?, ?, 'enabled', 'offline', ?);`, params.Name, params.SecretHash, params.SecretHint, params.Remark)
	if err != nil {
		return model.Client{}, mapSQLiteError("create client", err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		return model.Client{}, fmt.Errorf("get created client id: %w", err)
	}
	return GetClientByID(ctx, database, id)
}

func MarkClientOnline(ctx context.Context, database *sql.DB, id int64, ip string) error {
	result, err := database.ExecContext(ctx, `
UPDATE clients
SET online_status = 'online', last_ip = ?, last_seen_at = datetime('now')
WHERE id = ?;`, ip, id)
	if err != nil {
		return fmt.Errorf("mark client online: %w", err)
	}
	return ensureRowsAffected(result, ErrNotFound)
}

func MarkClientHeartbeat(ctx context.Context, database *sql.DB, id int64) error {
	result, err := database.ExecContext(ctx, `
UPDATE clients
SET online_status = 'online', last_seen_at = datetime('now')
WHERE id = ?;`, id)
	if err != nil {
		return fmt.Errorf("mark client heartbeat: %w", err)
	}
	return ensureRowsAffected(result, ErrNotFound)
}

func MarkClientOffline(ctx context.Context, database *sql.DB, id int64) error {
	result, err := database.ExecContext(ctx, `
UPDATE clients
SET online_status = 'offline', last_seen_at = datetime('now')
WHERE id = ?;`, id)
	if err != nil {
		return fmt.Errorf("mark client offline: %w", err)
	}
	return ensureRowsAffected(result, ErrNotFound)
}

func MarkClientTunnelsUnavailable(ctx context.Context, database *sql.DB, clientID int64, reason string) error {
	_, err := database.ExecContext(ctx, `
UPDATE tunnels
SET status = 'error', last_error = ?
WHERE client_id = ? AND status = 'running';`, reason, clientID)
	if err != nil {
		return fmt.Errorf("mark client tunnels unavailable: %w", err)
	}
	return nil
}

func UpdateClient(ctx context.Context, database *sql.DB, id int64, params UpdateClientParams) (model.Client, error) {
	result, err := database.ExecContext(ctx, `
UPDATE clients
SET name = ?, remark = ?
WHERE id = ?;`, params.Name, params.Remark, id)
	if err != nil {
		return model.Client{}, mapSQLiteError("update client", err)
	}
	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return model.Client{}, fmt.Errorf("get update rows affected: %w", err)
	}
	if rowsAffected == 0 {
		return model.Client{}, ErrNotFound
	}
	return GetClientByID(ctx, database, id)
}

func SetClientStatus(ctx context.Context, database *sql.DB, id int64, status model.ClientStatus) (model.Client, error) {
	result, err := database.ExecContext(ctx, `
UPDATE clients
SET status = ?
WHERE id = ?;`, status, id)
	if err != nil {
		return model.Client{}, mapSQLiteError("set client status", err)
	}
	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return model.Client{}, fmt.Errorf("get status rows affected: %w", err)
	}
	if rowsAffected == 0 {
		return model.Client{}, ErrNotFound
	}
	return GetClientByID(ctx, database, id)
}

func RotateClientSecret(ctx context.Context, database *sql.DB, id int64, secretHash string, secretHint string) (model.Client, error) {
	result, err := database.ExecContext(ctx, `
UPDATE clients
SET secret_hash = ?, secret_hint = ?
WHERE id = ?;`, secretHash, secretHint, id)
	if err != nil {
		return model.Client{}, mapSQLiteError("rotate client secret", err)
	}
	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return model.Client{}, fmt.Errorf("get rotate rows affected: %w", err)
	}
	if rowsAffected == 0 {
		return model.Client{}, ErrNotFound
	}
	return GetClientByID(ctx, database, id)
}

type clientScanner interface {
	Scan(dest ...any) error
}

func scanClient(scanner clientScanner) (model.Client, error) {
	var client model.Client
	var lastIP sql.NullString
	var lastSeenAt sql.NullString
	var remark sql.NullString
	err := scanner.Scan(
		&client.ID,
		&client.Name,
		&client.SecretHash,
		&client.SecretHint,
		&client.Status,
		&client.OnlineStatus,
		&lastIP,
		&lastSeenAt,
		&remark,
		&client.CreatedAt,
		&client.UpdatedAt,
	)
	if err != nil {
		return model.Client{}, err
	}
	client.LastIP = nullableString(lastIP)
	client.LastSeenAt = nullableString(lastSeenAt)
	client.Remark = nullableString(remark)
	return client, nil
}

func nullableString(value sql.NullString) string {
	if !value.Valid {
		return ""
	}
	return value.String
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
