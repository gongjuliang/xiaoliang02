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

	"nattserver/internal/auth"
	"nattserver/internal/config"
	"nattserver/internal/db"
	"nattserver/internal/model"
)

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

	var resp mcpResponse
	decodeMCPResponse(t, rec, &resp)
	if !resp.Success || resp.Message != "ok" {
		t.Fatalf("unexpected response: %+v body=%s", resp, rec.Body.String())
	}

	var page struct {
		Items []model.Client `json:"items"`
		Total int64          `json:"total"`
	}
	if err := json.Unmarshal(resp.Data, &page); err != nil {
		t.Fatalf("decode page: %v", err)
	}
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

	var resp mcpResponse
	decodeMCPResponse(t, rec, &resp)
	var summary db.DashboardSummary
	if err := json.Unmarshal(resp.Data, &summary); err != nil {
		t.Fatalf("decode dashboard: %v", err)
	}
	if summary.TotalClients != 1 || summary.OnlineClients != 1 || summary.TotalTunnels != 1 {
		t.Fatalf("unexpected dashboard summary: %+v", summary)
	}
}

func TestServerMCPSupportsClientAndTunnelQueries(t *testing.T) {
	router, database := setupServerMCPRouter(t)
	defer database.Close()

	rec := callServerMCP(t, router, "server-mcp-token", "server.get_client", map[string]any{"id": 1})
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var getResp mcpResponse
	decodeMCPResponse(t, rec, &getResp)
	var client model.Client
	if err := json.Unmarshal(getResp.Data, &client); err != nil {
		t.Fatalf("decode client: %v", err)
	}
	if client.Name != "mcp-client" || client.OnlineStatus != model.OnlineStatusOnline {
		t.Fatalf("unexpected client: %+v", client)
	}

	rec = callServerMCP(t, router, "server-mcp-token", "server.list_tunnels", map[string]any{"page_size": 10})
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var listResp mcpResponse
	decodeMCPResponse(t, rec, &listResp)
	var page struct {
		Items []model.Tunnel `json:"items"`
		Total int64          `json:"total"`
	}
	if err := json.Unmarshal(listResp.Data, &page); err != nil {
		t.Fatalf("decode tunnels: %v", err)
	}
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
		"local_host":  "127.0.0.1",
		"local_port":  9000,
		"remote_host": "0.0.0.0",
		"remote_port": 18081,
		"auto_start":  true,
		"remark":      "created by mcp",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("create status=%d body=%s", rec.Code, rec.Body.String())
	}
	var createResp mcpResponse
	decodeMCPResponse(t, rec, &createResp)
	var created model.Tunnel
	if err := json.Unmarshal(createResp.Data, &created); err != nil {
		t.Fatalf("decode created tunnel: %v", err)
	}
	if created.ID == 0 || created.Name != "mcp-created" || !created.AutoStart {
		t.Fatalf("unexpected created tunnel: %+v", created)
	}

	rec = callServerMCP(t, router, "server-mcp-token", "server.start_tunnel", map[string]any{"id": created.ID})
	if rec.Code != http.StatusOK {
		t.Fatalf("start status=%d body=%s", rec.Code, rec.Body.String())
	}
	var startResp mcpResponse
	decodeMCPResponse(t, rec, &startResp)
	var running model.Tunnel
	if err := json.Unmarshal(startResp.Data, &running); err != nil {
		t.Fatalf("decode running tunnel: %v", err)
	}
	if running.Status != model.TunnelStatusRunning {
		t.Fatalf("status=%s want=%s", running.Status, model.TunnelStatusRunning)
	}

	rec = callServerMCP(t, router, "server-mcp-token", "server.stop_tunnel", map[string]any{"id": created.ID})
	if rec.Code != http.StatusOK {
		t.Fatalf("stop status=%d body=%s", rec.Code, rec.Body.String())
	}
	var stopResp mcpResponse
	decodeMCPResponse(t, rec, &stopResp)
	var stopped model.Tunnel
	if err := json.Unmarshal(stopResp.Data, &stopped); err != nil {
		t.Fatalf("decode stopped tunnel: %v", err)
	}
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
	if total != 4 {
		t.Fatalf("audit total=%d want=4 logs=%+v", total, logs)
	}
	for _, want := range []string{"mcp_tunnel_create", "mcp_tunnel_start", "mcp_tunnel_stop", "mcp_tunnel_delete"} {
		if !hasAuditAction(logs, want) {
			t.Fatalf("missing audit action %s in %+v", want, logs)
		}
	}
}

func setupServerMCPRouter(t *testing.T) (http.Handler, *sql.DB) {
	t.Helper()

	ctx := context.Background()
	database, err := db.Open(ctx, filepath.Join(t.TempDir(), "server.db"), nil)
	if err != nil {
		t.Fatalf("open database: %v", err)
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
	if _, err := db.CreateTunnel(ctx, database, db.CreateTunnelParams{
		Name:       "mcp-tunnel",
		ClientID:   client.ID,
		Protocol:   model.TunnelProtocolTCP,
		LocalHost:  "127.0.0.1",
		LocalPort:  8080,
		RemoteHost: "0.0.0.0",
		RemotePort: 18080,
	}); err != nil {
		t.Fatalf("create tunnel: %v", err)
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
	body, err := json.Marshal(map[string]any{
		"tool":   tool,
		"params": params,
	})
	if err != nil {
		t.Fatalf("encode request: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/tools/call", bytes.NewReader(body))
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
