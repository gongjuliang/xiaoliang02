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

const defaultAdminUsername = "admin"
const defaultAdminPassword = "admin123456"

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

CREATE TABLE IF NOT EXISTS server_connections (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	name TEXT NOT NULL,
	server_host TEXT NOT NULL,
	server_port INTEGER NOT NULL,
	data_port INTEGER NOT NULL,
	use_tls INTEGER NOT NULL DEFAULT 0,
	client_secret TEXT NOT NULL,
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

CREATE INDEX IF NOT EXISTS idx_server_connections_status ON server_connections(status);
CREATE INDEX IF NOT EXISTS idx_audit_logs_created_at ON audit_logs(created_at);

CREATE TRIGGER IF NOT EXISTS trg_users_updated_at
AFTER UPDATE ON users
FOR EACH ROW
WHEN NEW.updated_at = OLD.updated_at
BEGIN
	UPDATE users SET updated_at = datetime('now') WHERE id = OLD.id;
END;

CREATE TRIGGER IF NOT EXISTS trg_server_connections_updated_at
AFTER UPDATE ON server_connections
FOR EACH ROW
WHEN NEW.updated_at = OLD.updated_at
BEGIN
	UPDATE server_connections SET updated_at = datetime('now') WHERE id = OLD.id;
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
	username := strings.TrimSpace(os.Getenv("NATT_USER_ADMIN_USERNAME"))
	if username == "" {
		username = defaultAdminUsername
	}
	password := os.Getenv("NATT_USER_ADMIN_PASSWORD")
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
