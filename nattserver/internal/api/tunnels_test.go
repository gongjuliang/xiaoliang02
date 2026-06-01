package api

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"nattserver/internal/model"
)

func TestTunnelManagementFlow(t *testing.T) {
	router, database, tokens := setupAuthenticatedServerRouter(t)
	defer database.Close()

	clientResp := authorizedJSON(t, router, http.MethodPost, "/api/server/v1/clients", tokens.AccessToken, map[string]string{
		"name": "office-client",
	})
	var createdClient clientSecretResponse
	decodeResponseData(t, clientResp, &createdClient)

	createBody := map[string]any{
		"name":        "web-8080",
		"client_id":   createdClient.Client.ID,
		"remote_port": 18080,
		"auto_start":  true,
		"remark":      "local web",
	}
	createResp := authorizedJSON(t, router, http.MethodPost, "/api/server/v1/tunnels", tokens.AccessToken, createBody)
	var created model.Tunnel
	decodeResponseData(t, createResp, &created)
	if created.ID == 0 || created.Protocol != model.TunnelProtocolTCP || !created.AutoStart {
		t.Fatalf("unexpected created tunnel: %+v", created)
	}
	if created.Status != model.TunnelStatusStopped {
		t.Fatalf("new tunnel status=%s want stopped", created.Status)
	}
	assertTrafficStatCount(t, database, created.ID, 1)

	conflictResp := authorizedJSONAllowStatus(t, router, http.MethodPost, "/api/server/v1/tunnels", tokens.AccessToken, createBody, http.StatusConflict)
	assertResponseCode(t, conflictResp, CodeConflict)

	listResp := authorizedJSON(t, router, http.MethodGet, "/api/server/v1/tunnels?client_id=1&page=1&page_size=10", tokens.AccessToken, nil)
	var page PageResponse
	decodeResponseData(t, listResp, &page)
	if page.Total != 1 {
		t.Fatalf("tunnel total=%d want=1", page.Total)
	}

	updateResp := authorizedJSON(t, router, http.MethodPut, "/api/server/v1/tunnels/1", tokens.AccessToken, map[string]any{
		"name":        "web-9090",
		"client_id":   createdClient.Client.ID,
		"protocol":    "tcp",
		"remote_host": "0.0.0.0",
		"remote_port": 19090,
		"auto_start":  false,
		"remark":      "updated",
	})
	var updated model.Tunnel
	decodeResponseData(t, updateResp, &updated)
	if updated.Name != "web-9090" || updated.RemotePort != 19090 || updated.AutoStart {
		t.Fatalf("unexpected updated tunnel: %+v", updated)
	}

	startResp := authorizedJSON(t, router, http.MethodPost, "/api/server/v1/tunnels/1/start", tokens.AccessToken, nil)
	var running model.Tunnel
	decodeResponseData(t, startResp, &running)
	if running.Status != model.TunnelStatusRunning {
		t.Fatalf("tunnel status=%s want running", running.Status)
	}

	stopResp := authorizedJSON(t, router, http.MethodPost, "/api/server/v1/tunnels/1/stop", tokens.AccessToken, nil)
	var stopped model.Tunnel
	decodeResponseData(t, stopResp, &stopped)
	if stopped.Status != model.TunnelStatusStopped {
		t.Fatalf("tunnel status=%s want stopped", stopped.Status)
	}

	deleteResp := authorizedJSON(t, router, http.MethodDelete, "/api/server/v1/tunnels/1", tokens.AccessToken, nil)
	var deleted model.Tunnel
	decodeResponseData(t, deleteResp, &deleted)
	if deleted.ID != created.ID {
		t.Fatalf("deleted tunnel id=%d want=%d", deleted.ID, created.ID)
	}
	assertTrafficStatCount(t, database, created.ID, 0)
	assertAuditLogCount(t, database, 7)
}

func TestTunnelCreateRejectsPortOutsideConfiguredRange(t *testing.T) {
	router, database, tokens := setupAuthenticatedServerRouter(t)
	defer database.Close()

	clientResp := authorizedJSON(t, router, http.MethodPost, "/api/server/v1/clients", tokens.AccessToken, map[string]string{
		"name": "office-client",
	})
	var createdClient clientSecretResponse
	decodeResponseData(t, clientResp, &createdClient)

	resp := authorizedJSONAllowStatus(t, router, http.MethodPost, "/api/server/v1/tunnels", tokens.AccessToken, map[string]any{
		"name":        "bad-port",
		"client_id":   createdClient.Client.ID,
		"remote_port": 9999,
	}, http.StatusBadRequest)
	assertResponseCode(t, resp, CodeBadRequest)
}

