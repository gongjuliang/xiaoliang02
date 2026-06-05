package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"nattserver/internal/db"
	"nattserver/internal/model"
)

func TestOpsDashboardAuditAndConfigFlow(t *testing.T) {
	router, database, tokens := setupAuthenticatedServerRouter(t)
	defer database.Close()

	tunnelResp := authorizedJSON(t, router, http.MethodPost, "/api/server/v1/tunnels", tokens.AccessToken, map[string]any{
		"name":        "ops-tunnel",
		"remote_port": 18081,
	})
	var createdTunnel tunnelSecretResponse
	decodeResponseData(t, tunnelResp, &createdTunnel)

	authorizedJSON(t, router, http.MethodPost, "/api/server/v1/tunnels/1/start", tokens.AccessToken, nil)

	dashboardResp := authorizedJSON(t, router, http.MethodGet, "/api/server/v1/dashboard", tokens.AccessToken, nil)
	var summary db.DashboardSummary
	decodeResponseData(t, dashboardResp, &summary)
	if summary.TotalClients != 1 || summary.TotalTunnels != 1 || summary.RunningTunnels != 1 {
		t.Fatalf("unexpected dashboard summary: %+v", summary)
	}

	auditResp := authorizedJSON(t, router, http.MethodGet, "/api/server/v1/audit-logs?page=1&page_size=10", tokens.AccessToken, nil)
	var auditPage PageResponse
	decodeResponseData(t, auditResp, &auditPage)
	if auditPage.Total < 3 {
		t.Fatalf("audit total=%d want at least 3", auditPage.Total)
	}

	configResp := authorizedJSON(t, router, http.MethodGet, "/api/server/v1/config", tokens.AccessToken, nil)
	var configData map[string]any
	decodeResponseData(t, configResp, &configData)
	if configData["current"] == nil || configData["editable_keys"] == nil {
		t.Fatalf("unexpected config data: %+v", configData)
	}
	assertEditableKeysAreHotReloadOnly(t, configData["editable_keys"])

	updateResp := authorizedJSON(t, router, http.MethodPut, "/api/server/v1/config", tokens.AccessToken, map[string]any{
		"settings": map[string]string{
			"log.level":              "debug",
			"tunnel.remote_port_min": "20000",
		},
	})
	var updateData struct {
		Updated []configUpdateResult `json:"updated"`
	}
	decodeResponseData(t, updateResp, &updateData)
	if len(updateData.Updated) != 2 {
		t.Fatalf("updated count=%d want=2", len(updateData.Updated))
	}
	if !hasConfigResult(updateData.Updated, "log.level", true, false) {
		t.Fatalf("missing hot reload log.level result: %+v", updateData.Updated)
	}

	restartConfig := authorizedJSONAllowStatus(t, router, http.MethodPut, "/api/server/v1/config", tokens.AccessToken, map[string]any{
		"settings": map[string]string{
			"http.port": "18088",
		},
	}, http.StatusBadRequest)
	assertResponseMessageContains(t, restartConfig, "该配置不支持热更新")

	rejected := authorizedJSONAllowStatus(t, router, http.MethodPost, "/api/server/v1/tunnels", tokens.AccessToken, map[string]any{
		"name":        "too-low",
		"remote_port": 19999,
	}, http.StatusBadRequest)
	assertResponseCode(t, rejected, CodeBadRequest)
}

func TestServerRouterServesMCPOnHTTPPort(t *testing.T) {
	router, database, _ := setupAuthenticatedServerRouter(t)
	defer database.Close()

	body, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "tools/call",
		"params": map[string]any{
			"name":      "server.get_dashboard",
			"arguments": map[string]any{},
		},
	})
	if err != nil {
		t.Fatalf("marshal mcp request: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer server-mcp-token")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("mcp status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestServerConfigUpdateAllowsZeroRemotePortRangeMinimum(t *testing.T) {
	router, database, tokens := setupAuthenticatedServerRouter(t)
	defer database.Close()

	updateResp := authorizedJSON(t, router, http.MethodPut, "/api/server/v1/config", tokens.AccessToken, map[string]any{
		"settings": map[string]string{
			"tunnel.remote_port_min": "0",
		},
	})
	var updateData struct {
		Updated []configUpdateResult `json:"updated"`
	}
	decodeResponseData(t, updateResp, &updateData)
	if !hasConfigResult(updateData.Updated, "tunnel.remote_port_min", true, false) {
		t.Fatalf("missing hot reload tunnel.remote_port_min result: %+v", updateData.Updated)
	}
}

func TestServerMCPConfigTokenIsSystemManaged(t *testing.T) {
	router, database, tokens := setupAuthenticatedServerRouter(t)
	defer database.Close()

	if err := db.UpsertSetting(context.Background(), database, "mcp.access_token", ""); err != nil {
		t.Fatalf("clear mcp token: %v", err)
	}

	customResp := authorizedJSONAllowStatus(t, router, http.MethodPut, "/api/server/v1/mcp-config", tokens.AccessToken, map[string]any{
		"enabled":      true,
		"access_token": "custom-token",
	}, http.StatusBadRequest)
	assertResponseMessageContains(t, customResp, "MCP Key")

	enableResp := authorizedJSON(t, router, http.MethodPut, "/api/server/v1/mcp-config", tokens.AccessToken, map[string]any{
		"enabled": true,
	})
	var enabled struct {
		Enabled        bool   `json:"enabled"`
		HasAccessToken bool   `json:"has_access_token"`
		AccessHint     string `json:"access_token_hint"`
	}
	decodeResponseData(t, enableResp, &enabled)
	if !enabled.Enabled || !enabled.HasAccessToken || enabled.AccessHint == "" {
		t.Fatalf("unexpected mcp enable response: %+v", enabled)
	}

	revealResp := authorizedJSON(t, router, http.MethodGet, "/api/server/v1/mcp-config/reveal-token", tokens.AccessToken, nil)
	var revealed struct {
		AccessToken string `json:"access_token"`
		AccessHint  string `json:"access_token_hint"`
	}
	decodeResponseData(t, revealResp, &revealed)
	if !strings.HasPrefix(revealed.AccessToken, "xiaoliang_") || revealed.AccessHint == "" {
		t.Fatalf("unexpected revealed token: %+v", revealed)
	}

	logs, _, err := db.ListAuditLogs(context.Background(), database, 50, 0)
	if err != nil {
		t.Fatalf("list audit logs: %v", err)
	}
	if !hasAuditAction(logs, "mcp_token_reveal") {
		t.Fatalf("missing mcp_token_reveal audit log: %+v", logs)
	}
}

func hasConfigResult(results []configUpdateResult, key string, hotReloaded bool, restartRequired bool) bool {
	for _, result := range results {
		if result.Key == key && result.HotReloaded == hotReloaded && result.RestartRequired == restartRequired {
			return true
		}
	}
	return false
}

func hasAuditAction(logs []model.AuditLog, action string) bool {
	for _, item := range logs {
		if item.Action == action {
			return true
		}
	}
	return false
}

func assertEditableKeysAreHotReloadOnly(t *testing.T, raw any) {
	t.Helper()
	keys, ok := raw.([]any)
	if !ok {
		t.Fatalf("editable_keys has unexpected type %T", raw)
	}
	if len(keys) == 0 {
		t.Fatal("editable_keys must include hot reload keys")
	}
	for _, item := range keys {
		entry, ok := item.(map[string]any)
		if !ok {
			t.Fatalf("editable key has unexpected type %T", item)
		}
		if entry["hot_reload"] != true {
			t.Fatalf("editable key is not hot reloadable: %+v", entry)
		}
	}
}
