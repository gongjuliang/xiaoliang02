package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"nattuser/internal/db"
	"nattuser/internal/model"
)

func TestClientOpsStatusAuditAndConfigFlow(t *testing.T) {
	router, database, tokens := setupAuthenticatedClientRouter(t)
	defer database.Close()

	authorizedJSON(t, router, http.MethodPost, "/api/client/v1/tunnel-connections", tokens.AccessToken, map[string]any{
		"name":          "status-server",
		"server_host":   "example.com",
		"client_secret": "natt_status_secret",
		"local_host":    "127.0.0.1",
		"local_port":    8080,
	})
	authorizedJSON(t, router, http.MethodPost, "/api/client/v1/tunnel-connections/1/start", tokens.AccessToken, nil)

	statusResp := authorizedJSON(t, router, http.MethodGet, "/api/client/v1/status", tokens.AccessToken, nil)
	var statusData struct {
		Status            string                 `json:"status"`
		ServerConnections db.ClientStatusSummary `json:"server_connections"`
	}
	decodeResponseData(t, statusResp, &statusData)
	if statusData.Status != "ok" || statusData.ServerConnections.TotalServerConnections != 1 || statusData.ServerConnections.ConnectedServerConnections != 1 {
		t.Fatalf("unexpected status data: %+v", statusData)
	}

	tunnelsResp := authorizedJSON(t, router, http.MethodGet, "/api/client/v1/tunnels?page=1&page_size=10", tokens.AccessToken, nil)
	var tunnelsPage PageResponse
	decodeResponseData(t, tunnelsResp, &tunnelsPage)
	if tunnelsPage.Total != 0 {
		t.Fatalf("local tunnel total=%d want=0", tunnelsPage.Total)
	}

	auditResp := authorizedJSON(t, router, http.MethodGet, "/api/client/v1/audit-logs?page=1&page_size=10", tokens.AccessToken, nil)
	var auditPage PageResponse
	decodeResponseData(t, auditResp, &auditPage)
	if auditPage.Total < 3 {
		t.Fatalf("audit total=%d want at least 3", auditPage.Total)
	}

	configResp := authorizedJSON(t, router, http.MethodGet, "/api/client/v1/config", tokens.AccessToken, nil)
	var configData map[string]any
	decodeResponseData(t, configResp, &configData)
	if configData["current"] == nil || configData["editable_keys"] == nil {
		t.Fatalf("unexpected config data: %+v", configData)
	}
	assertEditableKeysAreHotReloadOnly(t, configData["editable_keys"])

	updateResp := authorizedJSON(t, router, http.MethodPut, "/api/client/v1/config", tokens.AccessToken, map[string]any{
		"settings": map[string]string{
			"log.level":                    "debug",
			"server_defaults.control_port": "7200",
		},
	})
	var updateData struct {
		Updated []configUpdateResult `json:"updated"`
	}
	decodeResponseData(t, updateResp, &updateData)
	if len(updateData.Updated) != 2 {
		t.Fatalf("updated count=%d want=2", len(updateData.Updated))
	}
	if !hasConfigResult(updateData.Updated, "server_defaults.control_port", true, false) {
		t.Fatalf("missing hot reload control port result: %+v", updateData.Updated)
	}

	defaultedResp := authorizedJSON(t, router, http.MethodPost, "/api/client/v1/tunnel-connections", tokens.AccessToken, map[string]any{
		"name":          "defaulted-after-config",
		"client_secret": "natt_default_after_config",
		"local_host":    "127.0.0.1",
		"local_port":    8080,
	})
	var defaulted model.ServerConnection
	decodeResponseData(t, defaultedResp, &defaulted)
	if defaulted.ServerPort != 7200 {
		t.Fatalf("hot updated defaults were not applied: %+v", defaulted)
	}

	restartConfig := authorizedJSONAllowStatus(t, router, http.MethodPut, "/api/client/v1/config", tokens.AccessToken, map[string]any{
		"settings": map[string]string{
			"http.port": "19080",
		},
	}, http.StatusBadRequest)
	assertResponseMessageContains(t, restartConfig, "该配置不支持热更新")

	badConfig := authorizedJSONAllowStatus(t, router, http.MethodPut, "/api/client/v1/config", tokens.AccessToken, map[string]any{
		"settings": map[string]string{
			"server_defaults.data_port": "70000",
		},
	}, http.StatusBadRequest)
	assertResponseCode(t, badConfig, CodeBadRequest)
}

