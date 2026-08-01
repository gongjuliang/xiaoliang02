package db

import (
	"context"
	"strings"
	"time"
)

func RetrySQLiteBusy(ctx context.Context, operation func() error) error {
	var err error
	delay := 50 * time.Millisecond
	for attempt := 0; attempt < 4; attempt++ {
		err = operation()
		if err == nil || !isSQLiteBusy(err) || attempt == 3 {
			return err
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
		delay *= 2
	}
	return err
}

func isSQLiteBusy(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "sqlite_busy") ||
		strings.Contains(message, "sqlite_locked") ||
		strings.Contains(message, "database is locked") ||
		strings.Contains(message, "database table is locked")
}
