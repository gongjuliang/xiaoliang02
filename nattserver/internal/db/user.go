package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"nattserver/internal/model"
)

type CreateUserParams struct {
	Username     string
	PasswordHash string
	Role         model.UserRole
}

func FindUserByUsername(ctx context.Context, database *sql.DB, username string) (model.User, error) {
	var user model.User
	err := database.QueryRowContext(ctx, `
SELECT id, username, password_hash, role, created_at, updated_at
FROM users
WHERE username = ?;`, username).Scan(
		&user.ID,
		&user.Username,
		&user.PasswordHash,
		&user.Role,
		&user.CreatedAt,
		&user.UpdatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return model.User{}, ErrNotFound
	}
	if err != nil {
		return model.User{}, fmt.Errorf("find user by username: %w", err)
	}
	return user, nil
}

func CreateUser(ctx context.Context, database *sql.DB, params CreateUserParams) (model.User, error) {
	role := params.Role
	if role == "" {
		role = model.UserRoleAdmin
	}
	result, err := database.ExecContext(ctx, `
INSERT INTO users(username, password_hash, role)
VALUES(?, ?, ?);`, params.Username, params.PasswordHash, role)
	if err != nil {
		return model.User{}, fmt.Errorf("create user: %w", err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		return model.User{}, fmt.Errorf("created user id: %w", err)
	}
	return FindUserByID(ctx, database, id)
}

func FindUserByID(ctx context.Context, database *sql.DB, id int64) (model.User, error) {
	var user model.User
	err := database.QueryRowContext(ctx, `
SELECT id, username, password_hash, role, created_at, updated_at
FROM users
WHERE id = ?;`, id).Scan(
		&user.ID,
		&user.Username,
		&user.PasswordHash,
		&user.Role,
		&user.CreatedAt,
		&user.UpdatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return model.User{}, ErrNotFound
	}
	if err != nil {
		return model.User{}, fmt.Errorf("find user by id: %w", err)
	}
	return user, nil
}

func CountUsers(ctx context.Context, database *sql.DB) (int, error) {
	var count int
	if err := database.QueryRowContext(ctx, "SELECT COUNT(1) FROM users;").Scan(&count); err != nil {
		return 0, fmt.Errorf("count users: %w", err)
	}
	return count, nil
}
