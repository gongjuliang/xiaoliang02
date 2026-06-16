// Package db 提供数据库版本迁移管理功能。
// 通过schema_migrations表追踪已应用的迁移版本，支持新数据库自动建表、
// 旧数据库增量升级、旧密码哈希格式重置、旧隧道密钥和客户端密钥哈希迁移。
package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strings"

	"nattserver/internal/auth"
	"nattserver/internal/logger"
)

// migration 数据库迁移版本定义结构体。
type migration struct {
	Version int    // 迁移版本号
	Name    string // 迁移名称
	SQL     string // 迁移SQL语句
}

// serverMigrations 服务端数据库迁移版本列表（4个版本）。
var serverMigrations = []migration{
	{
		Version: 1,
		Name:    "initial_schema",
		SQL: `
CREATE TABLE IF NOT EXISTS users (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	username TEXT UNIQUE NOT NULL,
	password_hash TEXT NOT NULL,
	role TEXT NOT NULL DEFAULT 'admin',
	created_at DATETIME NOT NULL DEFAULT (datetime('now')),
	updated_at DATETIME NOT NULL DEFAULT (datetime('now'))
);

CREATE TABLE IF NOT EXISTS tunnels (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	name TEXT NOT NULL,
	protocol TEXT NOT NULL DEFAULT 'tcp',
	remote_host TEXT NOT NULL DEFAULT '0.0.0.0',
	remote_port INTEGER UNIQUE NOT NULL,
	status TEXT NOT NULL DEFAULT 'stopped',
	auto_start INTEGER NOT NULL DEFAULT 0,
	last_error TEXT,
	remark TEXT,
	created_at DATETIME NOT NULL DEFAULT (datetime('now')),
	updated_at DATETIME NOT NULL DEFAULT (datetime('now'))
);

CREATE TABLE IF NOT EXISTS tunnel_keys (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	tunnel_id INTEGER UNIQUE NOT NULL,
	secret_hash TEXT UNIQUE NOT NULL,
	secret_hint TEXT NOT NULL,
	status TEXT NOT NULL DEFAULT 'enabled',
	online_status TEXT NOT NULL DEFAULT 'offline',
	last_ip TEXT,
	last_seen_at DATETIME,
	created_at DATETIME NOT NULL DEFAULT (datetime('now')),
	updated_at DATETIME NOT NULL DEFAULT (datetime('now')),
	FOREIGN KEY (tunnel_id) REFERENCES tunnels(id) ON DELETE CASCADE
);

CREATE TABLE IF NOT EXISTS audit_logs (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	actor TEXT NOT NULL,
	action TEXT NOT NULL,
	target_type TEXT,
	target_id TEXT,
	content TEXT NOT NULL,
	ip TEXT,
	created_at DATETIME NOT NULL DEFAULT (datetime('now'))
);

CREATE TABLE IF NOT EXISTS settings (
	key TEXT PRIMARY KEY,
	value TEXT NOT NULL,
	updated_at DATETIME NOT NULL DEFAULT (datetime('now'))
);

CREATE TABLE IF NOT EXISTS traffic_stats (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	tunnel_id INTEGER NOT NULL,
	connection_count INTEGER NOT NULL DEFAULT 0,
	active_connections INTEGER NOT NULL DEFAULT 0,
	bytes_in INTEGER NOT NULL DEFAULT 0,
	bytes_out INTEGER NOT NULL DEFAULT 0,
	updated_at DATETIME NOT NULL DEFAULT (datetime('now')),
	FOREIGN KEY (tunnel_id) REFERENCES tunnels(id) ON DELETE CASCADE
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_traffic_stats_tunnel_id ON traffic_stats(tunnel_id);
CREATE INDEX IF NOT EXISTS idx_tunnel_keys_online_status ON tunnel_keys(online_status);
CREATE INDEX IF NOT EXISTS idx_audit_logs_created_at ON audit_logs(created_at);

CREATE TRIGGER IF NOT EXISTS trg_users_updated_at
AFTER UPDATE ON users
FOR EACH ROW
WHEN NEW.updated_at = OLD.updated_at
BEGIN
	UPDATE users SET updated_at = datetime('now') WHERE id = OLD.id;
END;

CREATE TRIGGER IF NOT EXISTS trg_tunnel_keys_updated_at
AFTER UPDATE ON tunnel_keys
FOR EACH ROW
WHEN NEW.updated_at = OLD.updated_at
BEGIN
	UPDATE tunnel_keys SET updated_at = datetime('now') WHERE id = OLD.id;
END;

CREATE TRIGGER IF NOT EXISTS trg_tunnels_updated_at
AFTER UPDATE ON tunnels
FOR EACH ROW
WHEN NEW.updated_at = OLD.updated_at
BEGIN
	UPDATE tunnels SET updated_at = datetime('now') WHERE id = OLD.id;
END;

CREATE TRIGGER IF NOT EXISTS trg_settings_updated_at
AFTER UPDATE ON settings
FOR EACH ROW
WHEN NEW.updated_at = OLD.updated_at
BEGIN
	UPDATE settings SET updated_at = datetime('now') WHERE key = OLD.key;
END;

CREATE TRIGGER IF NOT EXISTS trg_traffic_stats_updated_at
AFTER UPDATE ON traffic_stats
FOR EACH ROW
WHEN NEW.updated_at = OLD.updated_at
BEGIN
	UPDATE traffic_stats SET updated_at = datetime('now') WHERE id = OLD.id;
END;
`,
	},
	{
		Version: 2,
		Name:    "reset_to_tunnel_key_model",
		SQL: `
DROP TABLE IF EXISTS traffic_stats;
DROP TABLE IF EXISTS tunnel_keys;
DROP TABLE IF EXISTS tunnels;
DROP TABLE IF EXISTS clients;

CREATE TABLE tunnels (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	name TEXT NOT NULL,
	protocol TEXT NOT NULL DEFAULT 'tcp',
	remote_host TEXT NOT NULL DEFAULT '0.0.0.0',
	remote_port INTEGER UNIQUE NOT NULL,
	status TEXT NOT NULL DEFAULT 'stopped',
	auto_start INTEGER NOT NULL DEFAULT 0,
	last_error TEXT,
	remark TEXT,
	created_at DATETIME NOT NULL DEFAULT (datetime('now')),
	updated_at DATETIME NOT NULL DEFAULT (datetime('now'))
);

CREATE TABLE tunnel_keys (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	tunnel_id INTEGER UNIQUE NOT NULL,
	secret_hash TEXT UNIQUE NOT NULL,
	secret_hint TEXT NOT NULL,
	status TEXT NOT NULL DEFAULT 'enabled',
	online_status TEXT NOT NULL DEFAULT 'offline',
	last_ip TEXT,
	last_seen_at DATETIME,
	created_at DATETIME NOT NULL DEFAULT (datetime('now')),
	updated_at DATETIME NOT NULL DEFAULT (datetime('now')),
	FOREIGN KEY (tunnel_id) REFERENCES tunnels(id) ON DELETE CASCADE
);

CREATE TABLE traffic_stats (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	tunnel_id INTEGER NOT NULL,
	connection_count INTEGER NOT NULL DEFAULT 0,
	active_connections INTEGER NOT NULL DEFAULT 0,
	bytes_in INTEGER NOT NULL DEFAULT 0,
	bytes_out INTEGER NOT NULL DEFAULT 0,
	updated_at DATETIME NOT NULL DEFAULT (datetime('now')),
	FOREIGN KEY (tunnel_id) REFERENCES tunnels(id) ON DELETE CASCADE
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_traffic_stats_tunnel_id ON traffic_stats(tunnel_id);
CREATE INDEX IF NOT EXISTS idx_tunnel_keys_online_status ON tunnel_keys(online_status);

CREATE TRIGGER IF NOT EXISTS trg_tunnels_updated_at
AFTER UPDATE ON tunnels
FOR EACH ROW
WHEN NEW.updated_at = OLD.updated_at
BEGIN
	UPDATE tunnels SET updated_at = datetime('now') WHERE id = OLD.id;
END;

CREATE TRIGGER IF NOT EXISTS trg_tunnel_keys_updated_at
AFTER UPDATE ON tunnel_keys
FOR EACH ROW
WHEN NEW.updated_at = OLD.updated_at
BEGIN
	UPDATE tunnel_keys SET updated_at = datetime('now') WHERE id = OLD.id;
END;

CREATE TRIGGER IF NOT EXISTS trg_traffic_stats_updated_at
AFTER UPDATE ON traffic_stats
FOR EACH ROW
WHEN NEW.updated_at = OLD.updated_at
BEGIN
	UPDATE traffic_stats SET updated_at = datetime('now') WHERE id = OLD.id;
END;
`,
	},
	{
		Version: 3,
		Name:    "persist_plain_tunnel_secret",
		SQL: `
ALTER TABLE tunnel_keys ADD COLUMN secret_plain TEXT;
`,
	},
	{
		Version: 4,
		Name:    "legacy_client_compatibility",
		SQL: `
CREATE TABLE IF NOT EXISTS clients (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	name TEXT NOT NULL,
	secret_hash TEXT UNIQUE NOT NULL,
	secret_hint TEXT NOT NULL,
	status TEXT NOT NULL DEFAULT 'enabled',
	online_status TEXT NOT NULL DEFAULT 'offline',
	last_ip TEXT,
	last_seen_at DATETIME,
	last_error TEXT,
	remark TEXT,
	created_at DATETIME NOT NULL DEFAULT (datetime('now')),
	updated_at DATETIME NOT NULL DEFAULT (datetime('now'))
);

ALTER TABLE tunnels ADD COLUMN client_id INTEGER NOT NULL DEFAULT 0;

CREATE INDEX IF NOT EXISTS idx_clients_status ON clients(status);
CREATE INDEX IF NOT EXISTS idx_clients_online_status ON clients(online_status);
CREATE INDEX IF NOT EXISTS idx_tunnels_client_id ON tunnels(client_id);

CREATE TRIGGER IF NOT EXISTS trg_clients_updated_at
AFTER UPDATE ON clients
FOR EACH ROW
WHEN NEW.updated_at = OLD.updated_at
BEGIN
	UPDATE clients SET updated_at = datetime('now') WHERE id = OLD.id;
END;
`,
	},
}

