package api

import (
	"net/http"
	"testing"

	"nattserver/internal/db"
)

func TestOpsDashboardAuditAndConfigFlow(t *testing.T) {
	router, database, tokens := setupAuthenticatedServerRouter(t)
	defer database.Close()

	clientResp := authorizedJSON(t, router, http.MethodPost, "/api/server/v1/clients", tokens.AccessToken, map[string]string{
		"name": "ops-client",
	})
	var createdClient clientSecretResponse
	decodeResponseData(t, clientResp, &createdClient)

	tunnelResp := authorizedJSON(t, router, http.MethodPost, "/api/server/v1/tunnels", tokens.AccessToken, map[string]any{
		"name":        "ops-tunnel",
		"client_id":   createdClient.Client.ID,
		"local_host":  "127.0.0.1",
		"local_port":  8080,
		"remote_port": 18081,
	})
	var createdTunnel struct {
		ID int64 `json:"id"`
	}
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

	updateResp := authorizedJSON(t, router, http.MethodPut, "/api/server/v1/config", tokens.AccessToken, map[string]any{
		"settings": map[string]string{
			"log.level":              "debug",
			"tunnel.remote_port_min": "20000",
			"http.port":              "18088",
		},
	})
	var updateData struct {
		Updated []configUpdateResult `json:"updated"`
	}
	decodeResponseData(t, updateResp, &updateData)
	if len(updateData.Updated) != 3 {
		t.Fatalf("updated count=%d want=3", len(updateData.Updated))
	}
	if !hasConfigResult(updateData.Updated, "log.level", true, false) {
		t.Fatalf("missing hot reload log.level result: %+v", updateData.Updated)
	}
	if !hasConfigResult(updateData.Updated, "http.port", false, true) {
		t.Fatalf("missing restart required http.port result: %+v", updateData.Updated)
	}

	rejected := authorizedJSONAllowStatus(t, router, http.MethodPost, "/api/server/v1/tunnels", tokens.AccessToken, map[string]any{
		"name":        "too-low",
		"client_id":   createdClient.Client.ID,
		"local_host":  "127.0.0.1",
		"local_port":  8080,
		"remote_port": 19999,
	}, http.StatusBadRequest)
	assertResponseCode(t, rejected, CodeBadRequest)
}

func hasConfigResult(results []configUpdateResult, key string, hotReloaded bool, restartRequired bool) bool {
	for _, result := range results {
		if result.Key == key && result.HotReloaded == hotReloaded && result.RestartRequired == restartRequired {
			return true
		}
	}
	return false
}
