package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"nattserver/internal/db"
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
		"tool":   "server.get_dashboard",
		"params": map[string]any{},
	})
	if err != nil {
		t.Fatalf("marshal mcp request: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/mcp/tools/call", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer server-mcp-token")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("mcp status=%d body=%s", rec.Code, rec.Body.String())
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