// Migrate 执行数据库迁移：创建schema_migrations表→逐版本应用未执行的迁移→处理旧哈希格式。
func Migrate(ctx context.Context, database *sql.DB, log *logger.Logger) error {
	// Migrations are idempotent: schema_migrations records completed versions,
	// while CREATE IF NOT EXISTS keeps a fresh database and reruns consistent.
	if _, err := database.ExecContext(ctx, `
CREATE TABLE IF NOT EXISTS schema_migrations (
	version INTEGER PRIMARY KEY,
	name TEXT NOT NULL,
	applied_at DATETIME NOT NULL DEFAULT (datetime('now'))
);`); err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}

	for _, item := range serverMigrations {
		applied, err := migrationApplied(ctx, database, item.Version)
		if err != nil {
			return err
		}
		if applied {
			continue
		}
		if err := applyMigration(ctx, database, item); err != nil {
			return err
		}
		if log != nil {
			log.Infof("applied database migration %d %s", item.Version, item.Name)
		}
	}

	if err := resetLegacyPasswordHashes(ctx, database, "NATT_SERVER_ADMIN_PASSWORD", log); err != nil {
		return err
	}
	if err := migrateLegacyTunnelSecretHashes(ctx, database, log); err != nil {
		return err
	}
	if err := disableLegacyClientSecretHashes(ctx, database, log); err != nil {
		return err
	}
	return nil
}

