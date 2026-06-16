// Package db 提供NATT客户端的数据库操作层，包含SQLite数据库的初始化、
// 数据迁移、以及用户、隧道连接、本地隧道绑定、审计日志等实体的CRUD操作。
package db

// import "errors" 提供errors.New创建哨兵错误值，用于业务层判断数据库操作结果。
import "errors"

// 数据库层业务错误哨兵变量，供上层调用方通过errors.Is进行错误类型判断。
var (
	// ErrNotFound 表示数据库查询未找到目标记录。
	ErrNotFound = errors.New("not found")
	// ErrConflict 表示数据库操作发生冲突（如重复记录）。
	ErrConflict = errors.New("conflict")
)
