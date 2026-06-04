package db

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"nattuser/internal/auth"
	"nattuser/internal/model"
)

func TestOpenRunsMigrationsAndSeedsAdmin(t *testing.T) {
	database, err := Open(context.Background(), ":memory:", nil)
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	defer database.Close()

	for _, table := range []string{
		"schema_migrations",
		"users",
		"tunnel_connections",
		"local_tunnels",
		"audit_logs",
		"settings",
	} {
		var name string
		err := database.QueryRow(`
SELECT name FROM sqlite_master
WHERE type = 'table' AND name = ?;`, table).Scan(&name)
		if err != nil {
			t.Fatalf("table %s was not created: %v", table, err)
		}
	}

	var username string
	var passwordHash string
	err = database.QueryRow("SELECT username, password_hash FROM users WHERE role = 'admin' LIMIT 1").Scan(&username, &passwordHash)
	if err != nil {
		t.Fatalf("default admin was not created: %v", err)
	}
	if username != defaultAdminUsername {
		t.Fatalf("unexpected default admin username: %s", username)
	}
	if !auth.CheckPassword(defaultAdminPassword, passwordHash) {
		t.Fatal("default admin password hash is invalid")
	}
}

func TestDataPersistsAfterDatabaseReopen(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "nattuser.db")
	logDir := filepath.Join(dir, "logs")

	database, err := Open(ctx, dbPath, nil)
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	if err := ConfigureAuditLogDir(ctx, database, logDir); err != nil {
		t.Fatalf("configure audit log dir: %v", err)
	}
	connection, err := CreateServerConnection(ctx, database, CreateServerConnectionParams{
		Name:         "prod-server",
		ServerHost:   "127.0.0.1",
		ServerPort:   7000,
		DataPort:     7001,
		UseTLS:       true,
		ClientSecret: "natt_client_secret",
		AutoStart:    true,
		Remark:       "persisted server connection",
	})
	if err != nil {
		t.Fatalf("create server connection: %v", err)
	}
	if err := UpsertSetting(ctx, database, "server_defaults.use_tls", "true"); err != nil {
		t.Fatalf("upsert setting: %v", err)
	}
	if err := InsertAuditLog(ctx, database, "admin", "persist_test", "server_connection", "1", "persisted audit", "127.0.0.1"); err != nil {
		t.Fatalf("insert audit log: %v", err)
	}
	assertSQLiteAuditLogCount(t, database, 0)
	if err := database.Close(); err != nil {
		t.Fatalf("close database: %v", err)
	}

	reopened, err := Open(ctx, dbPath, nil)
	if err != nil {
		t.Fatalf("reopen database: %v", err)
	}
	defer reopened.Close()
	if err := ConfigureAuditLogDir(ctx, reopened, logDir); err != nil {
		t.Fatalf("configure reopened audit log dir: %v", err)
	}

	storedConnection, err := GetServerConnectionByID(ctx, reopened, connection.ID)
	if err != nil {
		t.Fatalf("get persisted server connection: %v", err)
	}
	if storedConnection.Name != "prod-server" ||
		storedConnection.ServerHost != "127.0.0.1" ||
		storedConnection.ServerPort != 7000 ||
		storedConnection.DataPort != 7001 ||
		!storedConnection.UseTLS ||
		!storedConnection.AutoStart ||
		storedConnection.Remark != "persisted server connection" {
		t.Fatalf("unexpected persisted server connection: %+v", storedConnection)
	}
	settings, err := ListSettings(ctx, reopened)
	if err != nil {
		t.Fatalf("list persisted settings: %v", err)
	}
	if settingValue(settings, "server_defaults.use_tls") != "true" {
		t.Fatalf("persisted setting not found: %+v", settings)
	}
	logs, total, err := ListAuditLogs(ctx, reopened, 10, 0)
	if err != nil {
		t.Fatalf("list persisted audit logs: %v", err)
	}
	if total != 1 || logs[0].Action != "persist_test" || logs[0].Content != "persisted audit" {
		t.Fatalf("unexpected persisted audit logs total=%d logs=%+v", total, logs)
	}
}

func TestConfigureAuditLogDirMigratesSQLiteAuditLogsOnce(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	database, err := Open(ctx, filepath.Join(dir, "nattuser.db"), nil)
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	defer database.Close()

	if _, err := database.ExecContext(ctx, `
INSERT INTO audit_logs(actor, action, target_type, target_id, content, ip, created_at)
VALUES('admin', 'legacy_audit', 'server_connection', '1', 'from sqlite', '127.0.0.1', '2026-05-31 10:00:00');`); err != nil {
		t.Fatalf("insert legacy audit: %v", err)
	}

	logDir := filepath.Join(dir, "logs")
	if err := ConfigureAuditLogDir(ctx, database, logDir); err != nil {
		t.Fatalf("configure audit log dir: %v", err)
	}
	if err := ConfigureAuditLogDir(ctx, database, logDir); err != nil {
		t.Fatalf("configure audit log dir second time: %v", err)
	}

	logs, total, err := ListAuditLogs(ctx, database, 10, 0)
	if err != nil {
		t.Fatalf("list migrated audit logs: %v", err)
	}
	if total != 1 || logs[0].Action != "legacy_audit" || logs[0].Content != "from sqlite" {
		t.Fatalf("unexpected migrated audit logs total=%d logs=%+v", total, logs)
	}
}

func assertSQLiteAuditLogCount(t *testing.T, database *sql.DB, want int) {
	t.Helper()
	var count int
	if err := database.QueryRow("SELECT COUNT(1) FROM audit_logs").Scan(&count); err != nil {
		t.Fatalf("count sqlite audit logs: %v", err)
	}
	if count != want {
		t.Fatalf("sqlite audit log count=%d want=%d", count, want)
	}
}

func settingValue(settings []model.Setting, key string) string {
	for _, setting := range settings {
		if setting.Key == key {
			return setting.Value
		}
	}
	return ""
}
