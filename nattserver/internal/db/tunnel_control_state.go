package db

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

func MarkTunnelControlOffline(ctx context.Context, database *sql.DB, tunnelID int64, reason string) error {
	return RetrySQLiteBusy(ctx, func() error {
		tx, err := database.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("begin mark tunnel control offline: %w", err)
		}
		defer tx.Rollback()

		result, err := tx.ExecContext(ctx, `
UPDATE tunnel_keys
SET online_status = 'offline', last_seen_at = datetime('now')
WHERE tunnel_id = ?;`, tunnelID)
		if err != nil {
			return fmt.Errorf("mark tunnel key offline: %w", err)
		}
		if err := ensureRowsAffected(result, ErrNotFound); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
UPDATE clients
SET online_status = 'offline', last_seen_at = datetime('now')
WHERE id = (SELECT client_id FROM tunnels WHERE id = ? AND client_id > 0);`, tunnelID); err != nil {
			return fmt.Errorf("mark legacy client offline: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `
UPDATE tunnels
SET status = 'error', last_error = ?
WHERE id = ? AND status = 'running';`, strings.TrimSpace(reason), tunnelID); err != nil {
			return fmt.Errorf("mark tunnel unavailable: %w", err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit mark tunnel control offline: %w", err)
		}
		return nil
	})
}
