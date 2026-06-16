// Package db 提供客户端(Client)的数据库CRUD操作。
// 包括客户端列表查询、按ID查询、密钥认证（SM3加盐哈希遍历验证）、
// 创建/更新/状态变更/密钥轮换等完整客户端管理功能。
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

// CreateClientParams 创建客户端的参数结构体。
type CreateClientParams struct {
	Name       string // 客户端名称
	SecretHash string // 密钥SM3加盐哈希值
	SecretHint string // 密钥提示摘要
	Remark     string // 备注信息
}

// UpdateClientParams 更新客户端的参数结构体。
type UpdateClientParams struct {
	Name   string // 客户端名称
	Remark string // 备注信息
}

// ListClients 分页查询客户端列表（按ID倒序排列）。
// 参数ctx：上下文。database：数据库连接。limit：每页条数。offset：偏移量。
// 返回值：客户端列表、总数和错误。
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

// GetClientByID 按ID查询单个客户端，未找到时返回ErrNotFound。
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

// AuthenticateClientSecret 遍历已启用的客户端，使用SM3加盐哈希验证密钥匹配。
// 认证成功后返回匹配的客户端信息，未匹配返回ErrNotFound。
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

// CreateClient 创建新客户端，初始状态为enabled/offline，返回完整客户端信息。
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

// MarkClientOnline 将客户端标记为在线状态，记录最后连接IP和时间。
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

// MarkClientHeartbeat 更新客户端心跳时间（保持online状态）。
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

// MarkClientOffline 将客户端标记为离线状态。
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

// MarkClientTunnelsUnavailable 将客户端所有运行中的隧道标记为error状态。
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

// UpdateClient 更新客户端名称和备注，返回更新后的完整客户端信息。
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

// SetClientStatus 设置客户端启用/禁用状态，返回更新后的客户端信息。
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

// RotateClientSecret 轮换客户端密钥，更新哈希和提示信息，密钥明文不存储于此函数。
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

// clientScanner 扫描器接口，抽象sql.Row和sql.Rows的Scan方法。
type clientScanner interface {
	Scan(dest ...any) error
}

// scanClient 从扫描器中读取一行客户端数据并填充到结构体中。
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

// nullableString 将sql.NullString转为普通字符串，NULL转为空字符串。
func nullableString(value sql.NullString) string {
	if !value.Valid {
		return ""
	}
	return value.String
}

// mapSQLiteError 将SQLite错误映射为业务错误：unique constraint→ErrConflict。
func mapSQLiteError(action string, err error) error {
	if strings.Contains(strings.ToLower(err.Error()), "unique constraint") {
		return fmt.Errorf("%s: %w", action, ErrConflict)
	}
	return fmt.Errorf("%s: %w", action, err)
}

// ensureRowsAffected 检查SQL操作影响行数，0行时返回指定的notFound错误。
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
