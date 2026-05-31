package api

import (
	"net/http"
	"testing"

	"nattuser/internal/db"
	"nattuser/internal/model"
)

func TestClientOpsStatusAuditAndConfigFlow(t *testing.T) {
	router, database, tokens := setupAuthenticatedClientRouter(t)
	defer database.Close()

	authorizedJSON(t, router, http.MethodPost, "/api/client/v1/servers", tokens.AccessToken, map[string]any{
		"name":          "status-server",
		"server_host":   "example.com",
		"client_secret": "natt_status_secret",
	})
	authorizedJSON(t, router, http.MethodPost, "/api/client/v1/servers/1/start", tokens.AccessToken, nil)

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

	updateResp := authorizedJSON(t, router, http.MethodPut, "/api/client/v1/config", tokens.AccessToken, map[string]any{
		"settings": map[string]string{
			"log.level":                    "debug",
			"server_defaults.control_port": "7200",
			"server_defaults.use_tls":      "true",
			"http.port":                    "19080",
		},
	})
	var updateData struct {
		Updated []configUpdateResult `json:"updated"`
	}
	decodeResponseData(t, updateResp, &updateData)
	if len(updateData.Updated) != 4 {
		t.Fatalf("updated count=%d want=4", len(updateData.Updated))
	}
	if !hasConfigResult(updateData.Updated, "server_defaults.control_port", true, false) {
		t.Fatalf("missing hot reload control port result: %+v", updateData.Updated)
	}
	if !hasConfigResult(updateData.Updated, "http.port", false, true) {
		t.Fatalf("missing restart required http.port result: %+v", updateData.Updated)
	}

	defaultedResp := authorizedJSON(t, router, http.MethodPost, "/api/client/v1/servers", tokens.AccessToken, map[string]any{
		"name":          "defaulted-after-config",
		"client_secret": "natt_default_after_config",
	})
	var defaulted model.ServerConnection
	decodeResponseData(t, defaultedResp, &defaulted)
	if defaulted.ServerPort != 7200 || !defaulted.UseTLS {
		t.Fatalf("hot updated defaults were not applied: %+v", defaulted)
	}

	badConfig := authorizedJSONAllowStatus(t, router, http.MethodPut, "/api/client/v1/config", tokens.AccessToken, map[string]any{
		"settings": map[string]string{
			"server_defaults.data_port": "70000",
		},
	}, http.StatusBadRequest)
	assertResponseCode(t, badConfig, CodeBadRequest)
}

func hasConfigResult(results []configUpdateResult, key string, hotReloaded bool, restartRequired bool) bool {
	for _, result := range results {
		if result.Key == key && result.HotReloaded == hotReloaded && result.RestartRequired == restartRequired {
			return true
		}
	}
	return false
}