func TestTunnelCreateIgnoresLegacyLocalTargetFields(t *testing.T) {
	router, database, tokens := setupAuthenticatedServerRouter(t)
	defer database.Close()

	clientResp := authorizedJSON(t, router, http.MethodPost, "/api/server/v1/clients", tokens.AccessToken, map[string]string{
		"name": "office-client",
	})
	var createdClient clientSecretResponse
	decodeResponseData(t, clientResp, &createdClient)

	resp := authorizedJSON(t, router, http.MethodPost, "/api/server/v1/tunnels", tokens.AccessToken, map[string]any{
		"name":        "web-no-local",
		"client_id":   createdClient.Client.ID,
		"local_host":  "10.1.2.3",
		"local_port":  1234,
		"remote_port": 18081,
	})
	var created map[string]any
	decodeResponseData(t, resp, &created)
	if _, ok := created["local_host"]; ok {
		t.Fatalf("server tunnel response still exposes local_host: %+v", created)
	}
	if _, ok := created["local_port"]; ok {
		t.Fatalf("server tunnel response still exposes local_port: %+v", created)
	}
}

func TestTunnelStartReturnsClearConflictErrors(t *testing.T) {
	cases := []struct {
		name        string
		err         error
		wantMessage string
	}{
		{
			name:        "client offline",
			err:         fmt.Errorf("client 1 is not online"),
			wantMessage: "client 1 is not online",
		},
		{
			name:        "remote port conflict",
			err:         fmt.Errorf("listen remote port: bind: address already in use"),
			wantMessage: "listen remote port",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			router, database, tokens := setupAuthenticatedServerRouterWithRuntime(t, fakeTunnelRuntime{startErr: tc.err})
			defer database.Close()

			clientResp := authorizedJSON(t, router, http.MethodPost, "/api/server/v1/clients", tokens.AccessToken, map[string]string{
				"name": "office-client",
			})
			var createdClient clientSecretResponse
			decodeResponseData(t, clientResp, &createdClient)

			createResp := authorizedJSON(t, router, http.MethodPost, "/api/server/v1/tunnels", tokens.AccessToken, map[string]any{
				"name":        "web-8080",
				"client_id":   createdClient.Client.ID,
				"remote_port": 18080,
			})
			var created model.Tunnel
			decodeResponseData(t, createResp, &created)

			resp := authorizedJSONAllowStatus(t, router, http.MethodPost, "/api/server/v1/tunnels/1/start", tokens.AccessToken, nil, http.StatusConflict)
			assertResponseCode(t, resp, CodeConflict)
			assertResponseMessageContains(t, resp, tc.wantMessage)
		})
	}
}

type fakeTunnelRuntime struct {
	startErr error
	stopErr  error
}

func (r fakeTunnelRuntime) StartTunnel(ctx context.Context, id int64) (model.Tunnel, error) {
	if r.startErr != nil {
		return model.Tunnel{}, r.startErr
	}
	return model.Tunnel{ID: id, Status: model.TunnelStatusRunning}, nil
}

func (r fakeTunnelRuntime) StopTunnel(ctx context.Context, id int64) (model.Tunnel, error) {
	if r.stopErr != nil {
		return model.Tunnel{}, r.stopErr
	}
	return model.Tunnel{ID: id, Status: model.TunnelStatusStopped}, nil
}

func (r fakeTunnelRuntime) DisconnectClient(clientID int64) {}

func authorizedJSONAllowStatus(t *testing.T, router http.Handler, method string, path string, accessToken string, body any, wantStatus int) *httptest.ResponseRecorder {
	t.Helper()
	rec := authorizedJSONRaw(t, router, method, path, accessToken, body)
	if rec.Code != wantStatus {
		t.Fatalf("%s %s status=%d want=%d body=%s", method, path, rec.Code, wantStatus, rec.Body.String())
	}
	return rec
}

func authorizedJSONRaw(t *testing.T, router http.Handler, method string, path string, accessToken string, body any) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	req := makeJSONRequest(t, method, path, body)
	req.Header.Set("Authorization", "Bearer "+accessToken)
	router.ServeHTTP(rec, req)
	return rec
}

func makeJSONRequest(t *testing.T, method string, path string, body any) *http.Request {
	t.Helper()
	if body == nil {
		return httptest.NewRequest(method, path, nil)
	}
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal request body: %v", err)
	}
	req := httptest.NewRequest(method, path, bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	return req
}

func assertResponseCode(t *testing.T, rec *httptest.ResponseRecorder, want int) {
	t.Helper()
	var resp struct {
		Code int `json:"code"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response code: %v", err)
	}
	if resp.Code != want {
		t.Fatalf("response code=%d want=%d body=%s", resp.Code, want, rec.Body.String())
	}
}

func assertResponseMessageContains(t *testing.T, rec *httptest.ResponseRecorder, want string) {
	t.Helper()
	var resp struct {
		Message string `json:"message"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response message: %v", err)
	}
	if !strings.Contains(resp.Message, want) {
		t.Fatalf("response message=%q want contains %q body=%s", resp.Message, want, rec.Body.String())
	}
}

func assertTrafficStatCount(t *testing.T, database interface {
	QueryRow(query string, args ...any) *sql.Row
}, tunnelID int64, want int) {
	t.Helper()
	var count int
	if err := database.QueryRow("SELECT COUNT(1) FROM traffic_stats WHERE tunnel_id = ?", tunnelID).Scan(&count); err != nil {
		t.Fatalf("count traffic stats: %v", err)
	}
	if count != want {
		t.Fatalf("traffic stats count=%d want=%d", count, want)
	}
}
