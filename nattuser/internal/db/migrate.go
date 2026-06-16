// Package db 提供数据库版本迁移管理功能。
// 通过schema_migrations表追踪已应用的迁移版本，支持新数据库自动建表、
// 旧数据库增量升级、旧密码哈希重置和旧审计日志迁移到JSONL文件。
package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strings"

	"nattuser/internal/auth"
	"nattuser/internal/logger"
)

type migration struct {
	Version int
	Name    string
	SQL     string
}

var clientMigrations = []migration{
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

CREATE TABLE IF NOT EXISTS tunnel_connections (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	name TEXT NOT NULL,
	server_host TEXT NOT NULL,
	server_port INTEGER NOT NULL,
	data_port INTEGER NOT NULL,
	use_tls INTEGER NOT NULL DEFAULT 0,
	client_secret TEXT NOT NULL,
	local_host TEXT NOT NULL,
	local_port INTEGER NOT NULL,
	status TEXT NOT NULL DEFAULT 'stopped',
	auto_start INTEGER NOT NULL DEFAULT 0,
	last_error TEXT,
	remark TEXT,
	created_at DATETIME NOT NULL DEFAULT (datetime('now')),
	updated_at DATETIME NOT NULL DEFAULT (datetime('now'))
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

CREATE INDEX IF NOT EXISTS idx_tunnel_connections_status ON tunnel_connections(status);
CREATE INDEX IF NOT EXISTS idx_audit_logs_created_at ON audit_logs(created_at);

CREATE TRIGGER IF NOT EXISTS trg_users_updated_at
AFTER UPDATE ON users
FOR EACH ROW
WHEN NEW.updated_at = OLD.updated_at
BEGIN
	UPDATE users SET updated_at = datetime('now') WHERE id = OLD.id;
END;

CREATE TRIGGER IF NOT EXISTS trg_tunnel_connections_updated_at
AFTER UPDATE ON tunnel_connections
FOR EACH ROW
WHEN NEW.updated_at = OLD.updated_at
BEGIN
	UPDATE tunnel_connections SET updated_at = datetime('now') WHERE id = OLD.id;
END;

CREATE TRIGGER IF NOT EXISTS trg_settings_updated_at
AFTER UPDATE ON settings
FOR EACH ROW
WHEN NEW.updated_at = OLD.updated_at
BEGIN
	UPDATE settings SET updated_at = datetime('now') WHERE key = OLD.key;
END;
`,
	},
	{
		Version: 3,
		Name:    "reset_to_tunnel_connections",
		SQL: `
DROP TABLE IF EXISTS local_tunnels;
DROP TABLE IF EXISTS server_connections;
DROP TABLE IF EXISTS tunnel_connections;

CREATE TABLE tunnel_connections (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	name TEXT NOT NULL,
	server_host TEXT NOT NULL,
	server_port INTEGER NOT NULL,
	data_port INTEGER NOT NULL,
	use_tls INTEGER NOT NULL DEFAULT 0,
	client_secret TEXT NOT NULL,
	local_host TEXT NOT NULL,
	local_port INTEGER NOT NULL,
	status TEXT NOT NULL DEFAULT 'stopped',
	auto_start INTEGER NOT NULL DEFAULT 0,
	last_error TEXT,
	remark TEXT,
	created_at DATETIME NOT NULL DEFAULT (datetime('now')),
	updated_at DATETIME NOT NULL DEFAULT (datetime('now'))
);

CREATE INDEX IF NOT EXISTS idx_tunnel_connections_status ON tunnel_connections(status);

CREATE TRIGGER IF NOT EXISTS trg_tunnel_connections_updated_at
AFTER UPDATE ON tunnel_connections
FOR EACH ROW
WHEN NEW.updated_at = OLD.updated_at
BEGIN
	UPDATE tunnel_connections SET updated_at = datetime('now') WHERE id = OLD.id;
END;
`,
	},
	{
		Version: 4,
		Name:    "local_tunnel_bindings",
		SQL: `
CREATE TABLE IF NOT EXISTS local_tunnels (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	name TEXT NOT NULL,
	server_connection_id INTEGER NOT NULL,
	server_tunnel_id INTEGER NOT NULL,
	local_host TEXT NOT NULL,
	local_port INTEGER NOT NULL,
	enabled INTEGER NOT NULL DEFAULT 1,
	last_error TEXT,
	remark TEXT,
	created_at DATETIME NOT NULL DEFAULT (datetime('now')),
	updated_at DATETIME NOT NULL DEFAULT (datetime('now')),
	FOREIGN KEY (server_connection_id) REFERENCES tunnel_connections(id) ON DELETE CASCADE,
	UNIQUE(server_connection_id, server_tunnel_id)
);

CREATE INDEX IF NOT EXISTS idx_local_tunnels_server_connection_id ON local_tunnels(server_connection_id);
CREATE INDEX IF NOT EXISTS idx_local_tunnels_server_tunnel_id ON local_tunnels(server_tunnel_id);

CREATE TRIGGER IF NOT EXISTS trg_local_tunnels_updated_at
AFTER UPDATE ON local_tunnels
FOR EACH ROW
WHEN NEW.updated_at = OLD.updated_at
BEGIN
	UPDATE local_tunnels SET updated_at = datetime('now') WHERE id = OLD.id;
END;
`,
	},
	{
		Version: 5,
		Name:    "server_remote_port_display",
		SQL: `
ALTER TABLE tunnel_connections ADD COLUMN remote_port INTEGER NOT NULL DEFAULT 0;
`,
	},
}

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

	for _, item := range clientMigrations {
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

	if err := resetLegacyPasswordHashes(ctx, database, "NATT_USER_ADMIN_PASSWORD", log); err != nil {
		return err
	}
	return nil
}

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