func TestClientRouterServesMCPOnHTTPPort(t *testing.T) {
	router, database, _ := setupAuthenticatedClientRouter(t)
	defer database.Close()

	body, err := json.Marshal(map[string]any{
		"tool":   "client.get_network_status",
		"params": map[string]any{},
	})
	if err != nil {
		t.Fatalf("marshal mcp request: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/mcp/tools/call", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer client-mcp-token")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("mcp status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestClientMCPConfigTokenIsSystemManaged(t *testing.T) {
	router, database, tokens := setupAuthenticatedClientRouter(t)
	defer database.Close()

	if err := db.UpsertSetting(context.Background(), database, "mcp.access_token", ""); err != nil {
		t.Fatalf("clear mcp token: %v", err)
	}

	customResp := authorizedJSONAllowStatus(t, router, http.MethodPut, "/api/client/v1/mcp-config", tokens.AccessToken, map[string]any{
		"enabled":      true,
		"access_token": "custom-token",
	}, http.StatusBadRequest)
	assertResponseMessageContains(t, customResp, "MCP Key")

	enableResp := authorizedJSON(t, router, http.MethodPut, "/api/client/v1/mcp-config", tokens.AccessToken, map[string]any{
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

	revealResp := authorizedJSON(t, router, http.MethodGet, "/api/client/v1/mcp-config/reveal-token", tokens.AccessToken, nil)
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

func TestClientLocalTunnelCRUDFlow(t *testing.T) {
	router, database, tokens := setupAuthenticatedClientRouter(t)
	defer database.Close()

	serverResp := authorizedJSON(t, router, http.MethodPost, "/api/client/v1/tunnel-connections", tokens.AccessToken, map[string]any{
		"name":          "edge-server",
		"server_host":   "127.0.0.1",
		"client_secret": "secret-for-local-tunnel",
		"local_host":    "127.0.0.1",
		"local_port":    8080,
	})
	var server model.ServerConnection
	decodeResponseData(t, serverResp, &server)

	createResp := authorizedJSON(t, router, http.MethodPost, "/api/client/v1/tunnels", tokens.AccessToken, map[string]any{
		"name":                 "ssh-home",
		"server_connection_id": server.ID,
		"server_tunnel_id":     101,
		"local_host":           "127.0.0.1",
		"local_port":           22,
		"enabled":              true,
		"remark":               "local ssh",
	})
	var created model.LocalTunnel
	decodeResponseData(t, createResp, &created)
	if created.ID == 0 || created.ServerTunnelID != 101 || created.LocalHost != "127.0.0.1" || created.LocalPort != 22 || !created.Enabled {
		t.Fatalf("unexpected created local tunnel: %+v", created)
	}

	duplicateResp := authorizedJSONAllowStatus(t, router, http.MethodPost, "/api/client/v1/tunnels", tokens.AccessToken, map[string]any{
		"name":                 "duplicate",
		"server_connection_id": server.ID,
		"server_tunnel_id":     101,
		"local_host":           "127.0.0.1",
		"local_port":           2200,
	}, http.StatusConflict)
	assertResponseCode(t, duplicateResp, CodeConflict)

	updateResp := authorizedJSON(t, router, http.MethodPut, "/api/client/v1/tunnels/1", tokens.AccessToken, map[string]any{
		"name":                 "ssh-home-updated",
		"server_connection_id": server.ID,
		"server_tunnel_id":     101,
		"local_host":           "127.0.0.1",
		"local_port":           2222,
		"enabled":              false,
		"remark":               "updated",
	})
	var updated model.LocalTunnel
	decodeResponseData(t, updateResp, &updated)
	if updated.Name != "ssh-home-updated" || updated.LocalPort != 2222 || updated.Enabled {
		t.Fatalf("unexpected updated local tunnel: %+v", updated)
	}

	listResp := authorizedJSON(t, router, http.MethodGet, "/api/client/v1/tunnels?page=1&page_size=10", tokens.AccessToken, nil)
	var page PageResponse
	decodeResponseData(t, listResp, &page)
	if page.Total != 1 {
		t.Fatalf("local tunnel total=%d want=1", page.Total)
	}

	deleteResp := authorizedJSON(t, router, http.MethodDelete, "/api/client/v1/tunnels/1", tokens.AccessToken, nil)
	var deleted model.LocalTunnel
	decodeResponseData(t, deleteResp, &deleted)
	if deleted.ID != created.ID {
		t.Fatalf("deleted local tunnel id=%d want=%d", deleted.ID, created.ID)
	}
}

func hasAuditAction(logs []model.AuditLog, action string) bool {
	for _, item := range logs {
		if item.Action == action {
			return true
		}
	}
	return false
}

func hasConfigResult(results []configUpdateResult, key string, hotReloaded bool, restartRequired bool) bool {
	for _, result := range results {
		if result.Key == key && result.HotReloaded == hotReloaded && result.RestartRequired == restartRequired {
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
