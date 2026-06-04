package mcp

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"nattuser/internal/config"
	"nattuser/internal/db"
	"nattuser/internal/model"
)

func TestClientMCPRequiresTokenAndListsServers(t *testing.T) {
	router, database := setupClientMCPRouter(t)
	defer database.Close()

	rec := callClientMCP(t, router, "", "client.list_servers", map[string]any{"page_size": 10})
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}

	rec = callClientMCP(t, router, "client-mcp-token", "client.list_servers", map[string]any{"page_size": 10})
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}

	var resp mcpResponse
	decodeMCPResponse(t, rec, &resp)
	if !resp.Success || resp.Message != "ok" {
		t.Fatalf("unexpected response: %+v body=%s", resp, rec.Body.String())
	}

	var page struct {
		Items []model.ServerConnection `json:"items"`
		Total int64                    `json:"total"`
	}
	if err := json.Unmarshal(resp.Data, &page); err != nil {
		t.Fatalf("decode page: %v", err)
	}
	if page.Total != 1 || len(page.Items) != 1 || page.Items[0].Name != "mcp-server" {
		t.Fatalf("unexpected server page: %+v", page)
	}
}

func TestClientMCPGetsNetworkStatus(t *testing.T) {
	router, database := setupClientMCPRouter(t)
	defer database.Close()

	rec := callClientMCP(t, router, "client-mcp-token", "client.get_network_status", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}

	var resp mcpResponse
	decodeMCPResponse(t, rec, &resp)
	var status struct {
		Hostname   string `json:"hostname"`
		Interfaces []any  `json:"interfaces"`
	}
	if err := json.Unmarshal(resp.Data, &status); err != nil {
		t.Fatalf("decode status: %v", err)
	}
	if status.Hostname == "" {
		t.Fatalf("hostname is empty in network status: %+v", status)
	}
}

func TestClientMCPAuditsEveryToolCallAndSanitizesParams(t *testing.T) {
	router, database := setupClientMCPRouter(t)
	defer database.Close()

	rec := callClientMCP(t, router, "client-mcp-token", "client.list_servers", map[string]any{
		"page_size":     10,
		"client_secret": "must-not-leak",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("list status=%d body=%s", rec.Code, rec.Body.String())
	}
	rec = callClientMCP(t, router, "client-mcp-token", "client.unknown_tool", map[string]any{
		"access_token": "also-secret",
	})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("unknown status=%d body=%s", rec.Code, rec.Body.String())
	}

	logs, total, err := db.ListAuditLogs(context.Background(), database, 20, 0)
	if err != nil {
		t.Fatalf("list audit logs: %v", err)
	}
	if total != 2 {
		t.Fatalf("audit total=%d want=2 logs=%+v", total, logs)
	}
	if !hasAuditAction(logs, "mcp_tool_call") {
		t.Fatalf("missing mcp_tool_call audit: %+v", logs)
	}
	for _, item := range logs {
		if item.Action != "mcp_tool_call" {
			continue
		}
		if item.TargetType != "mcp_tool" || item.TargetID == "" {
			t.Fatalf("unexpected mcp audit target: %+v", item)
		}
		if bytes.Contains([]byte(item.Content), []byte("must-not-leak")) || bytes.Contains([]byte(item.Content), []byte("also-secret")) {
			t.Fatalf("mcp audit leaked secret content: %+v", item)
		}
		if !bytes.Contains([]byte(item.Content), []byte("[已脱敏]")) {
			t.Fatalf("mcp audit did not include sanitized marker: %+v", item)
		}
	}
}

