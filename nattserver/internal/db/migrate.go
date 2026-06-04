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

type migration struct {
	Version int
	Name    string
	SQL     string
}

const defaultAdminUsername = "admin"
const defaultAdminPassword = "admin123456"

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

	if err := seedDefaultAdmin(ctx, database, log); err != nil {
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

func seedDefaultAdmin(ctx context.Context, database *sql.DB, log *logger.Logger) error {
	var count int
	if err := database.QueryRowContext(ctx, "SELECT COUNT(1) FROM users").Scan(&count); err != nil {
		return fmt.Errorf("count users: %w", err)
	}
	if count > 0 {
		return nil
	}

	// The default admin is created only once. Environment variables are read at
	// first boot so production can avoid shipping the documented dev password.
	username := strings.TrimSpace(os.Getenv("NATT_SERVER_ADMIN_USERNAME"))
	if username == "" {
		username = defaultAdminUsername
	}
	password := os.Getenv("NATT_SERVER_ADMIN_PASSWORD")
	if password == "" {
		password = defaultAdminPassword
	}
	hash, err := auth.HashPassword(password)
	if err != nil {
		return fmt.Errorf("hash default admin password: %w", err)
	}
	if _, err := database.ExecContext(ctx, `
INSERT INTO users(username, password_hash, role)
VALUES(?, ?, 'admin');`, username, hash); err != nil {
		return fmt.Errorf("insert default admin: %w", err)
	}
	if log != nil {
		log.Infof("default admin initialized username=%s", username)
		if password == defaultAdminPassword {
			log.Infof("default admin uses initial password; change it before production use")
		}
	}
	return nil
}
