package db

import (
	"context"
	"testing"
)

func TestTunnelConnectionsPersistProtocolWithTCPDefault(t *testing.T) {
	database, err := Open(context.Background(), ":memory:", nil)
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	defer database.Close()

	connection, err := CreateServerConnection(context.Background(), database, CreateServerConnectionParams{
		Name:         "legacy-tcp",
		ServerHost:   "127.0.0.1",
		ServerPort:   25511,
		DataPort:     25512,
		ClientSecret: "xiaoliang_secret",
		LocalHost:    "127.0.0.1",
		LocalPort:    8080,
	})
	if err != nil {
		t.Fatalf("create connection: %v", err)
	}
	if connection.Protocol != "tcp" {
		t.Fatalf("protocol=%q want tcp", connection.Protocol)
	}
}
