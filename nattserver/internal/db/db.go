// Package db 提供SQLite数据库的初始化和连接管理。
// 使用modernc.org/sqlite（纯Go的SQLite实现，无需CGO），
// 自动创建数据库目录、配置WAL模式和外键约束、执行数据迁移。
package db

import (
	// context 提供上下文传递和超时控制。
	"context"
	// database/sql 提供Go标准数据库/SQL接口。
	"database/sql"
	// fmt 提供错误信息的格式化包装。
	"fmt"
	// os 提供目录创建功能。
	"os"
	// path/filepath 提供文件路径解析和拼接。
	"path/filepath"
	// time 提供连接空闲超时的时间设置。
	"time"

	// nattserver/internal/logger 项目内部日志包。
	"nattserver/internal/logger"

	// _ 匿名导入modernc.org/sqlite驱动（纯Go SQLite实现），
	// 通过init()函数自动注册"sqlite"驱动名，无需CGO依赖。
	_ "modernc.org/sqlite"
)

// Open 打开（或创建）SQLite数据库连接，并完成初始化配置。
// 配置流程：创建目录→打开连接→设置连接池参数→启用外键→设置WAL模式→
// 设置忙碌超时→验证连接→执行数据迁移。
// 参数ctx：上下文，用于设置PRAGMA和迁移。
// 参数path：数据库文件路径（如"xiaoliang02_server/data/natt.db"）。
// 参数log：日志记录器。
// 返回值：数据库连接句柄和可能的错误。
func Open(ctx context.Context, path string, log *logger.Logger) (*sql.DB, error) {
	// 确保数据库文件所在目录存在（权限755）
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("create database dir: %w", err)
	}

	// 打开SQLite数据库连接（使用modernc.org/sqlite驱动）
	database, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open sqlite database: %w", err)
	}
	// 设置最大打开连接数为5（SQLite不适合过多并发写）
	database.SetMaxOpenConns(5)
	// 设置连接最大空闲时间为5分钟
	database.SetConnMaxIdleTime(5 * time.Minute)

	// 启用外键约束检查（SQLite默认不检查外键）
	if _, err := database.ExecContext(ctx, "PRAGMA foreign_keys = ON;"); err != nil {
		database.Close() // 失败时关闭连接
		return nil, fmt.Errorf("enable sqlite foreign keys: %w", err)
	}
	// 设置WAL（Write-Ahead Logging）日志模式，提高并发读写性能
	if _, err := database.ExecContext(ctx, "PRAGMA journal_mode = WAL;"); err != nil {
		database.Close()
		return nil, fmt.Errorf("set sqlite wal mode: %w", err)
	}
	// 设置数据库忙碌超时为5000毫秒（等待锁释放的最长时间）
	if _, err := database.ExecContext(ctx, "PRAGMA busy_timeout = 5000;"); err != nil {
		database.Close()
		return nil, fmt.Errorf("set sqlite busy timeout: %w", err)
	}
	// Ping数据库验证连接可用
	if err := database.PingContext(ctx); err != nil {
		database.Close()
		return nil, fmt.Errorf("ping sqlite database: %w", err)
	}

	// 记录数据库初始化完成日志
	if log != nil {
		log.Infof("sqlite database initialized at %s", path)
	}
	// 执行数据库迁移（创建表结构、升级schema等）
	if err := Migrate(ctx, database, log); err != nil {
		database.Close() // 迁移失败时关闭连接
		return nil, fmt.Errorf("migrate sqlite database: %w", err)
	}
	return database, nil
}