func TestClientMCPConnectsDisconnectsAndWritesAuditLogs(t *testing.T) {
	router, database := setupClientMCPRouter(t)
	defer database.Close()

	rec := callClientMCP(t, router, "client-mcp-token", "client.connect_server", map[string]any{"id": 1})
	if rec.Code != http.StatusOK {
		t.Fatalf("connect status=%d body=%s", rec.Code, rec.Body.String())
	}
	var connectResp mcpResponse
	decodeMCPResponse(t, rec, &connectResp)
	var connected model.ServerConnection
	if err := json.Unmarshal(connectResp.Data, &connected); err != nil {
		t.Fatalf("decode connected server: %v", err)
	}
	if connected.Status != model.ServerConnectionStatusConnected {
		t.Fatalf("status=%s want=%s", connected.Status, model.ServerConnectionStatusConnected)
	}

	rec = callClientMCP(t, router, "client-mcp-token", "client.disconnect_server", map[string]any{"id": 1})
	if rec.Code != http.StatusOK {
		t.Fatalf("disconnect status=%d body=%s", rec.Code, rec.Body.String())
	}
	var disconnectResp mcpResponse
	decodeMCPResponse(t, rec, &disconnectResp)
	var stopped model.ServerConnection
	if err := json.Unmarshal(disconnectResp.Data, &stopped); err != nil {
		t.Fatalf("decode stopped server: %v", err)
	}
	if stopped.Status != model.ServerConnectionStatusStopped {
		t.Fatalf("status=%s want=%s", stopped.Status, model.ServerConnectionStatusStopped)
	}

	logs, total, err := db.ListAuditLogs(context.Background(), database, 10, 0)
	if err != nil {
		t.Fatalf("list audit logs: %v", err)
	}
	if total != 4 {
		t.Fatalf("audit total=%d want=4 logs=%+v", total, logs)
	}
	for _, want := range []string{"mcp_tool_call", "mcp_server_connect", "mcp_server_disconnect"} {
		if !hasAuditAction(logs, want) {
			t.Fatalf("missing audit action %s in %+v", want, logs)
		}
	}
}

func TestClientMCPListsLocalTunnels(t *testing.T) {
	router, database := setupClientMCPRouter(t)
	defer database.Close()

	rec := callClientMCP(t, router, "client-mcp-token", "client.list_tunnels", map[string]any{"page_size": 10})
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var resp mcpResponse
	decodeMCPResponse(t, rec, &resp)
	var page struct {
		Items []localTunnelStatus `json:"items"`
		Total int64               `json:"total"`
	}
	if err := json.Unmarshal(resp.Data, &page); err != nil {
		t.Fatalf("decode local tunnels: %v", err)
	}
	if page.Total != 1 || len(page.Items) != 1 || page.Items[0].ServerName != "mcp-server" {
		t.Fatalf("unexpected local tunnel page: %+v", page)
	}
}

func setupClientMCPRouter(t *testing.T) (http.Handler, *sql.DB) {
	t.Helper()

	ctx := context.Background()
	dir := t.TempDir()
	database, err := db.Open(ctx, filepath.Join(dir, "client.db"), nil)
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	if err := db.ConfigureAuditLogDir(ctx, database, filepath.Join(dir, "logs")); err != nil {
		t.Fatalf("configure audit log dir: %v", err)
	}
	if _, err := db.CreateServerConnection(ctx, database, db.CreateServerConnectionParams{
		Name:         "mcp-server",
		ServerHost:   "127.0.0.1",
		ServerPort:   7000,
		DataPort:     7001,
		ClientSecret: "client-secret",
	}); err != nil {
		t.Fatalf("create server connection: %v", err)
	}

	router := NewClientRouter(config.MCPConfig{
		Enabled:     true,
		AccessToken: "client-mcp-token",
	}, database, nil)
	return router, database
}

func callClientMCP(t *testing.T, handler http.Handler, token string, tool string, params any) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"tool":   tool,
		"params": params,
	})
	if err != nil {
		t.Fatalf("encode request: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/mcp/tools/call", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

func decodeMCPResponse(t *testing.T, rec *httptest.ResponseRecorder, target any) {
	t.Helper()
	if err := json.Unmarshal(rec.Body.Bytes(), target); err != nil {
		t.Fatalf("decode response: %v body=%s", err, rec.Body.String())
	}
}

func hasAuditAction(logs []model.AuditLog, action string) bool {
	for _, log := range logs {
		if log.Action == action && log.Actor == "mcp" {
			return true
		}
	}
	return false
}
