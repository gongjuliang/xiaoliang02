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

type CreateTunnelKeyParams struct {
	TunnelID   int64
	SecretHash string
	SecretHint string
}

func CreateTunnelKey(ctx context.Context, database *sql.DB, params CreateTunnelKeyParams) (model.TunnelKey, error) {
	result, err := database.ExecContext(ctx, `
INSERT INTO tunnel_keys(tunnel_id, secret_hash, secret_hint, status, online_status)
VALUES(?, ?, ?, 'enabled', 'offline');`, params.TunnelID, params.SecretHash, params.SecretHint)
	if err != nil {
		return model.TunnelKey{}, mapSQLiteError("create tunnel key", err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		return model.TunnelKey{}, fmt.Errorf("get created tunnel key id: %w", err)
	}
	return GetTunnelKeyByID(ctx, database, id)
}

func GetTunnelKeyByID(ctx context.Context, database *sql.DB, id int64) (model.TunnelKey, error) {
	row := database.QueryRowContext(ctx, `
SELECT id, tunnel_id, secret_hash, secret_hint, status, online_status, last_ip, last_seen_at, created_at, updated_at
FROM tunnel_keys
WHERE id = ?;`, id)
	key, err := scanTunnelKey(row)
	if errors.Is(err, sql.ErrNoRows) {
		return model.TunnelKey{}, ErrNotFound
	}
	if err != nil {
		return model.TunnelKey{}, err
	}
	return key, nil
}

func GetTunnelKeyByTunnelID(ctx context.Context, database *sql.DB, tunnelID int64) (model.TunnelKey, error) {
	row := database.QueryRowContext(ctx, `
SELECT id, tunnel_id, secret_hash, secret_hint, status, online_status, last_ip, last_seen_at, created_at, updated_at
FROM tunnel_keys
WHERE tunnel_id = ?;`, tunnelID)
	key, err := scanTunnelKey(row)
	if errors.Is(err, sql.ErrNoRows) {
		return model.TunnelKey{}, ErrNotFound
	}
	if err != nil {
		return model.TunnelKey{}, err
	}
	return key, nil
}

func AuthenticateTunnelSecret(ctx context.Context, database *sql.DB, secret string) (model.TunnelKey, error) {
	rows, err := database.QueryContext(ctx, `
SELECT id, tunnel_id, secret_hash, secret_hint, status, online_status, last_ip, last_seen_at, created_at, updated_at
FROM tunnel_keys
WHERE status = 'enabled';`)
	if err != nil {
		return model.TunnelKey{}, fmt.Errorf("query enabled tunnel keys: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		key, err := scanTunnelKey(rows)
		if err != nil {
			return model.TunnelKey{}, err
		}
		if auth.CheckPassword(secret, key.SecretHash) {
			return key, nil
		}
	}
	if err := rows.Err(); err != nil {
		return model.TunnelKey{}, fmt.Errorf("iterate enabled tunnel keys: %w", err)
	}
	return model.TunnelKey{}, ErrNotFound
}

func MarkTunnelKeyOnline(ctx context.Context, database *sql.DB, tunnelID int64, ip string) error {
	result, err := database.ExecContext(ctx, `
UPDATE tunnel_keys
SET online_status = 'online', last_ip = ?, last_seen_at = datetime('now')
WHERE tunnel_id = ? AND status = 'enabled';`, ip, tunnelID)
	if err != nil {
		return fmt.Errorf("mark tunnel key online: %w", err)
	}
	return ensureRowsAffected(result, ErrNotFound)
}

func MarkTunnelKeyHeartbeat(ctx context.Context, database *sql.DB, tunnelID int64) error {
	result, err := database.ExecContext(ctx, `
UPDATE tunnel_keys
SET online_status = 'online', last_seen_at = datetime('now')
WHERE tunnel_id = ? AND status = 'enabled';`, tunnelID)
	if err != nil {
		return fmt.Errorf("mark tunnel key heartbeat: %w", err)
	}
	return ensureRowsAffected(result, ErrNotFound)
}

func MarkTunnelKeyOffline(ctx context.Context, database *sql.DB, tunnelID int64) error {
	result, err := database.ExecContext(ctx, `
UPDATE tunnel_keys
SET online_status = 'offline', last_seen_at = datetime('now')
WHERE tunnel_id = ?;`, tunnelID)
	if err != nil {
		return fmt.Errorf("mark tunnel key offline: %w", err)
	}
	return ensureRowsAffected(result, ErrNotFound)
}

func SetTunnelKeyStatus(ctx context.Context, database *sql.DB, tunnelID int64, status model.TunnelKeyStatus) (model.TunnelKey, error) {
	result, err := database.ExecContext(ctx, `
UPDATE tunnel_keys
SET status = ?, online_status = CASE WHEN ? = 'disabled' THEN 'offline' ELSE online_status END
WHERE tunnel_id = ?;`, status, status, tunnelID)
	if err != nil {
		return model.TunnelKey{}, mapSQLiteError("set tunnel key status", err)
	}
	if err := ensureRowsAffected(result, ErrNotFound); err != nil {
		return model.TunnelKey{}, err
	}
	return GetTunnelKeyByTunnelID(ctx, database, tunnelID)
}

func RotateTunnelSecret(ctx context.Context, database *sql.DB, tunnelID int64, secretHash string, secretHint string) (model.TunnelKey, error) {
	result, err := database.ExecContext(ctx, `
UPDATE tunnel_keys
SET secret_hash = ?, secret_hint = ?, online_status = 'offline'
WHERE tunnel_id = ?;`, secretHash, secretHint, tunnelID)
	if err != nil {
		return model.TunnelKey{}, mapSQLiteError("rotate tunnel secret", err)
	}
	if err := ensureRowsAffected(result, ErrNotFound); err != nil {
		return model.TunnelKey{}, err
	}
	return GetTunnelKeyByTunnelID(ctx, database, tunnelID)
}

func MarkTunnelUnavailable(ctx context.Context, database *sql.DB, tunnelID int64, reason string) error {
	_, err := database.ExecContext(ctx, `
UPDATE tunnels
SET status = 'error', last_error = ?
WHERE id = ? AND status = 'running';`, strings.TrimSpace(reason), tunnelID)
	if err != nil {
		return fmt.Errorf("mark tunnel unavailable: %w", err)
	}
	return nil
}

type tunnelKeyScanner interface {
	Scan(dest ...any) error
}

func scanTunnelKey(scanner tunnelKeyScanner) (model.TunnelKey, error) {
	var key model.TunnelKey
	var lastIP sql.NullString
	var lastSeenAt sql.NullString
	err := scanner.Scan(
		&key.ID,
		&key.TunnelID,
		&key.SecretHash,
		&key.SecretHint,
		&key.Status,
		&key.OnlineStatus,
		&lastIP,
		&lastSeenAt,
		&key.CreatedAt,
		&key.UpdatedAt,
	)
	if err != nil {
		return model.TunnelKey{}, err
	}
	key.LastIP = nullableString(lastIP)
	key.LastSeenAt = nullableString(lastSeenAt)
	return key, nil
}
