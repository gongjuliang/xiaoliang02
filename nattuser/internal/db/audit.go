package db

import (
	"context"
	"database/sql"
	"fmt"
)

func InsertAuditLog(ctx context.Context, database *sql.DB, actor string, action string, targetType string, targetID string, content string, ip string) error {
	if actor == "" {
		actor = "anonymous"
	}
	_, err := database.ExecContext(ctx, `
INSERT INTO audit_logs(actor, action, target_type, target_id, content, ip)
VALUES(?, ?, ?, ?, ?, ?);`, actor, action, targetType, targetID, content, ip)
	if err != nil {
		return fmt.Errorf("insert audit log: %w", err)
	}
	return nil
}
