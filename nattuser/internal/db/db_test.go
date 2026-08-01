package db

import (
	"context"
	"database/sql"
	"testing"
)

func TestOpenConfiguresPragmasForEveryPooledConnection(t *testing.T) {
	database, err := Open(context.Background(), ":memory:", nil)
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	defer database.Close()

	connections := make([]*sql.Conn, 0, 2)
	for range 2 {
		conn, err := database.Conn(context.Background())
		if err != nil {
			t.Fatalf("get pooled connection: %v", err)
		}
		connections = append(connections, conn)
	}
	defer func() {
		for _, conn := range connections {
			_ = conn.Close()
		}
	}()

	for index, conn := range connections {
		var foreignKeys, busyTimeout int
		if err := conn.QueryRowContext(context.Background(), "PRAGMA foreign_keys;").Scan(&foreignKeys); err != nil {
			t.Fatalf("connection %d foreign_keys: %v", index, err)
		}
		if err := conn.QueryRowContext(context.Background(), "PRAGMA busy_timeout;").Scan(&busyTimeout); err != nil {
			t.Fatalf("connection %d busy_timeout: %v", index, err)
		}
		if foreignKeys != 1 || busyTimeout != 5000 {
			t.Fatalf("connection %d pragmas foreign_keys=%d busy_timeout=%d, want 1 and 5000", index, foreignKeys, busyTimeout)
		}
	}
}
