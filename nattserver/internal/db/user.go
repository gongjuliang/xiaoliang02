// Package db 提供用户(User)相关的数据库CRUD操作。
// 包括用户查询（按用户名/ID）、创建用户和用户计数等功能。
package db

import (
	// context 提供上下文传递。
	"context"
	// database/sql 提供数据库操作接口。
	"database/sql"
	// errors 提供错误判断（如sql.ErrNoRows）。
	"errors"
	// fmt 提供错误信息格式化。
	"fmt"

	// nattserver/internal/model 项目数据模型。
	"nattserver/internal/model"
)

// CreateUserParams 创建用户的参数结构体。
type CreateUserParams struct {
	// Username 用户登录名。
	Username string
	// PasswordHash 已哈希处理的密码（HMAC-SM3格式）。
	PasswordHash string
	// Role 用户角色（空值时默认为admin）。
	Role model.UserRole
}

// FindUserByUsername 根据用户名查询用户。
// 参数ctx：上下文。
// 参数database：数据库连接。
// 参数username：要查询的用户名。
// 返回值：找到的User和可能的错误（未找到时返回ErrNotFound）。
func FindUserByUsername(ctx context.Context, database *sql.DB, username string) (model.User, error) {
	// 声明User变量接收查询结果
	var user model.User
	// 执行单行查询，按用户名搜索
	err := database.QueryRowContext(ctx, `
SELECT id, username, password_hash, role, created_at, updated_at
FROM users
WHERE username = ?;`, username).Scan(
		&user.ID,           // 扫描用户ID
		&user.Username,     // 扫描用户名
		&user.PasswordHash, // 扫描密码哈希
		&user.Role,         // 扫描角色
		&user.CreatedAt,    // 扫描创建时间
		&user.UpdatedAt,    // 扫描更新时间
	)
	// 未找到记录，返回ErrNotFound哨兵错误
	if errors.Is(err, sql.ErrNoRows) {
		return model.User{}, ErrNotFound
	}
	// 其他数据库错误
	if err != nil {
		return model.User{}, fmt.Errorf("find user by username: %w", err)
	}
	return user, nil
}

// CreateUser 创建新用户并返回完整的User对象。
// 参数ctx：上下文。
// 参数database：数据库连接。
// 参数params：创建用户的参数（用户名、密码哈希、角色）。
// 返回值：创建的User和可能的错误。
func CreateUser(ctx context.Context, database *sql.DB, params CreateUserParams) (model.User, error) {
	// 默认角色为admin管理员
	role := params.Role
	if role == "" {
		role = model.UserRoleAdmin
	}
	// 执行INSERT语句创建用户
	result, err := database.ExecContext(ctx, `
INSERT INTO users(username, password_hash, role)
VALUES(?, ?, ?);`, params.Username, params.PasswordHash, role)
	if err != nil {
		return model.User{}, fmt.Errorf("create user: %w", err)
	}
	// 获取自增主键ID
	id, err := result.LastInsertId()
	if err != nil {
		return model.User{}, fmt.Errorf("created user id: %w", err)
	}
	// 查询并返回完整的用户对象
	return FindUserByID(ctx, database, id)
}

// FindUserByID 根据ID查询用户。
// 参数ctx：上下文。
// 参数database：数据库连接。
// 参数id：用户ID。
// 返回值：找到的User和可能的错误。
func FindUserByID(ctx context.Context, database *sql.DB, id int64) (model.User, error) {
	// 声明User变量
	var user model.User
	// 执行单行查询，按ID搜索
	err := database.QueryRowContext(ctx, `
SELECT id, username, password_hash, role, created_at, updated_at
FROM users
WHERE id = ?;`, id).Scan(
		&user.ID,           // 扫描ID
		&user.Username,     // 扫描用户名
		&user.PasswordHash, // 扫描密码哈希
		&user.Role,         // 扫描角色
		&user.CreatedAt,    // 扫描创建时间
		&user.UpdatedAt,    // 扫描更新时间
	)
	// 未找到
	if errors.Is(err, sql.ErrNoRows) {
		return model.User{}, ErrNotFound
	}
	// 其他错误
	if err != nil {
		return model.User{}, fmt.Errorf("find user by id: %w", err)
	}
	return user, nil
}

// CountUsers 统计系统中的用户总数。
// 用于判断是否需要运行初始化向导（用户数为0时需要初始化）。
// 参数ctx：上下文。
// 参数database：数据库连接。
// 返回值：用户总数和可能的错误。
func CountUsers(ctx context.Context, database *sql.DB) (int, error) {
	// 声明计数变量
	var count int
	// 执行COUNT查询
	if err := database.QueryRowContext(ctx, "SELECT COUNT(1) FROM users;").Scan(&count); err != nil {
		return 0, fmt.Errorf("count users: %w", err)
	}
	return count, nil
}
