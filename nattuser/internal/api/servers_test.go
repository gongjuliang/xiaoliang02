package api

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

	"nattuser/internal/auth"
	"nattuser/internal/config"
	"nattuser/internal/db"
	"nattuser/internal/model"

	"github.com/gin-gonic/gin"
)

func TestServerConnectionManagementFlow(t *testing.T) {
	router, database, tokens := setupAuthenticatedClientRouter(t)
	defer database.Close()

	req := httptest.NewRequest(http.MethodGet, "/api/client/v1/tunnel-connections", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized list status=%d body=%s", rec.Code, rec.Body.String())
	}

	createResp := authorizedJSON(t, router, http.MethodPost, "/api/client/v1/tunnel-connections", tokens.AccessToken, map[string]any{
		"name":          "public-server",
		"server_host":   "example.com",
		"server_port":   7000,
		"data_port":     7001,
		"client_secret": "xiaoliang_test_secret",
		"local_host":    "127.0.0.1",
		"local_port":    8080,
		"auto_start":    true,
		"remark":        "prod",
	})
	var created model.ServerConnection
	decodeResponseData(t, createResp, &created)
	if created.ID == 0 || created.Status != model.ServerConnectionStatusStopped || !created.AutoStart {
		t.Fatalf("unexpected created server connection: %+v", created)
	}

	listResp := authorizedJSON(t, router, http.MethodGet, "/api/client/v1/tunnel-connections?page=1&page_size=10", tokens.AccessToken, nil)
	var listData struct {
		Items []struct {
			ID           int64  `json:"id"`
			ServerHost   string `json:"server_host"`
			ServerPort   int    `json:"server_port"`
			DataPort     int    `json:"data_port"`
			RemotePort   int    `json:"remote_port"`
			ClientSecret string `json:"client_secret"`
		} `json:"items"`
		Total int64 `json:"total"`
	}
	decodeResponseData(t, listResp, &listData)
	if listData.Total != 1 {
		t.Fatalf("server connection total=%d want=1", listData.Total)
	}
	if len(listData.Items) != 1 {
		t.Fatalf("server connection items=%d want=1", len(listData.Items))
	}
	item := listData.Items[0]
	if item.ServerHost != "example.com" || item.ServerPort != 7000 || item.DataPort != 7001 || item.RemotePort != 0 || item.ClientSecret != "xiaoliang_test_secret" {
		t.Fatalf("list did not expose expected connection display fields: %+v", item)
	}

	updateResp := authorizedJSON(t, router, http.MethodPut, "/api/client/v1/tunnel-connections/1", tokens.AccessToken, map[string]any{
		"name":          "public-server-renamed",
		"server_host":   "192.0.2.10",
		"server_port":   7100,
		"data_port":     7101,
		"client_secret": "xiaoliang_test_secret_2",
		"local_host":    "127.0.0.1",
		"local_port":    9090,
		"auto_start":    false,
		"remark":        "updated",
	})
	var updated model.ServerConnection
	decodeResponseData(t, updateResp, &updated)
	if updated.Name != "public-server-renamed" || updated.ServerPort != 7100 || updated.DataPort != 7101 || updated.AutoStart {
		t.Fatalf("unexpected updated server connection: %+v", updated)
	}

	startResp := authorizedJSON(t, router, http.MethodPost, "/api/client/v1/tunnel-connections/1/start", tokens.AccessToken, nil)
	var connected model.ServerConnection
	decodeResponseData(t, startResp, &connected)
	if connected.Status != model.ServerConnectionStatusConnected {
		t.Fatalf("server connection status=%s want connected", connected.Status)
	}

	stopResp := authorizedJSON(t, router, http.MethodPost, "/api/client/v1/tunnel-connections/1/stop", tokens.AccessToken, nil)
	var stopped model.ServerConnection
	decodeResponseData(t, stopResp, &stopped)
	if stopped.Status != model.ServerConnectionStatusStopped {
		t.Fatalf("server connection status=%s want stopped", stopped.Status)
	}

	deleteResp := authorizedJSON(t, router, http.MethodDelete, "/api/client/v1/tunnel-connections/1", tokens.AccessToken, nil)
	var deleted model.ServerConnection
	decodeResponseData(t, deleteResp, &deleted)
	if deleted.ID != created.ID {
		t.Fatalf("deleted server connection id=%d want=%d", deleted.ID, created.ID)
	}
	assertAuditLogCount(t, database, 6)
}

func TestServerConnectionCreateUsesDefaultsAndRejectsBadPorts(t *testing.T) {
	router, database, tokens := setupAuthenticatedClientRouter(t)
	defer database.Close()

	createResp := authorizedJSON(t, router, http.MethodPost, "/api/client/v1/tunnel-connections", tokens.AccessToken, map[string]any{
		"name":          "defaulted",
		"client_secret": "xiaoliang_default_secret",
		"local_host":    "127.0.0.1",
		"local_port":    8080,
	})
	var created model.ServerConnection
	decodeResponseData(t, createResp, &created)
	if created.ServerHost != "127.0.0.1" || created.ServerPort != 25511 || created.DataPort != 25512 {
		t.Fatalf("defaults were not applied: %+v", created)
	}

	resp := authorizedJSONAllowStatus(t, router, http.MethodPost, "/api/client/v1/tunnel-connections", tokens.AccessToken, map[string]any{
		"name":          "bad-port",
		"server_port":   70000,
		"client_secret": "xiaoliang_default_secret",
		"local_host":    "127.0.0.1",
		"local_port":    8080,
	}, http.StatusBadRequest)
	assertResponseCode(t, resp, CodeBadRequest)
}

