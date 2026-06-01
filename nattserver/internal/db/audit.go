package db

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"nattserver/internal/model"
)

const auditMigrationSettingKey = "audit.file_migration_done"

var auditStore = struct {
	sync.RWMutex
	dir string
}{
	dir: filepath.Join("logs", "audit"),
}

type auditRecord struct {
	ID         int64  `json:"id"`
	Actor      string `json:"actor"`
	Action     string `json:"action"`
	TargetType string `json:"target_type"`
	TargetID   string `json:"target_id"`
	Content    string `json:"content"`
	IP         string `json:"ip"`
	CreatedAt  string `json:"created_at"`
}

func ConfigureAuditLogDir(ctx context.Context, database *sql.DB, logDir string) error {
	dir := filepath.Join(logDir, "audit")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create audit log dir: %w", err)
	}
	auditStore.Lock()
	auditStore.dir = dir
	auditStore.Unlock()
	if database != nil {
		if err := migrateSQLiteAuditLogs(ctx, database); err != nil {
			return err
		}
	}
	return nil
}

func InsertAuditLog(ctx context.Context, database *sql.DB, actor string, action string, targetType string, targetID string, content string, ip string) error {
	if actor == "" {
		actor = "anonymous"
	}
	record := model.AuditLog{
		ID:         time.Now().UnixNano(),
		Actor:      actor,
		Action:     action,
		TargetType: targetType,
		TargetID:   targetID,
		Content:    content,
		IP:         ip,
		CreatedAt:  time.Now().Format(time.RFC3339),
	}
	return appendAuditLog(record)
}

func appendAuditLog(record model.AuditLog) error {
	auditStore.RLock()
	dir := auditStore.dir
	auditStore.RUnlock()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create audit log dir: %w", err)
	}
	date := auditDate(record.CreatedAt)
	file, err := os.OpenFile(filepath.Join(dir, date+".jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("open audit log file: %w", err)
	}
	defer file.Close()
	raw, err := json.Marshal(auditRecord(record))
	if err != nil {
		return fmt.Errorf("encode audit log: %w", err)
	}
	if _, err := file.Write(append(raw, '\n')); err != nil {
		return fmt.Errorf("write audit log: %w", err)
	}
	return nil
}

func listAuditLogsFromFiles(limit int, offset int) ([]model.AuditLog, int64, error) {
	auditStore.RLock()
	dir := auditStore.dir
	auditStore.RUnlock()
	files, err := filepath.Glob(filepath.Join(dir, "*.jsonl"))
	if err != nil {
		return nil, 0, fmt.Errorf("glob audit logs: %w", err)
	}
	sort.Sort(sort.Reverse(sort.StringSlice(files)))
	var logs []model.AuditLog
	for _, file := range files {
		items, err := readAuditLogFile(file)
		if err != nil {
			return nil, 0, err
		}
		logs = append(logs, items...)
	}
	sort.SliceStable(logs, func(i, j int) bool {
		return logs[i].CreatedAt > logs[j].CreatedAt
	})
	total := int64(len(logs))
	if offset >= len(logs) {
		return []model.AuditLog{}, total, nil
	}
	end := offset + limit
	if end > len(logs) {
		end = len(logs)
	}
	return logs[offset:end], total, nil
}

func readAuditLogFile(path string) ([]model.AuditLog, error) {
	file, err := os.Open(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("open audit log file %s: %w", path, err)
	}
	defer file.Close()
	var logs []model.AuditLog
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var record auditRecord
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			return nil, fmt.Errorf("decode audit log file %s: %w", path, err)
		}
		logs = append(logs, model.AuditLog(record))
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan audit log file %s: %w", path, err)
	}
	return logs, nil
}

func migrateSQLiteAuditLogs(ctx context.Context, database *sql.DB) error {
	done, err := auditMigrationDone(ctx, database)
	if err != nil {
		return err
	}
	if done {
		return nil
	}
	rows, err := database.QueryContext(ctx, `
SELECT id, actor, action, target_type, target_id, content, ip, created_at
FROM audit_logs
ORDER BY id ASC;`)
	if err != nil {
		return fmt.Errorf("query sqlite audit logs: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var item model.AuditLog
		var targetType sql.NullString
		var targetID sql.NullString
		var ip sql.NullString
		if err := rows.Scan(&item.ID, &item.Actor, &item.Action, &targetType, &targetID, &item.Content, &ip, &item.CreatedAt); err != nil {
			return fmt.Errorf("scan sqlite audit log: %w", err)
		}
		item.TargetType = nullableString(targetType)
		item.TargetID = nullableString(targetID)
		item.IP = nullableString(ip)
		if err := appendAuditLog(item); err != nil {
			return err
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate sqlite audit logs: %w", err)
	}
	return UpsertSetting(ctx, database, auditMigrationSettingKey, "true")
}

func auditMigrationDone(ctx context.Context, database *sql.DB) (bool, error) {
	var value string
	err := database.QueryRowContext(ctx, "SELECT value FROM settings WHERE key = ?", auditMigrationSettingKey).Scan(&value)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("query audit migration setting: %w", err)
	}
	return value == "true", nil
}

func auditDate(createdAt string) string {
	if len(createdAt) >= len("2006-01-02") {
		return createdAt[:len("2006-01-02")]
	}
	return time.Now().Format("2006-01-02")
}
