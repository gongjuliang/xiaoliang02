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

	"nattserver/internal/auth"
	"nattserver/internal/config"
	"nattserver/internal/db"
	"nattserver/internal/model"
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

func TestServerMCPRequiresTokenAndListsClients(t *testing.T) {
	router, database := setupServerMCPRouter(t)
	defer database.Close()

	rec := callServerMCP(t, router, "", "server.list_clients", map[string]any{"page_size": 10})
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}

	rec = callServerMCP(t, router, "server-mcp-token", "server.list_clients", map[string]any{"page_size": 10})
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}

	var page struct {
		Items []model.Client `json:"items"`
		Total int64          `json:"total"`
	}
	decodeMCPToolStructured(t, rec, &page)
	if page.Total != 1 || len(page.Items) != 1 || page.Items[0].Name != "mcp-client" {
		t.Fatalf("unexpected client page: %+v", page)
	}
}

func TestServerMCPGetsDashboardFromSharedStats(t *testing.T) {
	router, database := setupServerMCPRouter(t)
	defer database.Close()

	rec := callServerMCP(t, router, "server-mcp-token", "server.get_dashboard", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}

	var summary db.DashboardSummary
	decodeMCPToolStructured(t, rec, &summary)
	if summary.TotalClients != 1 || summary.OnlineClients != 1 || summary.TotalTunnels != 1 {
		t.Fatalf("unexpected dashboard summary: %+v", summary)
	}
}

