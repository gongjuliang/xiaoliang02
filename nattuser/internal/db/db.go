package db

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"nattuser/internal/logger"

	_ "modernc.org/sqlite"
)

func Open(ctx context.Context, path string, log *logger.Logger) (*sql.DB, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("create database dir: %w", err)
	}

	database, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open sqlite database: %w", err)
	}
	database.SetMaxOpenConns(1)
	database.SetConnMaxIdleTime(5 * time.Minute)

	if _, err := database.ExecContext(ctx, "PRAGMA foreign_keys = ON;"); err != nil {
		database.Close()
		return nil, fmt.Errorf("enable sqlite foreign keys: %w", err)
	}
	if _, err := database.ExecContext(ctx, "PRAGMA busy_timeout = 5000;"); err != nil {
		database.Close()
		return nil, fmt.Errorf("set sqlite busy timeout: %w", err)
	}
	if err := database.PingContext(ctx); err != nil {
		database.Close()
		return nil, fmt.Errorf("ping sqlite database: %w", err)
	}

	if log != nil {
		log.Infof("sqlite database initialized at %s", path)
	}
	if err := Migrate(ctx, database, log); err != nil {
		database.Close()
		return nil, fmt.Errorf("migrate sqlite database: %w", err)
	}
	return database, nil
}
