package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"nattserver/internal/model"
)

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