// migrationApplied 检查指定版本的迁移是否已执行。
func migrationApplied(ctx context.Context, database *sql.DB, version int) (bool, error) {
	var exists int
	err := database.QueryRowContext(ctx, "SELECT 1 FROM schema_migrations WHERE version = ?", version).Scan(&exists)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("query schema migration %d: %w", version, err)
	}
	return true, nil
}

// applyMigration 在事务中应用单个迁移版本。
func applyMigration(ctx context.Context, database *sql.DB, item migration) error {
	tx, err := database.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin migration %d: %w", item.Version, err)
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, item.SQL); err != nil {
		return fmt.Errorf("apply migration %d %s: %w", item.Version, item.Name, err)
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO schema_migrations(version, name) VALUES(?, ?)", item.Version, item.Name); err != nil {
		return fmt.Errorf("record migration %d %s: %w", item.Version, item.Name, err)
	}
	return tx.Commit()
}

// resetLegacyPasswordHashes 将旧格式的用户密码哈希升级为当前SM3加盐格式。
func resetLegacyPasswordHashes(ctx context.Context, database *sql.DB, passwordEnv string, log *logger.Logger) error {
	rows, err := database.QueryContext(ctx, "SELECT id, password_hash FROM users;")
	if err != nil {
		return fmt.Errorf("query user password hashes: %w", err)
	}

	var legacyUserIDs []int64
	for rows.Next() {
		var id int64
		var passwordHash string
		if err := rows.Scan(&id, &passwordHash); err != nil {
			return fmt.Errorf("scan user password hash: %w", err)
		}
		if auth.IsCurrentPasswordHash(passwordHash) {
			continue
		}
		legacyUserIDs = append(legacyUserIDs, id)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate user password hashes: %w", err)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("close user password hash rows: %w", err)
	}

	password := strings.TrimSpace(os.Getenv(passwordEnv))
	if password == "" {
		if len(legacyUserIDs) > 0 && log != nil {
			log.Infof("legacy password hashes found but %s is not set; keeping existing hashes", passwordEnv)
		}
		return nil
	}
	for _, id := range legacyUserIDs {
		newHash, err := auth.HashPassword(password)
		if err != nil {
			return fmt.Errorf("hash reset password: %w", err)
		}
		if _, err := database.ExecContext(ctx, "UPDATE users SET password_hash = ? WHERE id = ?;", newHash, id); err != nil {
			return fmt.Errorf("reset legacy user password hash: %w", err)
		}
		if log != nil {
			log.Infof("reset legacy password hash user_id=%d", id)
		}
	}
	return nil
}

