package db

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"nattserver/internal/auth"
	"nattserver/internal/model"
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
		"clients",
		"tunnels",
		"audit_logs",
		"settings",
		"traffic_stats",
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
	dbPath := filepath.Join(dir, "nattserver.db")
	logDir := filepath.Join(dir, "logs")

	database, err := Open(ctx, dbPath, nil)
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	if err := ConfigureAuditLogDir(ctx, database, logDir); err != nil {
		t.Fatalf("configure audit log dir: %v", err)
	}
	secretHash, err := auth.HashPassword("natt_client_secret")
	if err != nil {
		t.Fatalf("hash client secret: %v", err)
	}
	client, err := CreateClient(ctx, database, CreateClientParams{
		Name:       "office-client",
		SecretHash: secretHash,
		SecretHint: auth.SecretHint("natt_client_secret"),
		Remark:     "persisted client",
	})
	if err != nil {
		t.Fatalf("create client: %v", err)
	}
	tunnel, err := CreateTunnel(ctx, database, CreateTunnelParams{
		Name:       "web",
		ClientID:   client.ID,
		Protocol:   model.TunnelProtocolTCP,
		LocalHost:  "127.0.0.1",
		LocalPort:  8080,
		RemoteHost: "0.0.0.0",
		RemotePort: 18080,
		AutoStart:  true,
		Remark:     "persisted tunnel",
	})
	if err != nil {
		t.Fatalf("create tunnel: %v", err)
	}
	if err := UpsertSetting(ctx, database, "protocol.tls.enabled", "true"); err != nil {
		t.Fatalf("upsert setting: %v", err)
	}
	if err := InsertAuditLog(ctx, database, "admin", "persist_test", "tunnel", "1", "persisted audit", "127.0.0.1"); err != nil {
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

	storedClient, err := GetClientByID(ctx, reopened, client.ID)
	if err != nil {
		t.Fatalf("get persisted client: %v", err)
	}
	if storedClient.Name != "office-client" || storedClient.Remark != "persisted client" {
		t.Fatalf("unexpected persisted client: %+v", storedClient)
	}
	storedTunnel, err := GetTunnelByID(ctx, reopened, tunnel.ID)
	if err != nil {
		t.Fatalf("get persisted tunnel: %v", err)
	}
	if storedTunnel.Name != "web" || !storedTunnel.AutoStart || storedTunnel.Remark != "persisted tunnel" {
		t.Fatalf("unexpected persisted tunnel: %+v", storedTunnel)
	}
	settings, err := ListSettings(ctx, reopened)
	if err != nil {
		t.Fatalf("list persisted settings: %v", err)
	}
	if settingValue(settings, "protocol.tls.enabled") != "true" {
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
	database, err := Open(ctx, filepath.Join(dir, "nattserver.db"), nil)
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	defer database.Close()

	if _, err := database.ExecContext(ctx, `
INSERT INTO audit_logs(actor, action, target_type, target_id, content, ip, created_at)
VALUES('admin', 'legacy_audit', 'tunnel', '1', 'from sqlite', '127.0.0.1', '2026-05-31 10:00:00');`); err != nil {
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
