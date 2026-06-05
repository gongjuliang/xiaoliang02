package mcp

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"nattuser/internal/config"
	"nattuser/internal/db"
	"nattuser/internal/model"
)

type testJSONRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type testJSONRPCResponse struct {
	JSONRPC string            `json:"jsonrpc"`
	ID      int               `json:"id"`
	Result  json.RawMessage   `json:"result"`
	Error   *testJSONRPCError `json:"error"`
}

type testMCPToolResult struct {
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	StructuredContent json.RawMessage `json:"structuredContent"`
	IsError           bool            `json:"isError"`
}

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

	var page struct {
		Items []model.ServerConnection `json:"items"`
		Total int64                    `json:"total"`
	}
	decodeMCPToolStructured(t, rec, &page)
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

	var status struct {
		Hostname   string `json:"hostname"`
		Interfaces []any  `json:"interfaces"`
	}
	decodeMCPToolStructured(t, rec, &status)
	if status.Hostname == "" {
		t.Fatalf("hostname is empty in network status: %+v", status)
	}
}

func TestClientMCPInitializesListsToolsAndRejectsOldPath(t *testing.T) {
	router, database := setupClientMCPRouter(t)
	defer database.Close()

	initRec := callClientMCPMethod(t, router, "client-mcp-token", "initialize", map[string]any{
		"protocolVersion": "2025-06-18",
		"capabilities":    map[string]any{},
		"clientInfo": map[string]any{
			"name":    "codex-test",
			"version": "1.0.0",
		},
	})
	if initRec.Code != http.StatusOK {
		t.Fatalf("initialize status=%d body=%s", initRec.Code, initRec.Body.String())
	}
	var initResult struct {
		ProtocolVersion string `json:"protocolVersion"`
		ServerInfo      struct {
			Name string `json:"name"`
		} `json:"serverInfo"`
		Capabilities map[string]any `json:"capabilities"`
	}
	decodeMCPResult(t, initRec, &initResult)
	if initResult.ProtocolVersion != "2025-06-18" || initResult.ServerInfo.Name != "nattuser" {
		t.Fatalf("unexpected initialize result: %+v", initResult)
	}
	if _, ok := initResult.Capabilities["tools"]; !ok {
		t.Fatalf("initialize result missing tools capability: %+v", initResult)
	}

	listRec := callClientMCPMethod(t, router, "client-mcp-token", "tools/list", map[string]any{})
	if listRec.Code != http.StatusOK {
		t.Fatalf("tools/list status=%d body=%s", listRec.Code, listRec.Body.String())
	}
	var listResult struct {
		Tools []struct {
			Name        string         `json:"name"`
			Description string         `json:"description"`
			InputSchema map[string]any `json:"inputSchema"`
		} `json:"tools"`
	}
	decodeMCPResult(t, listRec, &listResult)
	if !hasTool(listResult.Tools, "client.list_tunnel_connections") || !hasTool(listResult.Tools, "client.get_network_status") {
		t.Fatalf("tools/list missing expected tools: %+v", listResult.Tools)
	}

	oldBody, _ := json.Marshal(map[string]any{"tool": "client.list_servers", "params": map[string]any{}})
	oldReq := httptest.NewRequest(http.MethodPost, "/mcp/tools/call", bytes.NewReader(oldBody))
	oldReq.Header.Set("Authorization", "Bearer client-mcp-token")
	oldReq.Header.Set("Content-Type", "application/json")
	oldRec := httptest.NewRecorder()
	router.ServeHTTP(oldRec, oldReq)
	if oldRec.Code != http.StatusNotFound && oldRec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("old MCP path status=%d body=%s", oldRec.Code, oldRec.Body.String())
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
	if rec.Code != http.StatusOK {
		t.Fatalf("unknown status=%d body=%s", rec.Code, rec.Body.String())
	}
	assertMCPToolError(t, rec, "unknown MCP tool")

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
	var connected model.ServerConnection
	decodeMCPToolStructured(t, rec, &connected)
	if connected.Status != model.ServerConnectionStatusConnected {
		t.Fatalf("status=%s want=%s", connected.Status, model.ServerConnectionStatusConnected)
	}

	rec = callClientMCP(t, router, "client-mcp-token", "client.disconnect_server", map[string]any{"id": 1})
	if rec.Code != http.StatusOK {
		t.Fatalf("disconnect status=%d body=%s", rec.Code, rec.Body.String())
	}
	var stopped model.ServerConnection
	decodeMCPToolStructured(t, rec, &stopped)
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
	var page struct {
		Items []localTunnelStatus `json:"items"`
		Total int64               `json:"total"`
	}
	decodeMCPToolStructured(t, rec, &page)
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
	return callClientMCPMethod(t, handler, token, "tools/call", map[string]any{
		"name":      tool,
		"arguments": params,
	})
}

func callClientMCPMethod(t *testing.T, handler http.Handler, token string, method string, params any) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  method,
		"params":  params,
	})
	if err != nil {
		t.Fatalf("encode request: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

func decodeMCPResult(t *testing.T, rec *httptest.ResponseRecorder, target any) {
	t.Helper()
	var resp testJSONRPCResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v body=%s", err, rec.Body.String())
	}
	if resp.JSONRPC != "2.0" || resp.ID != 1 {
		t.Fatalf("unexpected json-rpc envelope: %+v body=%s", resp, rec.Body.String())
	}
	if resp.Error != nil {
		t.Fatalf("unexpected json-rpc error: %+v body=%s", resp.Error, rec.Body.String())
	}
	if err := json.Unmarshal(resp.Result, target); err != nil {
		t.Fatalf("decode result: %v body=%s", err, rec.Body.String())
	}
}

func decodeMCPToolStructured(t *testing.T, rec *httptest.ResponseRecorder, target any) {
	t.Helper()
	var result testMCPToolResult
	decodeMCPResult(t, rec, &result)
	if result.IsError {
		t.Fatalf("unexpected tool error: %+v body=%s", result, rec.Body.String())
	}
	if len(result.Content) == 0 || result.Content[0].Type != "text" {
		t.Fatalf("tool result missing text content: %+v", result)
	}
	if err := json.Unmarshal(result.StructuredContent, target); err != nil {
		t.Fatalf("decode structured content: %v body=%s", err, rec.Body.String())
	}
}

func assertMCPToolError(t *testing.T, rec *httptest.ResponseRecorder, want string) {
	t.Helper()
	var result testMCPToolResult
	decodeMCPResult(t, rec, &result)
	if !result.IsError {
		t.Fatalf("expected tool error: %+v body=%s", result, rec.Body.String())
	}
	if len(result.Content) == 0 || !strings.Contains(result.Content[0].Text, want) {
		t.Fatalf("tool error %q does not contain %q", result.Content, want)
	}
}

func hasTool(tools []struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
}, name string) bool {
	for _, tool := range tools {
		if tool.Name == name && tool.Description != "" && tool.InputSchema["type"] == "object" {
			return true
		}
	}
	return false
}

func hasAuditAction(logs []model.AuditLog, action string) bool {
	for _, log := range logs {
		if log.Action == action && log.Actor == "mcp" {
			return true
		}
	}
	return false
}