func TestServerMCPInitializesListsToolsAndRejectsOldPath(t *testing.T) {
	router, database := setupServerMCPRouter(t)
	defer database.Close()

	initRec := callServerMCPMethod(t, router, "server-mcp-token", "initialize", map[string]any{
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
	if initResult.ProtocolVersion != "2025-06-18" || initResult.ServerInfo.Name != "nattserver" {
		t.Fatalf("unexpected initialize result: %+v", initResult)
	}
	if _, ok := initResult.Capabilities["tools"]; !ok {
		t.Fatalf("initialize result missing tools capability: %+v", initResult)
	}

	listRec := callServerMCPMethod(t, router, "server-mcp-token", "tools/list", map[string]any{})
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
	if !hasTool(listResult.Tools, "server.list_tunnels") || !hasTool(listResult.Tools, "server.start_tunnel") {
		t.Fatalf("tools/list missing expected tools: %+v", listResult.Tools)
	}

	oldBody, _ := json.Marshal(map[string]any{"tool": "server.list_clients", "params": map[string]any{}})
	oldReq := httptest.NewRequest(http.MethodPost, "/mcp/tools/call", bytes.NewReader(oldBody))
	oldReq.Header.Set("Authorization", "Bearer server-mcp-token")
	oldReq.Header.Set("Content-Type", "application/json")
	oldRec := httptest.NewRecorder()
	router.ServeHTTP(oldRec, oldReq)
	if oldRec.Code != http.StatusNotFound && oldRec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("old MCP path status=%d body=%s", oldRec.Code, oldRec.Body.String())
	}
}

func TestServerMCPAuditsEveryToolCallAndSanitizesParams(t *testing.T) {
	router, database := setupServerMCPRouter(t)
	defer database.Close()

	rec := callServerMCP(t, router, "server-mcp-token", "server.list_clients", map[string]any{
		"page_size":     10,
		"client_secret": "must-not-leak",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("list status=%d body=%s", rec.Code, rec.Body.String())
	}
	rec = callServerMCP(t, router, "server-mcp-token", "server.unknown_tool", map[string]any{
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

func TestServerMCPSupportsClientAndTunnelQueries(t *testing.T) {
	router, database := setupServerMCPRouter(t)
	defer database.Close()

	rec := callServerMCP(t, router, "server-mcp-token", "server.get_client", map[string]any{"id": 1})
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var client model.Client
	decodeMCPToolStructured(t, rec, &client)
	if client.Name != "mcp-client" || client.OnlineStatus != model.OnlineStatusOnline {
		t.Fatalf("unexpected client: %+v", client)
	}

	rec = callServerMCP(t, router, "server-mcp-token", "server.list_tunnels", map[string]any{"page_size": 10})
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var page struct {
		Items []model.Tunnel `json:"items"`
		Total int64          `json:"total"`
	}
	decodeMCPToolStructured(t, rec, &page)
	if page.Total != 1 || len(page.Items) != 1 || page.Items[0].Name != "mcp-tunnel" {
		t.Fatalf("unexpected tunnel page: %+v", page)
	}
}

func TestServerMCPCreatesStartsStopsAndDeletesTunnelWithAuditLogs(t *testing.T) {
	router, database := setupServerMCPRouterWithRuntime(t)
	defer database.Close()

	rec := callServerMCP(t, router, "server-mcp-token", "server.create_tunnel", map[string]any{
		"name":        "mcp-created",
		"client_id":   1,
		"protocol":    "tcp",
		"remote_host": "0.0.0.0",
		"remote_port": 18081,
		"auto_start":  true,
		"remark":      "created by mcp",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("create status=%d body=%s", rec.Code, rec.Body.String())
	}
	var createdPayload struct {
		Tunnel model.Tunnel    `json:"tunnel"`
		Key    model.TunnelKey `json:"key"`
		Secret string          `json:"secret"`
	}
	decodeMCPToolStructured(t, rec, &createdPayload)
	created := createdPayload.Tunnel
	if created.ID == 0 || created.Name != "mcp-created" || !created.AutoStart {
		t.Fatalf("unexpected created tunnel payload: %+v", createdPayload)
	}
	if createdPayload.Secret == "" || createdPayload.Key.SecretHint == "" {
		t.Fatalf("created tunnel response missing secret data: %+v", createdPayload)
	}

	rec = callServerMCP(t, router, "server-mcp-token", "server.start_tunnel", map[string]any{"id": created.ID})
	if rec.Code != http.StatusOK {
		t.Fatalf("start status=%d body=%s", rec.Code, rec.Body.String())
	}
	var running model.Tunnel
	decodeMCPToolStructured(t, rec, &running)
	if running.Status != model.TunnelStatusRunning {
		t.Fatalf("status=%s want=%s", running.Status, model.TunnelStatusRunning)
	}

	rec = callServerMCP(t, router, "server-mcp-token", "server.stop_tunnel", map[string]any{"id": created.ID})
	if rec.Code != http.StatusOK {
		t.Fatalf("stop status=%d body=%s", rec.Code, rec.Body.String())
	}
	var stopped model.Tunnel
	decodeMCPToolStructured(t, rec, &stopped)
	if stopped.Status != model.TunnelStatusStopped {
		t.Fatalf("status=%s want=%s", stopped.Status, model.TunnelStatusStopped)
	}

	rec = callServerMCP(t, router, "server-mcp-token", "server.delete_tunnel", map[string]any{"id": created.ID})
	if rec.Code != http.StatusOK {
		t.Fatalf("delete status=%d body=%s", rec.Code, rec.Body.String())
	}
	if _, err := db.GetTunnelByID(context.Background(), database, created.ID); err != db.ErrNotFound {
		t.Fatalf("deleted tunnel lookup err=%v want ErrNotFound", err)
	}

	logs, total, err := db.ListAuditLogs(context.Background(), database, 10, 0)
	if err != nil {
		t.Fatalf("list audit logs: %v", err)
	}
	if total != 8 {
		t.Fatalf("audit total=%d want=8 logs=%+v", total, logs)
	}
	for _, want := range []string{"mcp_tool_call", "mcp_tunnel_create", "mcp_tunnel_start", "mcp_tunnel_stop", "mcp_tunnel_delete"} {
		if !hasAuditAction(logs, want) {
			t.Fatalf("missing audit action %s in %+v", want, logs)
		}
	}
}

func setupServerMCPRouter(t *testing.T) (http.Handler, *sql.DB) {
	t.Helper()

	ctx := context.Background()
	dir := t.TempDir()
	database, err := db.Open(ctx, filepath.Join(dir, "server.db"), nil)
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	if err := db.ConfigureAuditLogDir(ctx, database, filepath.Join(dir, "logs")); err != nil {
		t.Fatalf("configure audit log dir: %v", err)
	}

	secretHash, err := auth.HashPassword("client-secret")
	if err != nil {
		t.Fatalf("hash secret: %v", err)
	}
	client, err := db.CreateClient(ctx, database, db.CreateClientParams{
		Name:       "mcp-client",
		SecretHash: secretHash,
		SecretHint: auth.SecretHint("client-secret"),
	})
	if err != nil {
		t.Fatalf("create client: %v", err)
	}
	if err := db.MarkClientOnline(ctx, database, client.ID, "127.0.0.1"); err != nil {
		t.Fatalf("mark client online: %v", err)
	}
	tunnel, err := db.CreateTunnel(ctx, database, db.CreateTunnelParams{
		Name:       "mcp-tunnel",
		ClientID:   client.ID,
		Protocol:   model.TunnelProtocolTCP,
		RemoteHost: "0.0.0.0",
		RemotePort: 18080,
	})
	if err != nil {
		t.Fatalf("create tunnel: %v", err)
	}
	if err := db.MarkTunnelKeyOnline(ctx, database, tunnel.ID, "127.0.0.1"); err != nil {
		t.Fatalf("mark tunnel key online: %v", err)
	}

	router := NewServerRouter(config.MCPConfig{
		Enabled:     true,
		AccessToken: "server-mcp-token",
	}, database, nil, nil)
	return router, database
}

func setupServerMCPRouterWithRuntime(t *testing.T) (http.Handler, *sql.DB) {
	t.Helper()

	router, database := setupServerMCPRouter(t)
	_ = router
	return NewServerRouter(config.MCPConfig{
		Enabled:     true,
		AccessToken: "server-mcp-token",
	}, database, nil, fakeTunnelRuntime{database: database}), database
}

func callServerMCP(t *testing.T, handler http.Handler, token string, tool string, params any) *httptest.ResponseRecorder {
	t.Helper()
	return callServerMCPMethod(t, handler, token, "tools/call", map[string]any{
		"name":      tool,
		"arguments": params,
	})
}

func callServerMCPMethod(t *testing.T, handler http.Handler, token string, method string, params any) *httptest.ResponseRecorder {
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

type fakeTunnelRuntime struct {
	database *sql.DB
}

func (f fakeTunnelRuntime) StartTunnel(ctx context.Context, id int64) (model.Tunnel, error) {
	return db.SetTunnelStatus(ctx, f.database, id, model.TunnelStatusRunning, "")
}

func (f fakeTunnelRuntime) StopTunnel(ctx context.Context, id int64) (model.Tunnel, error) {
	return db.SetTunnelStatus(ctx, f.database, id, model.TunnelStatusStopped, "")
}

func hasAuditAction(logs []model.AuditLog, action string) bool {
	for _, log := range logs {
		if log.Action == action && log.Actor == "mcp" {
			return true
		}
	}
	return false
}
