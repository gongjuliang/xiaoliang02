// Package db 提供隧道密钥(TunnelKey)的数据库CRUD操作。
// 隧道密钥用于分发隧道访问凭证，支持创建、查询（按ID/隧道ID）、
// 密钥认证（SM3加盐哈希验证）、在线状态管理（上线/心跳/离线）、启用/禁用和密钥轮换。
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

// CreateTunnelKeyParams 创建隧道密钥的参数结构体。
type CreateTunnelKeyParams struct {
	TunnelID    int64  // 关联的隧道ID
	SecretHash  string // 密钥SM3加盐哈希值
	SecretHint  string // 密钥提示摘要
	SecretPlain string // 密钥明文（持久化存储供后续查看）
}

// CreateTunnelKey 创建隧道密钥，初始状态enabled/offline，返回完整密钥信息。
func CreateTunnelKey(ctx context.Context, database *sql.DB, params CreateTunnelKeyParams) (model.TunnelKey, error) {
	result, err := database.ExecContext(ctx, `
INSERT INTO tunnel_keys(tunnel_id, secret_hash, secret_hint, secret_plain, status, online_status)
VALUES(?, ?, ?, ?, 'enabled', 'offline');`, params.TunnelID, params.SecretHash, params.SecretHint, params.SecretPlain)
	if err != nil {
		return model.TunnelKey{}, mapSQLiteError("create tunnel key", err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		return model.TunnelKey{}, fmt.Errorf("get created tunnel key id: %w", err)
	}
	return GetTunnelKeyByID(ctx, database, id)
}

// GetTunnelKeyByID 按密钥ID查询单个隧道密钥，未找到返回ErrNotFound。
func GetTunnelKeyByID(ctx context.Context, database *sql.DB, id int64) (model.TunnelKey, error) {
	row := database.QueryRowContext(ctx, `
SELECT id, tunnel_id, secret_hash, secret_hint, secret_plain, status, online_status, last_ip, last_seen_at, created_at, updated_at
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

// GetTunnelKeyByTunnelID 按隧道ID查询关联的密钥，未找到返回ErrNotFound。
func GetTunnelKeyByTunnelID(ctx context.Context, database *sql.DB, tunnelID int64) (model.TunnelKey, error) {
	row := database.QueryRowContext(ctx, `
SELECT id, tunnel_id, secret_hash, secret_hint, secret_plain, status, online_status, last_ip, last_seen_at, created_at, updated_at
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

// AuthenticateTunnelSecret 遍历已启用的隧道密钥，SM3加盐哈希验证密钥匹配。
func AuthenticateTunnelSecret(ctx context.Context, database *sql.DB, secret string) (model.TunnelKey, error) {
	rows, err := database.QueryContext(ctx, `
SELECT id, tunnel_id, secret_hash, secret_hint, secret_plain, status, online_status, last_ip, last_seen_at, created_at, updated_at
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

// MarkTunnelKeyOnline 将隧道密钥标记为在线，记录最后连接IP（同时更新关联的旧版客户端状态）。
func MarkTunnelKeyOnline(ctx context.Context, database *sql.DB, tunnelID int64, ip string) error {
	result, err := database.ExecContext(ctx, `
UPDATE tunnel_keys
SET online_status = 'online', last_ip = ?, last_seen_at = datetime('now')
WHERE tunnel_id = ? AND status = 'enabled';`, ip, tunnelID)
	if err != nil {
		return fmt.Errorf("mark tunnel key online: %w", err)
	}
	if err := ensureRowsAffected(result, ErrNotFound); err != nil {
		return err
	}
	if _, err := database.ExecContext(ctx, `
UPDATE clients
SET online_status = 'online', last_ip = ?, last_seen_at = datetime('now')
WHERE id = (SELECT client_id FROM tunnels WHERE id = ? AND client_id > 0);`, ip, tunnelID); err != nil {
		return fmt.Errorf("mark legacy client online: %w", err)
	}
	return nil
}

// MarkTunnelKeyHeartbeat 更新隧道密钥的心跳时间（保持online状态，同时更新旧版客户端）。
func MarkTunnelKeyHeartbeat(ctx context.Context, database *sql.DB, tunnelID int64) error {
	result, err := database.ExecContext(ctx, `
UPDATE tunnel_keys
SET online_status = 'online', last_seen_at = datetime('now')
WHERE tunnel_id = ? AND status = 'enabled';`, tunnelID)
	if err != nil {
		return fmt.Errorf("mark tunnel key heartbeat: %w", err)
	}
	if err := ensureRowsAffected(result, ErrNotFound); err != nil {
		return err
	}
	if _, err := database.ExecContext(ctx, `
UPDATE clients
SET online_status = 'online', last_seen_at = datetime('now')
WHERE id = (SELECT client_id FROM tunnels WHERE id = ? AND client_id > 0);`, tunnelID); err != nil {
		return fmt.Errorf("mark legacy client heartbeat: %w", err)
	}
	return nil
}

// MarkTunnelKeyOffline 将隧道密钥标记为离线（同时更新旧版客户端离线状态）。
func MarkTunnelKeyOffline(ctx context.Context, database *sql.DB, tunnelID int64) error {
	result, err := database.ExecContext(ctx, `
UPDATE tunnel_keys
SET online_status = 'offline', last_seen_at = datetime('now')
WHERE tunnel_id = ?;`, tunnelID)
	if err != nil {
		return fmt.Errorf("mark tunnel key offline: %w", err)
	}
	if err := ensureRowsAffected(result, ErrNotFound); err != nil {
		return err
	}
	if _, err := database.ExecContext(ctx, `
UPDATE clients
SET online_status = 'offline', last_seen_at = datetime('now')
WHERE id = (SELECT client_id FROM tunnels WHERE id = ? AND client_id > 0);`, tunnelID); err != nil {
		return fmt.Errorf("mark legacy client offline: %w", err)
	}
	return nil
}

// SetTunnelKeyStatus 设置隧道密钥启用/禁用状态，禁用时同步将online_status设为offline。
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

// RotateTunnelSecret 轮换隧道密钥，更新哈希和明文，并将在线状态重置为offline。
func RotateTunnelSecret(ctx context.Context, database *sql.DB, tunnelID int64, secretHash string, secretHint string, secretPlain string) (model.TunnelKey, error) {
	result, err := database.ExecContext(ctx, `
UPDATE tunnel_keys
SET secret_hash = ?, secret_hint = ?, secret_plain = ?, online_status = 'offline'
WHERE tunnel_id = ?;`, secretHash, secretHint, secretPlain, tunnelID)
	if err != nil {
		return model.TunnelKey{}, mapSQLiteError("rotate tunnel secret", err)
	}
	if err := ensureRowsAffected(result, ErrNotFound); err != nil {
		return model.TunnelKey{}, err
	}
	return GetTunnelKeyByTunnelID(ctx, database, tunnelID)
}

// MarkTunnelUnavailable 将运行中的隧道标记为error状态（客户端离线等情况）。
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

// tunnelKeyScanner 隧道密钥扫描器接口。
type tunnelKeyScanner interface {
	Scan(dest ...any) error
}

// scanTunnelKey 从扫描器中读取一行隧道密钥数据。
func scanTunnelKey(scanner tunnelKeyScanner) (model.TunnelKey, error) {
	var key model.TunnelKey
	var lastIP sql.NullString
	var lastSeenAt sql.NullString
	var secretPlain sql.NullString
	err := scanner.Scan(
		&key.ID,
		&key.TunnelID,
		&key.SecretHash,
		&key.SecretHint,
		&secretPlain,
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
	key.SecretPlain = nullableString(secretPlain)
	key.LastIP = nullableString(lastIP)
	key.LastSeenAt = nullableString(lastSeenAt)
	return key, nil
}