func TestServerConnectionCreateReturnsFieldLevelValidationMessages(t *testing.T) {
	router, database, tokens := setupAuthenticatedClientRouter(t)
	defer database.Close()

	base := map[string]any{
		"name":          "validation-target",
		"server_host":   "127.0.0.1",
		"server_port":   7000,
		"data_port":     7001,
		"client_secret": "xiaoliang_validation_secret",
		"local_host":    "127.0.0.1",
		"local_port":    8080,
	}
	cases := []struct {
		name       string
		mutate     func(map[string]any)
		wantStatus int
		want       string
	}{
		{
			name: "missing name",
			mutate: func(body map[string]any) {
				delete(body, "name")
			},
			wantStatus: http.StatusBadRequest,
			want:       "name 为必填项",
		},
		{
			name: "missing client secret",
			mutate: func(body map[string]any) {
				delete(body, "client_secret")
			},
			wantStatus: http.StatusBadRequest,
			want:       "client_secret 为必填项",
		},
		{
			name: "missing local host",
			mutate: func(body map[string]any) {
				delete(body, "local_host")
			},
			wantStatus: http.StatusBadRequest,
			want:       "local_host 为必填项",
		},
		{
			name: "server port string",
			mutate: func(body map[string]any) {
				body["server_port"] = "abc"
			},
			wantStatus: http.StatusBadRequest,
			want:       "server_port 必须是数字",
		},
		{
			name: "local port too large",
			mutate: func(body map[string]any) {
				body["local_port"] = 70000
			},
			wantStatus: http.StatusBadRequest,
			want:       "local_port 必须在 1 到 65535 之间",
		},
		{
			name: "local port zero",
			mutate: func(body map[string]any) {
				body["local_port"] = 0
			},
			wantStatus: http.StatusBadRequest,
			want:       "local_port 必须在 1 到 65535 之间",
		},
		{
			name: "local port negative",
			mutate: func(body map[string]any) {
				body["local_port"] = -1
			},
			wantStatus: http.StatusBadRequest,
			want:       "local_port 必须在 1 到 65535 之间",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := cloneMap(base)
			tc.mutate(body)
			resp := authorizedJSONAllowStatus(t, router, http.MethodPost, "/api/client/v1/tunnel-connections", tokens.AccessToken, body, tc.wantStatus)
			assertResponseMessageContains(t, resp, tc.want)
			if strings.Contains(resp.Body.String(), "invalid server connection parameters") {
				t.Fatalf("response still contains vague validation message: %s", resp.Body.String())
			}
		})
	}
}

func setupAuthenticatedClientRouter(t *testing.T) (*gin.Engine, *sql.DB, auth.TokenPair) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	dir := t.TempDir()
	cfg := config.Default()
	cfg.Database.Path = filepath.Join(dir, "test.db")
	cfg.Log.Dir = filepath.Join(dir, "logs")
	cfg.Auth.JWTSecret = "test-secret"
	cfg.Auth.SM2PrivateKeyFile = filepath.Join(dir, "sm2_private.pem")
	cfg.Auth.SM2PublicKeyFile = filepath.Join(dir, "sm2_public.pem")
	cfg.MCP.Enabled = true
	cfg.MCP.AccessToken = "client-mcp-token"

	database, err := db.Open(context.Background(), cfg.Database.Path, nil)
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	if err := db.UpsertSetting(context.Background(), database, "mcp.enabled", "true"); err != nil {
		t.Fatalf("enable mcp setting: %v", err)
	}
	if err := db.UpsertSetting(context.Background(), database, "mcp.access_token", cfg.MCP.AccessToken); err != nil {
		t.Fatalf("set mcp token: %v", err)
	}
	seedTestAdmin(t, database)
	router := NewRouter(cfg, database, nil)
	publicKey := fetchPublicKey(t, router, "/api/client/v1/auth/sm2-public-key")
	encryptedPassword := encryptForPublicKey(t, publicKey, testAdminPassword)
	tokens := login(t, router, "/api/client/v1/auth/login", encryptedPassword)
	return router, database, tokens
}

func authorizedJSON(t *testing.T, router http.Handler, method string, path string, accessToken string, body any) *httptest.ResponseRecorder {
	t.Helper()
	rec := authorizedJSONRaw(t, router, method, path, accessToken, body)
	if rec.Code != http.StatusOK {
		t.Fatalf("%s %s status=%d body=%s", method, path, rec.Code, rec.Body.String())
	}
	return rec
}

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
	req := makeJSONRequest(t, method, path, body)
	req.Header.Set("Authorization", "Bearer "+accessToken)
	rec := httptest.NewRecorder()
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

func decodeResponseData(t *testing.T, rec *httptest.ResponseRecorder, target any) {
	t.Helper()
	var resp struct {
		Code int             `json:"code"`
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Code != CodeOK {
		t.Fatalf("response code=%d body=%s", resp.Code, rec.Body.String())
	}
	if err := json.Unmarshal(resp.Data, target); err != nil {
		t.Fatalf("decode data: %v body=%s", err, rec.Body.String())
	}
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

func cloneMap(src map[string]any) map[string]any {
	dst := make(map[string]any, len(src))
	for key, value := range src {
		dst[key] = value
	}
	return dst
}