// migrateLegacyTunnelSecretHashes 将旧格式的隧道密钥哈希迁移为当前SM3格式。
func migrateLegacyTunnelSecretHashes(ctx context.Context, database *sql.DB, log *logger.Logger) error {
	rows, err := database.QueryContext(ctx, "SELECT id, secret_hash, COALESCE(secret_plain, '') FROM tunnel_keys;")
	if err != nil {
		return fmt.Errorf("query tunnel secret hashes: %w", err)
	}
	defer rows.Close()

	type update struct {
		id          int64
		secretHash  string
		secretHint  string
		disableOnly bool
	}
	var updates []update
	for rows.Next() {
		var id int64
		var secretHash string
		var secretPlain string
		if err := rows.Scan(&id, &secretHash, &secretPlain); err != nil {
			return fmt.Errorf("scan tunnel secret hash: %w", err)
		}
		if auth.IsCurrentPasswordHash(secretHash) {
			continue
		}
		secretPlain = strings.TrimSpace(secretPlain)
		if secretPlain == "" {
			updates = append(updates, update{id: id, disableOnly: true})
			continue
		}
		newHash, err := auth.HashPassword(secretPlain)
		if err != nil {
			return fmt.Errorf("hash tunnel secret: %w", err)
		}
		updates = append(updates, update{id: id, secretHash: newHash, secretHint: auth.SecretHint(secretPlain)})
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate tunnel secret hashes: %w", err)
	}

	for _, item := range updates {
		if item.disableOnly {
			if _, err := database.ExecContext(ctx, `
UPDATE tunnel_keys
SET status = 'disabled', online_status = 'offline'
WHERE id = ?;`, item.id); err != nil {
				return fmt.Errorf("disable legacy tunnel key: %w", err)
			}
			if log != nil {
				log.Infof("disabled legacy tunnel key without secret_plain key_id=%d", item.id)
			}
			continue
		}
		if _, err := database.ExecContext(ctx, `
UPDATE tunnel_keys
SET secret_hash = ?, secret_hint = ?
WHERE id = ?;`, item.secretHash, item.secretHint, item.id); err != nil {
			return fmt.Errorf("migrate tunnel secret hash: %w", err)
		}
		if log != nil {
			log.Infof("migrated tunnel secret hash key_id=%d", item.id)
		}
	}
	return nil
}

// disableLegacyClientSecretHashes 禁用使用旧哈希格式的客户端密钥。
func disableLegacyClientSecretHashes(ctx context.Context, database *sql.DB, log *logger.Logger) error {
	rows, err := database.QueryContext(ctx, "SELECT id, secret_hash FROM clients;")
	if err != nil {
		return fmt.Errorf("query client secret hashes: %w", err)
	}
	defer rows.Close()

	var ids []int64
	for rows.Next() {
		var id int64
		var secretHash string
		if err := rows.Scan(&id, &secretHash); err != nil {
			return fmt.Errorf("scan client secret hash: %w", err)
		}
		if !auth.IsCurrentPasswordHash(secretHash) {
			ids = append(ids, id)
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate client secret hashes: %w", err)
	}
	for _, id := range ids {
		if _, err := database.ExecContext(ctx, `
UPDATE clients
SET status = 'disabled', online_status = 'offline'
WHERE id = ?;`, id); err != nil {
			return fmt.Errorf("disable legacy client secret hash: %w", err)
		}
		if log != nil {
			log.Infof("disabled legacy client secret hash client_id=%d", id)
		}
	}
	return nil
}
